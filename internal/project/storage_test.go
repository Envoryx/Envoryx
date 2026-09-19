package project

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
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
