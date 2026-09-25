package offsite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// memBackend is a target in memory; fail makes the next Put fail.
type memBackend struct {
	mu    sync.Mutex
	files map[string][]byte
	fail  error
}

func (m *memBackend) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return 0, m.fail
	}
	m.files[key] = b
	return int64(len(b)), nil
}

func (m *memBackend) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memBackend) List(_ context.Context, dir string) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Object
	for k, b := range m.files {
		if rest, ok := strings.CutPrefix(k, dir+"/"); ok && !strings.Contains(rest, "/") {
			out = append(out, Object{Key: k, Size: int64(len(b))})
		}
	}
	return out, nil
}

func (m *memBackend) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, key)
	return nil
}

func (m *memBackend) Close() error { return nil }

func (m *memBackend) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fakeProjects hands out one tar per backup and records imports.
type fakeProjects struct {
	backups  map[string]project.OffsiteArchive // by backup id, Body unused
	content  map[string][]byte
	imported [][]byte
}

func (f *fakeProjects) OpenOffsiteArchive(_ context.Context, _, backupID string) (project.OffsiteArchive, error) {
	a, ok := f.backups[backupID]
	if !ok {
		return project.OffsiteArchive{}, store.ErrNotFound
	}
	a.Body = io.NopCloser(bytes.NewReader(f.content[backupID]))
	return a, nil
}

func (f *fakeProjects) ImportBackupArchive(_ context.Context, _ string, r io.Reader) (project.BackupInfo, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return project.BackupInfo{}, err
	}
	f.imported = append(f.imported, b)
	return project.BackupInfo{ID: store.NewID()}, nil
}

func (f *fakeProjects) ProjectSlug(context.Context, string) (string, error) { return "shop", nil }

type recorder struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *recorder) Notify(_ context.Context, e notify.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}
func (r *recorder) Clear(string) {}

type harness struct {
	s       *Syncer
	st      *store.Store
	mem     *memBackend
	proj    *fakeProjects
	notes   *recorder
	project string
	now     time.Time
}

// today is a time of the real current day: instance backups carry the real creation
// time, so the syncer's clock must be on the same date.
func today(hour, min int) time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d, hour, min, 0, 0, time.Local)
}

