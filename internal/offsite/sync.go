package offsite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Layout on a target, below its prefix:
//
//	projects/<slug>/<backup dir>.<kind>.<source>.tar[.age]   a project backup
//	instance/<instance backup id>.tar.gz[.age]              an instance backup
//
// Everything a list needs is in the name, so listing never downloads anything, and the
// source decides whether rotation may remove a copy (only "scheduled" ones rotate).

var (
	projectKeyRe  = regexp.MustCompile(`^(\d{8}-\d{6}-[0-9a-f]{8})\.([a-z]+)\.([a-z-]+)\.tar(\.age)?$`)
	instanceKeyRe = regexp.MustCompile(`^((manual|upload|pre-migrate|pre-restore|scheduled)-(\d{8}-\d{6})-[0-9a-f]{4})\.tar\.gz(\.age)?$`)
	slugRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	sourceRe      = regexp.MustCompile(`^[a-z-]{1,20}$`)
)

// maxAttempts is how often an upload is tried automatically; the retry delays grow.
const maxAttempts = 5

var retryDelays = []time.Duration{5 * time.Minute, 15 * time.Minute, 45 * time.Minute, 2 * time.Hour}

// RemoteBackup is a backup found on a target.
type RemoteBackup struct {
	Key       string    `json:"key"`
	ID        string    `json:"id"` // backup directory (project) or instance backup id
	CreatedAt time.Time `json:"createdAt"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source,omitempty"`
	SizeBytes int64     `json:"sizeBytes"`
	Encrypted bool      `json:"encrypted"`
}

// ProjectBackups is what the syncer needs from the project manager.
type ProjectBackups interface {
	OpenOffsiteArchive(ctx context.Context, projectID, backupID string) (project.OffsiteArchive, error)
	ImportBackupArchive(ctx context.Context, projectID string, r io.Reader) (project.BackupInfo, error)
	ProjectSlug(ctx context.Context, projectID string) (string, error)
}

// Syncer uploads queued backups, takes the daily instance backup for targets that want
// one, rotates old copies on the targets and fetches copies back.
type Syncer struct {
	Config   *Config
	Store    *store.Store
	Projects ProjectBackups
	Instance *instance.Store
	// CreateInstanceBackup takes an instance backup of the given kind.
	CreateInstanceBackup func(ctx context.Context, kind string) (instance.Info, error)
	Notify               notify.Sender
	Log                  *slog.Logger
	// Dial opens a target (Dial by default; tests use an in-memory backend).
	Dial Dialer

	now  func() time.Time
	wake chan struct{}
	pass sync.Mutex
}

func (s *Syncer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Syncer) dial(ctx context.Context, t Target) (Backend, error) {
	d := s.Dial
	if d == nil {
		d = Dial
	}
	return d(ctx, t, func(fp string) {
		if err := s.Config.PinHostKey(t.ID, fp); err != nil {
			s.Log.Warn("offsite: could not record the SFTP host key", "target", t.Name, "err", err)
		}
	})
}

