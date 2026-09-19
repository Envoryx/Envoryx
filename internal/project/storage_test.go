package project

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/s3"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// fakeProvisioner records bucket provisioning calls instead of talking S3.
type fakeProvisioner struct {
	mu    sync.Mutex
	calls []string
	fail  error
}

func (f *fakeProvisioner) EnsureBucket(_ context.Context, endpoint, accessKey, secretKey, bucket string, publicRead bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	public := "private"
	if publicRead {
		public = "public"
	}
	f.calls = append(f.calls, endpoint+" "+accessKey+":"+secretKey+" "+bucket+" "+public)
	return nil
}

func (f *fakeProvisioner) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func envOf(t *testing.T, e *env, container string) map[string]string {
	t.Helper()
	c, ok := e.engine.Container(container)
	if !ok {
		t.Fatalf("container %s missing", container)
	}
	out := map[string]string{}
	for _, kv := range c.Spec.Env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func TestObjectStorageLifecycle(t *testing.T) {
	e := newEnv(t)
	prov := &fakeProvisioner{}
	e.m.SetProvisioner(prov)
	e.m.SetLinks(func(context.Context) (string, int, int) { return "nas.lan", 80, 443 })
	ctx := context.Background()

	req := phpRequest("Shop", true)
	req.Storage = &StorageRequest{}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	// The service exists with generated credentials, a bucket named after the project
	// and two published ports (S3 API + console).
	svc, cfg, err := storageConfig(view.Project)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Image != "rustfs/rustfs:1.0.0" || cfg.Bucket != "shop" || !cfg.PublicRead || !strings.HasPrefix(cfg.AccessKey, "envoryx") || len(cfg.SecretKey) != 40 {
		t.Fatalf("storage config: %+v image=%s", cfg, svc.Image)
	}
	if cfg.HostPort == 0 || cfg.ConsolePort == 0 || cfg.HostPort == cfg.ConsolePort || cfg.HostPort == view.Project.HTTPPort {
		t.Fatalf("ports: %+v http=%d", cfg, view.Project.HTTPPort)
	}
	c, ok := e.engine.Container("envoryx-shop-storage")
	if !ok || c.State != "running" || len(c.Spec.Ports) != 2 || c.Spec.Mounts[0].Source != "envoryx-shop-storage" {
		t.Fatalf("storage container: %+v", c)
	}
	if !strings.Contains(strings.Join(c.Spec.Env, ","), "RUSTFS_ACCESS_KEY="+cfg.AccessKey) {
		t.Fatalf("container env: %v", c.Spec.Env)
	}

	// The bucket was provisioned via the published port (bare metal) with public read.
	if got := prov.last(); got != "http://127.0.0.1:"+itoa(cfg.HostPort)+" "+cfg.AccessKey+":"+cfg.SecretKey+" shop public" {
		t.Fatalf("provisioning call: %q", got)
	}

	// Application containers get the S3_* and AWS_* variables; the public URL goes
	// through the proxy host name with TLS.
	env := envOf(t, e, "envoryx-shop-php")
	want := map[string]string{
		"S3_ENDPOINT": "http://s3:9000", "S3_BUCKET": "shop", "S3_ACCESS_KEY": cfg.AccessKey, "S3_SECRET_KEY": cfg.SecretKey, "S3_USE_PATH_STYLE": "true", "S3_REGION": "us-east-1",
		"AWS_ACCESS_KEY_ID": cfg.AccessKey, "AWS_SECRET_ACCESS_KEY": cfg.SecretKey, "AWS_BUCKET": "shop", "AWS_ENDPOINT": "http://s3:9000", "AWS_USE_PATH_STYLE_ENDPOINT": "true", "AWS_DEFAULT_REGION": "us-east-1",
		"AWS_URL": "https://shop-s3.test/shop", "S3_PUBLIC_URL": "https://shop-s3.test/shop", "S3_PUBLIC_ENDPOINT": "https://shop-s3.test",
	}
	for k, v := range want {
		if env[k] != v {
			t.Fatalf("env %s = %q, want %q", k, env[k], v)
		}
	}

	// Info hides the secret unless asked; both list the injected variables.
	info, err := e.m.StorageInfo(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.SecretKey != "" || info.AccessKey != "" || info.Bucket != "shop" || info.Hostname != "shop-s3.test" || info.PublicURL != "https://shop-s3.test/shop" || info.State != "running" || len(info.InjectedEnv) != len(runtime.StorageEnvKeys) {
		t.Fatalf("info: %+v", info)
	}
	if info, _ := e.m.StorageInfo(ctx, id, true); info.SecretKey != cfg.SecretKey {
		t.Fatalf("info with secrets: %+v", info)
	}

	// The proxy routes the S3 host name to the storage container; the name is reserved.
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := table.Routes["shop-s3.test"]; !ok || !r.Running || r.Dial != "127.0.0.1:"+itoa(cfg.HostPort) {
		t.Fatalf("route: %+v (ok=%v)", r, ok)
	}
	if _, err := e.m.AddDomain(ctx, id, "shop-s3.test"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reserved host name accepted: %v", err)
	}

	// Public read can be switched off; a running server is reconfigured right away.
	info, err = e.m.SetStoragePublicRead(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.PublicRead || !strings.HasSuffix(prov.last(), " shop private") {
		t.Fatalf("public read off: %+v last=%q", info, prov.last())
	}

	// Disabling needs the data confirmation and then removes container and volume.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Storage: &StorageUpdate{Enabled: false}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("disable without confirmation = %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Storage: &StorageUpdate{Enabled: false, RemoveData: true}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-shop-storage"); ok {
		t.Fatal("storage container must be gone")
	}
	vols, _ := e.engine.ListVolumes(ctx, true)
	for _, v := range vols {
		if v.Name == "envoryx-shop-storage" {
			t.Fatal("storage volume must be gone")
		}
	}
	if _, err := e.m.StorageInfo(ctx, id, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("info after removal = %v", err)
	}
	if env := envOf(t, e, "envoryx-shop-php"); env["AWS_BUCKET"] != "" {
		t.Fatal("app env must lose the storage variables")
	}

	// Adding it again to an existing project provisions fresh credentials and a bucket.
	private := false
	if _, err := e.m.Update(ctx, id, UpdateRequest{Storage: &StorageUpdate{Enabled: true, PublicRead: &private}}); err != nil {
		t.Fatal(err)
	}
	view, _ = e.m.Get(ctx, id)
	_, cfg2, err := storageConfig(view.Project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.AccessKey == cfg.AccessKey || cfg2.PublicRead || cfg2.HostPort == 0 || cfg2.ConsolePort == 0 {
		t.Fatalf("re-added storage: %+v", cfg2)
	}
	if !strings.HasSuffix(prov.last(), " shop private") {
		t.Fatalf("re-added bucket not provisioned: %q", prov.last())
	}
	if env := envOf(t, e, "envoryx-shop-php"); env["AWS_ACCESS_KEY_ID"] != cfg2.AccessKey {
		t.Fatalf("app env not updated: %q", env["AWS_ACCESS_KEY_ID"])
	}
}

func TestObjectStorageProvisioningFailureFailsStart(t *testing.T) {
	e := newEnv(t)
	prov := &fakeProvisioner{fail: errors.New("s3: HTTP 403: SignatureDoesNotMatch")}
	e.m.SetProvisioner(prov)
	ctx := context.Background()
	req := phpRequest("Shop", true)
	req.Storage = &StorageRequest{}
	_, err := e.m.Create(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "bucket shop") || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("create with failing provisioning = %v", err)
	}
	// Create rolled back: nothing left behind.
	if n, _ := e.store.Projects.Count(ctx); n != 0 {
		t.Fatal("failed create must not leave a project")
	}
}

func TestStorageBucketNameAndReservedEnv(t *testing.T) {
	for in, want := range map[string]string{"shop": "shop", "ab": "ab-bucket", strings.Repeat("a", 70): strings.Repeat("a", 63)} {
		if got := runtime.StorageBucketName(in); got != want {
			t.Errorf("bucket name for %q = %q, want %q", in, got, want)
		}
	}
	e := newEnv(t)
	req := phpRequest("Shop", false)
	req.Env = []EnvVarRequest{{Key: "RUSTFS_ACCESS_KEY", Value: "x"}}
	if _, err := e.m.Create(context.Background(), req); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("RUSTFS_ prefix must be reserved: %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// memStore is an in-memory ObjectStore keyed by bucket/key.
type memStore struct {
	mu      sync.Mutex
	objects map[string]memObject
	puts    int
}

type memObject struct {
	data  []byte
	ctype string
}

func newMemStore() *memStore { return &memStore{objects: map[string]memObject{}} }

func (s *memStore) ListObjects(_ context.Context, bucket string) ([]s3.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []s3.Object
	for k, o := range s.objects {
		if b, key, _ := strings.Cut(k, "/"); b == bucket {
			out = append(out, s3.Object{Key: key, Size: int64(len(o.data))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *memStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[bucket+"/"+key]
	if !ok {
		return nil, "", &s3.Error{Status: 404, Body: "NoSuchKey"}
	}
	return io.NopCloser(bytes.NewReader(o.data)), o.ctype, nil
}

func (s *memStore) PutObject(_ context.Context, bucket, key string, body io.Reader, size int64, ctype string) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("size mismatch: %d vs %d", len(data), size)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = memObject{data: data, ctype: ctype}
	s.puts++
	return nil
}

func (s *memStore) DeleteObjects(_ context.Context, bucket string, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.objects, bucket+"/"+k)
	}
	return nil
}

func (s *memStore) get(bucket, key string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[bucket+"/"+key]
	return string(o.data), o.ctype, ok
}

func TestBackupIncludesObjectStorage(t *testing.T) {
	e := newEnv(t)
	e.m.SetProvisioner(&fakeProvisioner{})
	mem := newMemStore()
	e.m.SetObjectStoreFactory(func(string, string, string) s3.ObjectStore { return mem })
	ctx := context.Background()
	req := phpRequest("Shop", true)
	req.Storage = &StorageRequest{}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("v1"), 0o644)
	big := bytes.Repeat([]byte("0123456789"), 300000) // 3 MB, crosses buffer boundaries
	_ = mem.PutObject(ctx, "shop", "img/logo.png", bytes.NewReader([]byte("PNG")), 3, "image/png")
	_ = mem.PutObject(ctx, "shop", "docs/a b/report.pdf", bytes.NewReader([]byte("%PDF")), 4, "application/pdf")
	_ = mem.PutObject(ctx, "shop", "big.bin", bytes.NewReader(big), int64(len(big)), "")

	// Files + storage: kind is "full", metadata counts the objects, the archive lists
	// them as plain files with their content types.
	info, err := e.m.CreateBackup(ctx, id, BackupOptions{Files: true, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "full" || info.Meta.Storage == nil || info.Meta.Storage.Objects != 3 || info.Meta.Storage.Bucket != "shop" || info.Meta.Storage.Bytes != int64(3+4+len(big)) {
		t.Fatalf("backup info: kind=%s storage=%+v", info.Kind, info.Meta.Storage)
	}
	archive := filepath.Join(e.cfgDir, "backups", "shop", info.Dir, backupStorageFile)
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(f)
	tr := tar.NewReader(gz)
	types := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		types[hdr.Name] = hdr.PAXRecords[paxContentType]
	}
	f.Close()
	if types["img/logo.png"] != "image/png" || types["docs/a b/report.pdf"] != "application/pdf" || len(types) != 3 {
		t.Fatalf("archive entries: %v", types)
	}

	// Storage alone is its own kind; a project without storage refuses it but a mixed
	// request just drops it.
	if only, err := e.m.CreateBackup(ctx, id, BackupOptions{Storage: true}); err != nil || only.Kind != "storage" {
		t.Fatalf("storage-only backup: %+v %v", only, err)
	}
	plain, err := e.m.Create(ctx, phpRequest("Plain", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateBackup(ctx, plain.Project.ID, BackupOptions{Storage: true}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("storage backup without storage = %v", err)
	}
	if b, err := e.m.CreateBackup(ctx, plain.Project.ID, BackupOptions{Files: true, Storage: true}); err != nil || b.Kind != "files" || b.Meta.Storage != nil {
		t.Fatalf("mixed request on a project without storage: %+v %v", b, err)
	}

	// Restore without wipe overwrites/adds; with wipe the extra object disappears.
	_ = mem.PutObject(ctx, "shop", "img/logo.png", bytes.NewReader([]byte("CHANGED")), 7, "image/png")
	_ = mem.PutObject(ctx, "shop", "extra.txt", bytes.NewReader([]byte("x")), 1, "text/plain")
	if _, err := e.m.RestoreBackup(ctx, id, info.ID, RestoreOptions{Storage: true, Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	if data, ctype, _ := mem.get("shop", "img/logo.png"); data != "PNG" || ctype != "image/png" {
		t.Fatalf("restored logo: %q %q", data, ctype)
	}
	if data, _, _ := mem.get("shop", "big.bin"); !bytes.Equal([]byte(data), big) {
		t.Fatal("restored big object differs")
	}
	if _, ctype, _ := mem.get("shop", "docs/a b/report.pdf"); ctype != "application/pdf" {
		t.Fatalf("content type lost: %q", ctype)
	}
	if _, _, ok := mem.get("shop", "extra.txt"); !ok {
		t.Fatal("restore without wipe must keep other objects")
	}
	if _, err := e.m.RestoreBackup(ctx, id, info.ID, RestoreOptions{Storage: true, WipeStorage: true, Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := mem.get("shop", "extra.txt"); ok {
		t.Fatal("wipe must remove objects that are not in the backup")
	}
	// A backup without storage cannot restore it; a stopped storage container refuses.
	filesOnly, _ := e.m.CreateBackup(ctx, id, BackupOptions{Files: true})
	if _, err := e.m.RestoreBackup(ctx, id, filesOnly.ID, RestoreOptions{Storage: true, Confirm: "shop"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("restore storage from files-only backup = %v", err)
	}
	e.engine.SetState("envoryx-shop-storage", "exited")
	if _, err := e.m.CreateBackup(ctx, id, BackupOptions{Storage: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("backup with stopped storage = %v", err)
	}
}

func TestValidObjectKey(t *testing.T) {
	for k, want := range map[string]bool{"a/b.txt": true, "with space/x": true, "": false, "/abs": false, "a/../b": false, "..": false, "ok/..x": true} {
		if got := validObjectKey(k); got != want {
			t.Errorf("validObjectKey(%q) = %v", k, got)
		}
	}
}