func newHarness(t *testing.T, targets ...Target) *harness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	projectID := store.NewID()
	_, err = sqlDB.Exec(`INSERT INTO projects (id, name, slug, path, docroot, desired_state, lifecycle, created_at, updated_at) VALUES (?, 'Shop', 'shop', 'shop', '', 'stopped', 'ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := t.TempDir()
	cfg, _ := OpenConfig(cfgDir)
	for _, tg := range targets {
		p, err := cfg.Prepare(tg)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{st: st, mem: &memBackend{files: map[string][]byte{}}, proj: &fakeProjects{backups: map[string]project.OffsiteArchive{}, content: map[string][]byte{}}, notes: &recorder{}, project: projectID,
		now: today(3, 30)}
	inst := &instance.Store{ConfigDir: cfgDir, DBPath: filepath.Join(cfgDir, "none.db"), Dir: filepath.Join(t.TempDir(), "_instance"), Version: "test", LatestSchema: db.LatestVersion(), Log: log}
	h.s = &Syncer{Config: cfg, Store: st, Projects: h.proj, Instance: inst, Notify: h.notes, Log: log,
		CreateInstanceBackup: func(ctx context.Context, kind string) (instance.Info, error) {
			return inst.Create(ctx, sqlDB, kind, "")
		},
		Dial: func(context.Context, Target, func(string)) (Backend, error) { return h.mem, nil },
		now:  func() time.Time { return h.now }}
	return h
}

// backup registers a local backup and inserts its row (the uploads refer to it).
func (h *harness) backup(t *testing.T, dir, source string) project.BackupInfo {
	t.Helper()
	b := &store.Backup{ProjectID: h.project, Filename: dir, Kind: "full", Metadata: []byte(`{}`), CreatedAt: time.Now()}
	if err := h.st.Backups.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	h.proj.backups[b.ID] = project.OffsiteArchive{Slug: "shop", Dir: dir, Kind: "full", Source: source}
	h.proj.content[b.ID] = []byte("tar of " + dir)
	return project.BackupInfo{ID: b.ID, Dir: dir, Meta: project.BackupMeta{Source: source}}
}

func TestScheduledBackupsGoUpEncryptedAndRotate(t *testing.T) {
	h := newHarness(t, Target{Name: "b2", Type: TypeS3, Enabled: true, Auto: true, Keep: 2, Encrypt: true, Passphrase: "correct horse battery",
		Endpoint: "https://s3.example.com", Bucket: "backups", AccessKey: "a", SecretKey: "s"})
	ctx := context.Background()
	var ids []project.BackupInfo
	for i, dir := range []string{"20260920-020000-aaaaaaaa", "20260921-020000-bbbbbbbb", "20260922-020000-cccccccc"} {
		b := h.backup(t, dir, "scheduled")
		ids = append(ids, b)
		h.s.OnProjectBackup(h.project, b)
		h.s.Pass(ctx)
		if i == 0 {
			if keys := h.mem.keys(); len(keys) != 1 || keys[0] != "projects/shop/20260920-020000-aaaaaaaa.full.scheduled.tar.age" {
				t.Fatalf("first upload: %v", keys)
			}
			if bytes.Contains(h.mem.files[h.mem.keys()[0]], []byte("tar of")) {
				t.Fatal("the archive went up unencrypted")
			}
		}
	}
	// A manual one is not rotated away, and manual backups do not go up by themselves.
	manual := h.backup(t, "20260919-100000-dddddddd", "manual")
	h.s.OnProjectBackup(h.project, manual)
	h.s.Pass(ctx)
	if len(h.mem.keys()) != 2 {
		t.Fatalf("manual backups must wait for a request: %v", h.mem.keys())
	}
	if _, err := h.s.UploadProject(ctx, h.project, manual.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.s.Pass(ctx)
	want := []string{
		"projects/shop/20260919-100000-dddddddd.full.manual.tar.age",
		"projects/shop/20260921-020000-bbbbbbbb.full.scheduled.tar.age",
		"projects/shop/20260922-020000-cccccccc.full.scheduled.tar.age",
	}
	if got := h.mem.keys(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after rotation:\n got %v\nwant %v", got, want)
	}

	// Listing and fetching decrypt what went up.
	list, err := h.s.ListProject(ctx, h.s.Config.Targets()[0].ID, h.project)
	if err != nil || len(list) != 3 || list[0].ID != "20260922-020000-cccccccc" || !list[0].Encrypted || list[2].Source != "manual" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := h.s.FetchProject(ctx, h.s.Config.Targets()[0].ID, h.project, list[0].Key); err != nil {
		t.Fatal(err)
	}
	if string(h.proj.imported[0]) != "tar of 20260922-020000-cccccccc" {
		t.Fatalf("fetched %q", h.proj.imported[0])
	}
	if _, err := h.s.FetchProject(ctx, h.s.Config.Targets()[0].ID, h.project, "projects/other/../shop/x.tar"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("a foreign key must be refused: %v", err)
	}
	ups, _ := h.st.Offsite.ByBackups(ctx, store.OffsiteProject, []string{ids[2].ID})
	if len(ups) != 1 || ups[0].Status != store.OffsiteDone || ups[0].SizeBytes == 0 {
		t.Fatalf("upload record: %+v", ups)
	}
}

func TestFailedUploadsRetryAndNotify(t *testing.T) {
	h := newHarness(t, Target{Name: "box", Type: TypeWebDAV, Enabled: true, Auto: true, URL: "https://dav.example.com"})
	ctx := context.Background()
	b := h.backup(t, "20260920-020000-aaaaaaaa", "scheduled")
	h.mem.fail = errors.New("connection refused")
	h.s.OnProjectBackup(h.project, b)
	h.s.Pass(ctx)
	u, _ := h.st.Offsite.Find(ctx, h.s.Config.Targets()[0].ID, store.OffsiteProject, b.ID)
	if u.Status != store.OffsiteFailed || u.Attempts != 1 || !strings.Contains(u.Error, "connection refused") || u.NextAttemptAt.IsZero() {
		t.Fatalf("after a failure: %+v", u)
	}
	if len(h.notes.events) != 1 || h.notes.events[0].Kind != "backup.failed" {
		t.Fatalf("notifications: %+v", h.notes.events)
	}
	// Not before the retry delay; afterwards it goes through.
	h.mem.fail = nil
	h.s.Pass(ctx)
	if len(h.mem.keys()) != 0 {
		t.Fatal("retried too early")
	}
	h.now = h.now.Add(6 * time.Minute)
	h.s.Pass(ctx)
	if len(h.mem.keys()) != 1 {
		t.Fatalf("the retry did not happen: %v", h.mem.keys())
	}

	// A local backup that is gone fails for good.
	gone := h.backup(t, "20260921-020000-bbbbbbbb", "scheduled")
	delete(h.proj.backups, gone.ID)
	h.s.OnProjectBackup(h.project, gone)
	h.s.Pass(ctx)
	u, _ = h.st.Offsite.Find(ctx, h.s.Config.Targets()[0].ID, store.OffsiteProject, gone.ID)
	if u.Status != store.OffsiteFailed || !u.NextAttemptAt.IsZero() || !strings.Contains(u.Error, "no longer exists") {
		t.Fatalf("a missing backup must not be retried: %+v", u)
	}
}

func TestDailyInstanceBackup(t *testing.T) {
	h := newHarness(t, Target{Name: "sftp", Type: TypeSFTP, Enabled: true, Instance: true, InstanceHour: 4, InstanceKeep: 3, Host: "box.example.com", User: "u1", Password: "pw", Encrypt: true, Passphrase: "correct horse battery"})
	ctx := context.Background()
	h.s.Pass(ctx) // 03:30 – not yet
	if list, _ := h.s.Instance.List(); len(list) != 0 {
		t.Fatalf("taken before its hour: %v", list)
	}
	h.now = h.now.Add(time.Hour)
	h.s.Pass(ctx)
	h.s.Pass(ctx) // once a day
	list, _ := h.s.Instance.List()
	if len(list) != 1 || list[0].Kind != instance.KindScheduled {
		t.Fatalf("instance backups: %+v", list)
	}
	keys := h.mem.keys()
	if len(keys) != 1 || keys[0] != "instance/"+list[0].ID+".tar.gz.age" {
		t.Fatalf("uploaded: %v", keys)
	}

	// Disaster recovery: the copy comes back as an importable instance backup.
	target := h.s.Config.Targets()[0].ID
	remote, err := h.s.ListInstance(ctx, target)
	if err != nil || len(remote) != 1 || remote[0].Source != "scheduled" || !remote[0].Encrypted {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	info, err := h.s.FetchInstance(ctx, target, remote[0].Key)
	if err != nil || info.Kind != instance.KindUpload {
		t.Fatalf("fetch: %+v %v", info, err)
	}

	// The wrong passphrase is named as such.
	tg, _ := h.s.Config.Get(target)
	tg.Passphrase = "not the right passphrase"
	if err := h.s.Config.Save(tg); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.FetchInstance(ctx, target, remote[0].Key); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
}

func TestTargetConfig(t *testing.T) {
	cfg, err := OpenConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, tg := range map[string]Target{
		"no name":       {Type: TypeS3},
		"bad type":      {Name: "x", Type: "ftp"},
		"s3 endpoint":   {Name: "x", Type: TypeS3, Endpoint: "s3.example.com", Bucket: "bb", AccessKey: "a", SecretKey: "s"},
		"s3 no secret":  {Name: "x", Type: TypeS3, Endpoint: "https://s3.example.com", Bucket: "bbb", AccessKey: "a"},
		"sftp no auth":  {Name: "x", Type: TypeSFTP, Host: "box.example.com", User: "u"},
		"sftp bad key":  {Name: "x", Type: TypeSFTP, Host: "box.example.com", User: "u", PrivateKey: "nonsense"},
		"short phrase":  {Name: "x", Type: TypeWebDAV, URL: "https://dav.example.com", Encrypt: true, Passphrase: "short"},
		"prefix escape": {Name: "x", Type: TypeWebDAV, URL: "https://dav.example.com", Prefix: "a/../b"},
	} {
		if _, err := cfg.Prepare(tg); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	p, err := cfg.Prepare(Target{Name: "Box", Type: TypeSFTP, Host: "u1.your-storagebox.de", Port: 23, User: "u1", Password: "pw", Encrypt: true, Passphrase: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Prefix != "envoryx" || p.ID == "" {
		t.Fatalf("defaults: %+v", p)
	}
	if err := cfg.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := cfg.PinHostKey(p.ID, "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	// Empty secrets keep the stored ones; another host drops the pinned key.
	upd, err := cfg.Prepare(Target{ID: p.ID, Name: "Box", Type: TypeSFTP, Host: "u2.your-storagebox.de", Port: 23, User: "u1", Encrypt: true, HostKey: "SHA256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Password != "pw" || upd.Passphrase != "correct horse battery" || upd.HostKey != "" {
		t.Fatalf("merge: %+v", upd)
	}
	if pub := upd.Public(); pub.Password != "" || pub.Passphrase != "" {
		t.Fatal("Public must drop the secrets")
	}
	raw, _ := os.ReadFile(filepath.Join(cfg.dir, configFile))
	if st, _ := os.Stat(filepath.Join(cfg.dir, configFile)); st.Mode().Perm() != 0o600 || !strings.Contains(string(raw), "u1.your-storagebox.de") {
		t.Fatalf("file: %v %s", st.Mode(), raw)
	}
}

func TestCrypt(t *testing.T) {
	var buf bytes.Buffer
	w, err := seal(&buf, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "secret archive")
	_ = w.Close()
	if !strings.HasPrefix(buf.String(), "age-encryption.org/v1") {
		t.Fatalf("not an age file: %q", buf.String()[:20])
	}
	r, err := unseal(bytes.NewReader(buf.Bytes()), "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(r); string(b) != "secret archive" {
		t.Fatalf("round trip: %q", b)
	}
	if _, err := unseal(bytes.NewReader(buf.Bytes()), "wrong passphrase!"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
}
