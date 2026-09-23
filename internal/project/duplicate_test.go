package project

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// allParts is a duplicate request that takes everything the original has.
func allParts(name string) DuplicateRequest {
	return DuplicateRequest{Name: name, Files: true, Database: true, Storage: true, Workers: true, Git: true}
}

func TestDuplicateProjectCopiesConfigFilesAndDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var dumped, restored []string
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		switch {
		case strings.HasSuffix(cmd[0], "-dump"), cmd[0] == "mysqldump":
			dumped = append(dumped, container)
			return "CREATE TABLE orders (id int);\n", 0, nil
		case strings.HasSuffix(container, "-database"):
			restored = append(restored, container+": "+string(stdin))
		}
		return "", 0, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	src := view.Project
	if _, err := e.m.AddWorker(ctx, src.ID, WorkerRequest{Name: "queue", Preset: "laravel:queue", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.projDir, "shop")
	_ = os.WriteFile(filepath.Join(dir, "public", "index.php"), []byte("<?php echo 'shop';"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "vendor", "lib"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "vendor", "lib", "x.php"), []byte("dep"), 0o644)
	_ = os.Symlink("public", filepath.Join(dir, "www"))

	copyView, err := e.m.Duplicate(ctx, src.ID, allParts("Shop Test"))
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	cp := copyView.Project
	if cp.Slug != "shop-test" || cp.Path != "shop-test" || cp.ID == src.ID || cp.Lifecycle != store.LifecycleReady {
		t.Fatalf("copy: %+v", cp)
	}
	if cp.HTTPPort == src.HTTPPort || cp.HTTPPort == 0 {
		t.Fatalf("the copy needs a port of its own: %d vs %d", cp.HTTPPort, src.HTTPPort)
	}
	if cp.Docroot != src.Docroot || len(cp.Env) != len(src.Env) || cp.Env[0].Key != "APP_ENV" {
		t.Fatalf("configuration not copied: %+v", cp)
	}
	// The copy is stopped unless asked otherwise, and its containers exist.
	if cp.DesiredState != store.DesiredStopped {
		t.Fatalf("desired state: %s", cp.DesiredState)
	}
	for _, name := range []string{"envoryx-shop-test-web", "envoryx-shop-test-php", "envoryx-shop-test-database", "envoryx-shop-test-worker-queue"} {
		if _, ok := e.engine.Container(name); !ok {
			t.Fatalf("container %s missing, have %v", name, e.engine.ContainerNames())
		}
	}
	// Credentials are copied verbatim: a checked-in .env of the original keeps working.
	srcCreds, _ := e.m.DatabaseCredentials(ctx, src.ID)
	cpCreds, err := e.m.DatabaseCredentials(ctx, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cpCreds.Password != srcCreds.Password || cpCreds.Database != srcCreds.Database || cpCreds.Username != srcCreds.Username {
		t.Fatalf("credentials must be copied: %+v vs %+v", cpCreds, srcCreds)
	}
	// Files: copied without the dependency directories, symlinks kept.
	if b, err := os.ReadFile(filepath.Join(e.projDir, "shop-test", "public", "index.php")); err != nil || string(b) != "<?php echo 'shop';" {
		t.Fatalf("index.php not copied: %v %q", err, b)
	}
	if _, err := os.Lstat(filepath.Join(e.projDir, "shop-test", "vendor", "lib", "x.php")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("vendor/ must be skipped by default")
	}
	if fi, err := os.Lstat(filepath.Join(e.projDir, "shop-test", "www")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink not copied: %v", err)
	}
	// The database was dumped from the original and imported into the copy.
	if len(dumped) != 1 || dumped[0] != "envoryx-shop-database" {
		t.Fatalf("dump containers: %v", dumped)
	}
	if len(restored) != 1 || !strings.HasPrefix(restored[0], "envoryx-shop-test-database: ") || !strings.Contains(restored[0], "CREATE TABLE orders") {
		t.Fatalf("restore: %v", restored)
	}
	// The copy's database container was started for the import and stopped again.
	db, _ := e.engine.Container("envoryx-shop-test-database")
	if db.State == "running" {
		t.Fatal("the copy's database must be stopped again after the import")
	}
	// Workers came along, the original is untouched.
	workers, err := e.store.Workers.ListByProject(ctx, cp.ID)
	if err != nil || len(workers) != 1 || workers[0].Name != "queue" || workers[0].ProjectID != cp.ID {
		t.Fatalf("workers: %+v %v", workers, err)
	}
	if srcAfter, err := e.m.Get(ctx, src.ID); err != nil || srcAfter.Project.HTTPPort != src.HTTPPort || srcAfter.Status.State != StateRunning {
		t.Fatalf("the original must stay untouched: %+v %v", srcAfter.Project, err)
	}
}

func TestDuplicateProjectWithoutFilesOrDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	execs := 0
	e.engine.StreamHandler = func(_ string, cmd []string, _ []string, _ []byte) (string, int, error) {
		if len(cmd) == 3 && strings.Contains(cmd[2], "getent passwd") {
			return "", 0, nil // the work user's passwd entry, not a dump
		}
		execs++
		return "", 0, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("x"), 0o644)

	cp, err := e.m.Duplicate(ctx, view.Project.ID, DuplicateRequest{Name: "Shop Empty", Path: "sandbox/shop-empty"})
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if cp.Project.Path != "sandbox/shop-empty" {
		t.Fatalf("path: %s", cp.Project.Path)
	}
	if execs != 0 {
		t.Fatalf("nothing may be dumped when the database is not requested (%d execs)", execs)
	}
	entries, err := os.ReadDir(filepath.Join(e.projDir, "sandbox", "shop-empty", "public"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("the document root must be empty: %v %v", entries, err)
	}
	workers, _ := e.store.Workers.ListByProject(ctx, cp.Project.ID)
	if len(workers) != 0 {
		t.Fatalf("workers must not be copied unless asked: %+v", workers)
	}
}

func TestDuplicateProjectReassignsPublishedPorts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := dbRequest("Shop", true) // database published on a host port
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	src := view.Project
	cp, err := e.m.Duplicate(ctx, src.ID, DuplicateRequest{Name: "Shop Test"})
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	port := func(p store.Project) int {
		svc := p.Service(store.ServiceDatabase)
		var cfg runtime.DatabaseConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		return cfg.HostPort
	}
	srcPort, cpPort := port(src), port(cp.Project)
	if srcPort == 0 || cpPort == 0 || srcPort == cpPort {
		t.Fatalf("the copy needs a database port of its own: %d vs %d", cpPort, srcPort)
	}
	used := []int{src.HTTPPort, srcPort, cp.Project.HTTPPort, cpPort}
	slices.Sort(used)
	if slices.Compact(slices.Clone(used))[3] != used[3] || len(slices.Compact(used)) != 4 {
		t.Fatalf("ports must all differ: %v", used)
	}
}

func TestDuplicateProjectRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	for name, req := range map[string]DuplicateRequest{
		"same name":      {Name: "Shop"},
		"same slug":      {Name: "shop"},
		"empty name":     {Name: "  "},
		"same directory": {Name: "Shop Test", Path: "shop"},
	} {
		if _, err := e.m.Duplicate(ctx, id, req); !errors.Is(err, validate.ErrInvalid) {
			t.Fatalf("%s must be rejected: %v", name, err)
		}
	}
	if _, err := e.m.Duplicate(ctx, store.NewID(), DuplicateRequest{Name: "Ghost"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown source: %v", err)
	}
	// A directory already in use by another project is a conflict, not a silent merge.
	if _, err := e.m.Create(ctx, phpRequest("Other", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Duplicate(ctx, id, DuplicateRequest{Name: "Other"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("existing name must conflict: %v", err)
	}
}

func TestDuplicateProjectRollsBackWhatItCreated(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("x"), 0o644)
	e.engine.FailCreate["envoryx-shop-test-web"] = errors.New("no room on the host")

	if _, err := e.m.Duplicate(ctx, view.Project.ID, allParts("Shop Test")); err == nil {
		t.Fatal("expected the duplicate to fail")
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop-test")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the copied directory must be removed again")
	}
	projects, _ := e.store.Projects.List(ctx)
	if len(projects) != 1 {
		t.Fatalf("the record must be gone: %+v", projects)
	}
	if names := e.engine.NetworkNames(); len(names) != 1 {
		t.Fatalf("the copy's network must be removed: %v", names)
	}
}