func (s *Syncer) poke() {
	if s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// OnProjectBackup queues a scheduled project backup for every automatic target.
func (s *Syncer) OnProjectBackup(projectID string, b project.BackupInfo) {
	if b.Meta.Source != "scheduled" {
		return
	}
	ctx := context.Background()
	queued := false
	for _, t := range s.Config.Targets() {
		if !t.Enabled || !t.Auto {
			continue
		}
		if err := s.Store.Offsite.EnqueueIfAbsent(ctx, store.OffsiteUpload{TargetID: t.ID, Scope: store.OffsiteProject, ProjectID: projectID, BackupID: b.ID, Source: "scheduled"}); err != nil {
			s.Log.Warn("offsite: queue a scheduled backup", "target", t.Name, "err", err)
			continue
		}
		queued = true
	}
	if queued {
		s.poke()
	}
}

// targetsFor resolves the targets of a manual upload: the given ones, or every enabled
// one when none is given.
func (s *Syncer) targetsFor(ids []string) ([]Target, error) {
	var out []Target
	if len(ids) == 0 {
		for _, t := range s.Config.Targets() {
			if t.Enabled {
				out = append(out, t)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%w: no offsite target is enabled – add one under Settings → Offsite backups", validate.ErrInvalid)
		}
		return out, nil
	}
	for _, id := range ids {
		t, err := s.Config.Get(id)
		if err != nil {
			return nil, err
		}
		if !t.Enabled {
			return nil, fmt.Errorf("%w: the target %s is switched off", validate.ErrInvalid, t.Name)
		}
		out = append(out, t)
	}
	return out, nil
}

// UploadProject queues a project backup for the targets (all enabled ones when empty).
// A failed upload starts over.
func (s *Syncer) UploadProject(ctx context.Context, projectID, backupID string, targetIDs []string) ([]store.OffsiteUpload, error) {
	if _, err := s.Store.Backups.Get(ctx, projectID, backupID); err != nil {
		return nil, err
	}
	return s.upload(ctx, store.OffsiteProject, projectID, backupID, targetIDs)
}

// UploadInstance queues an instance backup.
func (s *Syncer) UploadInstance(ctx context.Context, backupID string, targetIDs []string) ([]store.OffsiteUpload, error) {
	if _, err := s.Instance.Get(backupID); err != nil {
		return nil, err
	}
	return s.upload(ctx, store.OffsiteInstance, "", backupID, targetIDs)
}

func (s *Syncer) upload(ctx context.Context, scope, projectID, backupID string, targetIDs []string) ([]store.OffsiteUpload, error) {
	targets, err := s.targetsFor(targetIDs)
	if err != nil {
		return nil, err
	}
	var out []store.OffsiteUpload
	for _, t := range targets {
		u, err := s.Store.Offsite.Enqueue(ctx, store.OffsiteUpload{TargetID: t.ID, Scope: scope, ProjectID: projectID, BackupID: backupID, Source: "manual"})
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	s.poke()
	return out, nil
}

// Run processes the queue until ctx ends: every interval, and right away when something
// is queued.
func (s *Syncer) Run(ctx context.Context, interval time.Duration) {
	s.wake = make(chan struct{}, 1)
	if err := s.Store.Offsite.ResetRunning(ctx); err != nil {
		s.Log.Warn("offsite: reset interrupted uploads", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// Pass takes a due instance backup and works through the queue once.
func (s *Syncer) Pass(ctx context.Context) {
	s.pass.Lock()
	defer s.pass.Unlock()
	s.scheduleInstance(ctx)
	for {
		due, err := s.Store.Offsite.Due(ctx, s.clock(), maxAttempts)
		if err != nil {
			s.Log.Warn("offsite: read the queue", "err", err)
			return
		}
		if len(due) == 0 || ctx.Err() != nil {
			return
		}
		for _, u := range due {
			if ctx.Err() != nil {
				return
			}
			s.process(ctx, u)
		}
	}
}

// scheduleInstance takes today's instance backup once a target's hour has come and
// queues it for every target whose hour has come.
func (s *Syncer) scheduleInstance(ctx context.Context) {
	now := s.clock()
	var due []Target
	for _, t := range s.Config.Targets() {
		if t.Enabled && t.Instance && now.Hour() >= t.InstanceHour {
			due = append(due, t)
		}
	}
	if len(due) == 0 || s.Instance == nil || s.CreateInstanceBackup == nil {
		return
	}
	var today *instance.Info
	list, err := s.Instance.List()
	if err != nil {
		s.Log.Warn("offsite: list instance backups", "err", err)
		return
	}
	y, m, d := now.Date()
	for i, b := range list {
		by, bm, bd := b.CreatedAt.In(now.Location()).Date()
		if b.Kind == instance.KindScheduled && by == y && bm == m && bd == d {
			today = &list[i]
			break
		}
	}
	if today == nil {
		info, err := s.CreateInstanceBackup(ctx, instance.KindScheduled)
		if err != nil {
			s.Log.Warn("offsite: daily instance backup failed", "err", err)
			s.notifyFailure(ctx, "instance", "the daily instance backup could not be taken: "+err.Error())
			return
		}
		s.Log.Info("offsite: daily instance backup taken", "backup", info.ID)
		today = &info
	}
	for _, t := range due {
		if err := s.Store.Offsite.EnqueueIfAbsent(ctx, store.OffsiteUpload{TargetID: t.ID, Scope: store.OffsiteInstance, BackupID: today.ID, Source: "scheduled"}); err != nil {
			s.Log.Warn("offsite: queue the instance backup", "target", t.Name, "err", err)
		}
	}
}

// process uploads one queued backup and records the outcome.
func (s *Syncer) process(ctx context.Context, u store.OffsiteUpload) {
	t, err := s.Config.Get(u.TargetID)
	if err != nil || !t.Enabled {
		// The target went away or was switched off: nothing to do until it is back.
		_ = s.Store.Offsite.MarkFailed(ctx, u.ID, "the target is removed or switched off", time.Time{})
		return
	}
	if err := s.Store.Offsite.MarkRunning(ctx, u.ID); err != nil {
		s.Log.Warn("offsite: claim an upload", "err", err)
		return
	}
	key, size, err := s.send(ctx, t, u)
	if err != nil {
		if ctx.Err() != nil {
			// Shutting down: the next start picks it up again.
			_ = s.Store.Offsite.ResetRunning(context.WithoutCancel(ctx))
			return
		}
		next := time.Time{}
		if u.Attempts < len(retryDelays) && !errors.Is(err, errPermanent) {
			next = s.clock().Add(retryDelays[u.Attempts])
		}
		msg := strings.TrimPrefix(err.Error(), errPermanent.Error()+": ")
		_ = s.Store.Offsite.MarkFailed(ctx, u.ID, msg, next)
		s.Log.Warn("offsite: upload failed", "target", t.Name, "scope", u.Scope, "backup", u.BackupID, "attempt", u.Attempts+1, "err", msg)
		s.notifyFailure(ctx, t.ID, fmt.Sprintf("Copying a backup to %s failed: %s", t.Name, msg))
		return
	}
	if err := s.Store.Offsite.MarkDone(ctx, u.ID, key, size); err != nil {
		s.Log.Warn("offsite: record an upload", "err", err)
	}
	s.Log.Info("offsite: backup copied", "target", t.Name, "key", key, "bytes", size)
	if s.Notify != nil {
		s.Notify.Clear("offsite:" + t.ID)
	}
	s.rotate(ctx, t, u)
}

// errPermanent marks failures a retry will not fix (the local backup is gone).
var errPermanent = errors.New("permanent")

func (s *Syncer) send(ctx context.Context, t Target, u store.OffsiteUpload) (string, int64, error) {
	var (
		body io.ReadCloser
		key  string
	)
	switch u.Scope {
	case store.OffsiteProject:
		a, err := s.Projects.OpenOffsiteArchive(ctx, u.ProjectID, u.BackupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, project.ErrNotFound) {
				return "", 0, fmt.Errorf("%w: the local backup no longer exists", errPermanent)
			}
			return "", 0, err
		}
		source := a.Source
		if !sourceRe.MatchString(source) {
			source = "manual"
		}
		body, key = a.Body, "projects/"+a.Slug+"/"+a.Dir+"."+a.Kind+"."+source+".tar"
	case store.OffsiteInstance:
		rc, _, _, err := s.Instance.Open(u.BackupID)
		if err != nil {
			if errors.Is(err, instance.ErrNotFound) {
				return "", 0, fmt.Errorf("%w: the local instance backup no longer exists", errPermanent)
			}
			return "", 0, err
		}
		body, key = rc, "instance/"+u.BackupID+".tar.gz"
	default:
		return "", 0, fmt.Errorf("%w: unknown scope %q", errPermanent, u.Scope)
	}
	defer body.Close()

	ctx, cancel := context.WithTimeout(ctx, 12*time.Hour)
	defer cancel()
	b, err := s.dial(ctx, t)
	if err != nil {
		return "", 0, err
	}
	defer b.Close()

	var r io.Reader = body
	if t.Encrypt {
		key += encryptedSuffix
		pr, pw := io.Pipe()
		go func() {
			w, err := seal(pw, t.Passphrase)
			if err == nil {
				_, err = io.Copy(w, body)
				if cerr := w.Close(); err == nil {
					err = cerr
				}
			}
			pw.CloseWithError(err)
		}()
		defer pr.Close()
		r = pr
	}
	n, err := b.Put(ctx, key, r)
	if err != nil {
		return "", 0, err
	}
	return key, n, nil
}

// rotate removes scheduled copies beyond the target's keep count after a scheduled upload.
func (s *Syncer) rotate(ctx context.Context, t Target, u store.OffsiteUpload) {
	keep, dir := t.Keep, ""
	switch u.Scope {
	case store.OffsiteProject:
		slug, err := s.Projects.ProjectSlug(ctx, u.ProjectID)
		if err != nil {
			return
		}
		dir = "projects/" + slug
	case store.OffsiteInstance:
		keep, dir = t.InstanceKeep, "instance"
	}
	if keep <= 0 {
		return
	}
	b, err := s.dial(ctx, t)
	if err != nil {
		s.Log.Warn("offsite: rotation skipped", "target", t.Name, "err", err)
		return
	}
	defer b.Close()
	list, err := s.list(ctx, b, dir)
	if err != nil {
		s.Log.Warn("offsite: rotation skipped", "target", t.Name, "err", err)
		return
	}
	n := 0
	for _, rb := range list {
		if rb.Source != "scheduled" {
			continue
		}
		n++
		if n <= keep {
			continue
		}
		if err := b.Delete(ctx, rb.Key); err != nil {
			s.Log.Warn("offsite: remove an old copy", "target", t.Name, "key", rb.Key, "err", err)
			continue
		}
		_ = s.Store.Offsite.DeleteByKey(ctx, t.ID, rb.Key)
		s.Log.Info("offsite: old copy removed", "target", t.Name, "key", rb.Key)
	}
}

func (s *Syncer) notifyFailure(ctx context.Context, key, message string) {
	if s.Notify == nil {
		return
	}
	s.Notify.Notify(ctx, notify.Event{Kind: "backup.failed", Level: notify.Error, Title: "Offsite backup failed", Message: message, Key: "offsite:" + key})
}

// list parses a directory of a target, newest first.
func (s *Syncer) list(ctx context.Context, b Backend, dir string) ([]RemoteBackup, error) {
	objs, err := b.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	var out []RemoteBackup
	for _, o := range objs {
		name := o.Key[strings.LastIndex(o.Key, "/")+1:]
		if m := projectKeyRe.FindStringSubmatch(name); m != nil && dir != "instance" {
			created, _ := time.Parse("20060102-150405", m[1][:15])
			out = append(out, RemoteBackup{Key: o.Key, ID: m[1], CreatedAt: created, Kind: m[2], Source: m[3], SizeBytes: o.Size, Encrypted: m[4] != ""})
		} else if m := instanceKeyRe.FindStringSubmatch(name); m != nil && dir == "instance" {
			created, _ := time.Parse("20060102-150405", m[3])
			source := ""
			if m[2] == instance.KindScheduled {
				source = "scheduled"
			}
			out = append(out, RemoteBackup{Key: o.Key, ID: m[1], CreatedAt: created, Kind: m[2], Source: source, SizeBytes: o.Size, Encrypted: m[4] != ""})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// ListProject lists the copies of a project's backups on a target.
func (s *Syncer) ListProject(ctx context.Context, targetID, projectID string) ([]RemoteBackup, error) {
	slug, err := s.Projects.ProjectSlug(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.listDir(ctx, targetID, "projects/"+slug)
}

// ListInstance lists the instance backups on a target.
func (s *Syncer) ListInstance(ctx context.Context, targetID string) ([]RemoteBackup, error) {
	return s.listDir(ctx, targetID, "instance")
}

func (s *Syncer) listDir(ctx context.Context, targetID, dir string) ([]RemoteBackup, error) {
	t, err := s.Config.Get(targetID)
	if err != nil {
		return nil, err
	}
	b, err := s.dial(ctx, t)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	out, err := s.list(ctx, b, dir)
	if out == nil {
		out = []RemoteBackup{}
	}
	return out, err
}

// open fetches a copy and decrypts it when needed.
func (s *Syncer) open(ctx context.Context, t Target, key string) (io.Reader, func(), error) {
	b, err := s.dial(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	rc, err := b.Get(ctx, key)
	if err != nil {
		b.Close()
		return nil, nil, err
	}
	done := func() { rc.Close(); b.Close() }
	var r io.Reader = rc
	if strings.HasSuffix(key, encryptedSuffix) {
		r, err = unseal(rc, t.Passphrase)
		if err != nil {
			done()
			return nil, nil, err
		}
	}
	return r, done, nil
}

// checkKey accepts only names this package writes, below the expected directory.
func checkKey(key, dir string, re *regexp.Regexp) (string, error) {
	name, ok := strings.CutPrefix(key, dir+"/")
	if !ok || !re.MatchString(name) {
		return "", fmt.Errorf("%w: %q is not a backup on this target", validate.ErrInvalid, key)
	}
	return name, nil
}

// FetchProject copies a project backup from a target into the local backups.
func (s *Syncer) FetchProject(ctx context.Context, targetID, projectID, key string) (project.BackupInfo, error) {
	slug, err := s.Projects.ProjectSlug(ctx, projectID)
	if err != nil {
		return project.BackupInfo{}, err
	}
	name, err := checkKey(key, "projects/"+slug, projectKeyRe)
	if err != nil {
		return project.BackupInfo{}, err
	}
	t, err := s.Config.Get(targetID)
	if err != nil {
		return project.BackupInfo{}, err
	}
	r, done, err := s.open(ctx, t, key)
	if err != nil {
		return project.BackupInfo{}, err
	}
	defer done()
	info, err := s.Projects.ImportBackupArchive(ctx, projectID, r)
	if err != nil {
		return project.BackupInfo{}, err
	}
	m := projectKeyRe.FindStringSubmatch(name)
	_ = s.Store.Offsite.Record(ctx, store.OffsiteUpload{TargetID: t.ID, Scope: store.OffsiteProject, ProjectID: projectID, BackupID: info.ID, Source: m[3], RemoteKey: key})
	return info, nil
}

// FetchInstance copies an instance backup from a target into the local instance
// backups, where it can be restored like an uploaded one.
func (s *Syncer) FetchInstance(ctx context.Context, targetID, key string) (instance.Info, error) {
	if _, err := checkKey(key, "instance", instanceKeyRe); err != nil {
		return instance.Info{}, err
	}
	t, err := s.Config.Get(targetID)
	if err != nil {
		return instance.Info{}, err
	}
	r, done, err := s.open(ctx, t, key)
	if err != nil {
		return instance.Info{}, err
	}
	defer done()
	info, err := s.Instance.Import(r)
	if err != nil {
		return instance.Info{}, err
	}
	_ = s.Store.Offsite.Record(ctx, store.OffsiteUpload{TargetID: t.ID, Scope: store.OffsiteInstance, BackupID: info.ID, Source: "manual", RemoteKey: key})
	return info, nil
}

// DeleteRemote removes a copy from a target.
func (s *Syncer) DeleteRemote(ctx context.Context, targetID, key string) error {
	if _, err := checkKey(key, "instance", instanceKeyRe); err != nil {
		dir := key[:max(strings.LastIndex(key, "/"), 0)]
		slug := strings.TrimPrefix(dir, "projects/")
		if !strings.HasPrefix(dir, "projects/") || !slugRe.MatchString(slug) {
			return err
		}
		if _, err := checkKey(key, dir, projectKeyRe); err != nil {
			return err
		}
	}
	t, err := s.Config.Get(targetID)
	if err != nil {
		return err
	}
	b, err := s.dial(ctx, t)
	if err != nil {
		return err
	}
	defer b.Close()
	if err := b.Delete(ctx, key); err != nil {
		return err
	}
	return s.Store.Offsite.DeleteByKey(ctx, t.ID, key)
}

// TestResult is what a connection test found.
type TestResult struct {
	// HostKey is the SFTP server's key fingerprint (pinned on the first save).
	HostKey string `json:"hostKey,omitempty"`
}

// Test writes, lists, reads and deletes a small file on a (possibly unsaved) target.
func (s *Syncer) Test(ctx context.Context, t Target) (TestResult, error) {
	var res TestResult
	d := s.Dial
	if d == nil {
		d = Dial
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	b, err := d(ctx, t, func(fp string) { res.HostKey = fp })
	if err != nil {
		return res, err
	}
	defer b.Close()
	if res.HostKey == "" {
		res.HostKey = t.HostKey
	}
	const key = "test/envoryx-write-test.txt"
	payload := "Envoryx offsite test " + s.clock().UTC().Format(time.RFC3339)
	var r io.Reader = strings.NewReader(payload)
	if t.Encrypt {
		pr, pw := io.Pipe()
		go func() {
			w, err := seal(pw, t.Passphrase)
			if err == nil {
				_, err = io.WriteString(w, payload)
				if cerr := w.Close(); err == nil {
					err = cerr
				}
			}
			pw.CloseWithError(err)
		}()
		r = pr
	}
	if _, err := b.Put(ctx, key, r); err != nil {
		return res, fmt.Errorf("write: %w", err)
	}
	if _, err := b.List(ctx, "test"); err != nil {
		return res, fmt.Errorf("list: %w", err)
	}
	rc, err := b.Get(ctx, key)
	if err != nil {
		return res, fmt.Errorf("read: %w", err)
	}
	var got io.Reader = rc
	if t.Encrypt {
		if got, err = unseal(rc, t.Passphrase); err != nil {
			rc.Close()
			return res, fmt.Errorf("read: %w", err)
		}
	}
	data, err := io.ReadAll(io.LimitReader(got, 4096))
	rc.Close()
	if err != nil || string(data) != payload {
		return res, fmt.Errorf("read: the file came back different")
	}
	if err := b.Delete(ctx, key); err != nil {
		return res, fmt.Errorf("delete: %w", err)
	}
	return res, nil
}
