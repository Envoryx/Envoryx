package project

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/db"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/docker/dockertest"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
)

type env struct {
	t        *testing.T
	m        *Manager
	engine   *dockertest.Fake
	store    *store.Store
	cfgDir   string
	projDir  string
	pathsErr error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	e := &env{t: t, engine: dockertest.New(), store: st, cfgDir: t.TempDir(), projDir: t.TempDir()}
	paths := func() (Paths, error) {
		if e.pathsErr != nil {
			return Paths{}, e.pathsErr
		}
		return Paths{
			ConfigDir: e.cfgDir, ConfigHostDir: "/host/appdata/staqio",
			ProjectsDir: e.projDir, ProjectsHostDir: "/host/development",
			PUID: 1000, PGID: 1000, StaqioVersion: "test",
		}, nil
	}
	e.m = NewManager(st, e.engine, runtime.Default(), paths, audit.New(st.Audit, log), Config{PortRangeStart: 20000, PortRangeEnd: 20005}, log)
	return e
}

func phpRequest(name string, start bool) CreateRequest {
	return CreateRequest{
		Name:          name,
		Docroot:       "public",
		PHP:           &PHPRequest{Version: "8.4", Config: runtime.DefaultPHPConfig()},
		Env:           []EnvVarRequest{{Key: "APP_ENV", Value: "local"}},
		CreateStarter: true,
		Start:         start,
	}
}

func TestCreateProjectProvisionsResources(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	view, err := e.m.Create(ctx, phpRequest("Shimly API", true))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p := view.Project
	if p.Slug != "shimly-api" || p.Path != "shimly-api" || p.HTTPPort != 20000 || p.Lifecycle != store.LifecycleReady {
		t.Fatalf("unexpected project: %+v", p)
	}
	if view.Status.State != StateRunning {
		t.Fatalf("expected running, got %s (%v)", view.Status.State, view.Status.Warnings)
	}
	if len(view.Status.Services) != 2 || !view.Status.Services[0].Running || view.Status.Services[0].Kind != store.ServicePHP {
		t.Fatalf("service status: %+v", view.Status.Services)
	}
	if got := e.engine.NetworkNames(); len(got) != 1 || got[0] != "staqio-shimly-api" {
		t.Fatalf("network: %v", got)
	}
	if got := e.engine.ContainerNames(); strings.Join(got, ",") != "staqio-shimly-api-php,staqio-shimly-api-web" {
		t.Fatalf("containers: %v", got)
	}

	php, _ := e.engine.Container("staqio-shimly-api-php")
	if php.State != "running" || php.Spec.Labels[docker.LabelManaged] != "true" || php.Spec.Labels[docker.LabelProjectID] != p.ID || php.Spec.Labels[docker.LabelService] != "php" {
		t.Fatalf("php container labels/state: %+v", php)
	}
	if php.Spec.Mounts[0].Source != "/host/development/shimly-api" || php.Spec.Mounts[0].Target != "/var/www/html" {
		t.Fatalf("php bind mount must use host path: %+v", php.Spec.Mounts)
	}
	if !strings.Contains(strings.Join(php.Spec.Env, "\n"), "APP_ENV=local") {
		t.Fatalf("env not injected: %v", php.Spec.Env)
	}
	web, _ := e.engine.Container("staqio-shimly-api-web")
	if len(web.Spec.Ports) != 1 || web.Spec.Ports[0].HostPort != 20000 || web.Spec.Ports[0].ContainerPort != 80 {
		t.Fatalf("web ports: %+v", web.Spec.Ports)
	}
	if web.Spec.Mounts[0].ReadOnly != true {
		t.Fatal("web container must mount project files read-only")
	}

	// Generated files.
	for _, f := range []string{"php/zz-staqio.ini", "php/zz-staqio.conf", "web/Caddyfile"} {
		if _, err := os.Stat(filepath.Join(e.cfgDir, "projects", p.ID, f)); err != nil {
			t.Errorf("config file %s missing: %v", f, err)
		}
	}
	caddy, _ := os.ReadFile(filepath.Join(e.cfgDir, "projects", p.ID, "web/Caddyfile"))
	if !strings.Contains(string(caddy), "root * /var/www/html/public") || !strings.Contains(string(caddy), "php_fastcgi php:9000") {
		t.Fatalf("caddyfile: %s", caddy)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shimly-api", "public", "index.php")); err != nil {
		t.Fatalf("starter index.php missing: %v", err)
	}

	// Start order: php before web.
	calls := strings.Join(e.engine.Calls, " ")
	if strings.Index(calls, "start:staqio-shimly-api-php") > strings.Index(calls, "start:staqio-shimly-api-web") {
		t.Fatalf("php must start before web: %s", calls)
	}
	entries, _ := e.store.Audit.Recent(ctx, 10)
	if len(entries) == 0 || entries[0].Action != audit.ActionProjectCreated {
		t.Fatalf("audit entry missing: %+v", entries)
	}
}

func TestCreateRollsBackOnContainerFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.AddForeignContainer("plex", "plexinc/pms-docker", "running")
	e.engine.FailCreate["staqio-broken-web"] = errors.New("simulated docker failure")

	_, err := e.m.Create(ctx, phpRequest("Broken", true))
	if err == nil || !strings.Contains(err.Error(), "simulated docker failure") {
		t.Fatalf("expected failure, got %v", err)
	}
	if got := e.engine.ContainerNames(); strings.Join(got, ",") != "plex" {
		t.Fatalf("rollback left containers behind: %v", got)
	}
	if got := e.engine.NetworkNames(); len(got) != 0 {
		t.Fatalf("rollback left network behind: %v", got)
	}
	if n, _ := e.store.Projects.Count(ctx); n != 0 {
		t.Fatalf("project record must be removed after failed create, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "projects")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(e.cfgDir, "projects"))
		if len(entries) != 0 {
			t.Fatalf("config dir not cleaned: %v", entries)
		}
	}
	// Project files are never deleted by a rollback.
	if _, err := os.Stat(filepath.Join(e.projDir, "broken")); err != nil {
		t.Fatalf("project files must survive rollback: %v", err)
	}
	// The foreign container is untouched.
	for _, c := range e.engine.Calls {
		if strings.Contains(c, "plex") {
			t.Fatalf("foreign container was touched: %s", c)
		}
	}
}

func TestCreateFailsWhenDockerUnavailable(t *testing.T) {
	e := newEnv(t)
	e.engine.Unavailable = true
	_, err := e.m.Create(context.Background(), phpRequest("Offline", false))
	if !errors.Is(err, docker.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if n, _ := e.store.Projects.Count(context.Background()); n != 0 {
		t.Fatal("no project must be recorded when docker is down")
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cases := []CreateRequest{
		{Name: "X"},
		{Name: "Escape", Path: "../etc"},
		{Name: "Absolute", Path: "/etc"},
		{Name: "Hidden", Path: ".ssh"},
		{Name: "Docroot", Docroot: "../../x"},
		{Name: "Version", PHP: &PHPRequest{Version: "5.6"}},
		{Name: "Version2", PHP: &PHPRequest{Version: "8.4 && rm"}},
		{Name: "Env", Env: []EnvVarRequest{{Key: "bad key", Value: "x"}}},
		{Name: "EnvReserved", Env: []EnvVarRequest{{Key: "STAQIO_X", Value: "x"}}},
		{Name: "Web", Web: WebRequest{Type: "nginx"}},
		{Name: "Ext", PHP: &PHPRequest{Version: "8.4", Config: runtime.PHPConfig{Extensions: []string{"evil"}}}},
	}
	for _, c := range cases {
		if _, err := e.m.Create(ctx, c); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", c.Name, err)
		}
	}
	if n, _ := e.store.Projects.Count(ctx); n != 0 {
		t.Fatal("invalid requests must not create projects")
	}
}

func TestCreateRejectsDuplicates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.Create(ctx, phpRequest("Shop", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Create(ctx, phpRequest("shop", false)); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	req := phpRequest("Shop Two", false)
	req.Path = "shop"
	if _, err := e.m.Create(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected path conflict, got %v", err)
	}
}

func TestStartStopRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Site", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if view.Status.State != StateStopped {
		t.Fatalf("expected stopped after create without start, got %s", view.Status.State)
	}

	view, err = e.m.Start(ctx, id)
	if err != nil || view.Status.State != StateRunning || view.Project.DesiredState != store.DesiredRunning {
		t.Fatalf("start: %v %+v", err, view.Status)
	}
	view, err = e.m.Stop(ctx, id)
	if err != nil || view.Status.State != StateStopped || view.Project.DesiredState != store.DesiredStopped {
		t.Fatalf("stop: %v %+v", err, view.Status)
	}
	view, err = e.m.Restart(ctx, id)
	if err != nil || view.Status.State != StateRunning {
		t.Fatalf("restart: %v %+v", err, view.Status)
	}
	// Idempotent start.
	if _, err := e.m.Start(ctx, id); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if _, err := e.m.Start(ctx, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if _, err := e.m.Start(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found for malformed id, got %v", err)
	}
}

func TestStartFailureRecordsError(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Site", false))
	if err != nil {
		t.Fatal(err)
	}
	e.engine.FailStart["staqio-site-web"] = errors.New("port already allocated")
	if _, err := e.m.Start(ctx, view.Project.ID); err == nil {
		t.Fatal("expected start failure")
	}
	view, _ = e.m.Get(ctx, view.Project.ID)
	if view.Status.State != StatePartial || !strings.Contains(view.Project.LastError, "port already allocated") {
		t.Fatalf("expected partial state with error: %+v %q", view.Status, view.Project.LastError)
	}
}

func TestContainerUnexpectedlyStopped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Crashy", true))
	if err != nil {
		t.Fatal(err)
	}
	e.engine.SetState("staqio-crashy-php", "exited")
	view, _ = e.m.Get(ctx, view.Project.ID)
	if view.Status.State != StatePartial {
		t.Fatalf("expected partial, got %s", view.Status.State)
	}
	found := false
	for _, w := range view.Status.Warnings {
		if strings.Contains(w, "should be running") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warning about desired state, got %v", view.Status.Warnings)
	}
	report := e.m.Reconcile(ctx)
	if len(report.Issues) != 1 || report.Issues[0].Severity != "warning" {
		t.Fatalf("reconcile issues: %+v", report.Issues)
	}
	// Start heals it without recreating the healthy container.
	before := len(e.engine.Calls)
	if _, err := e.m.Start(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(e.engine.Calls[before:], " ")
	if strings.Contains(calls, "create:") || !strings.Contains(calls, "start:staqio-crashy-php") {
		t.Fatalf("unexpected calls during heal: %s", calls)
	}
}

func TestStaqioRestartRecognisesExistingContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Persist", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	// Simulate a Staqio restart: new manager on the same DB and same Docker state.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	paths := func() (Paths, error) {
		return Paths{ConfigDir: e.cfgDir, ConfigHostDir: "/host/appdata/staqio", ProjectsDir: e.projDir, ProjectsHostDir: "/host/development", PUID: 1000, PGID: 1000}, nil
	}
	m2 := NewManager(e.store, e.engine, runtime.Default(), paths, audit.New(e.store.Audit, log), Config{PortRangeStart: 20000, PortRangeEnd: 20005}, log)
	report := m2.Reconcile(ctx)
	if report.Error != "" || len(report.Issues) != 0 || len(report.Orphans) != 0 {
		t.Fatalf("clean state expected after restart: %+v", report)
	}
	view, err = m2.Get(ctx, id)
	if err != nil || view.Status.State != StateRunning {
		t.Fatalf("project should be recognised as running: %v %+v", err, view.Status)
	}
	before := len(e.engine.Calls)
	if _, err := m2.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(e.engine.Calls) != before {
		t.Fatalf("start on a running project must be a no-op, got %v", e.engine.Calls[before:])
	}
}

func TestReconcileInterruptedLifecycleAndOrphans(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	stuck := &store.Project{Name: "Stuck", Slug: "stuck", Path: "stuck", Lifecycle: store.LifecycleCreating}
	if err := e.store.Projects.Create(ctx, stuck); err != nil {
		t.Fatal(err)
	}
	e.engine.AddNetwork("staqio-ghost", docker.ManagedLabels("ghost-id", "ghost", "", "test"))
	e.engine.AddForeignContainer("nextcloud", "nextcloud", "running")

	report := e.m.Reconcile(ctx)
	if len(report.Orphans) != 1 || report.Orphans[0].Name != "staqio-ghost" {
		t.Fatalf("orphans: %+v", report.Orphans)
	}
	p, _ := e.store.Projects.Get(ctx, stuck.ID)
	if p.Lifecycle != store.LifecycleFailed {
		t.Fatalf("interrupted create must become failed, got %s", p.Lifecycle)
	}
	if len(e.engine.Calls) != 0 {
		t.Fatalf("reconcile must never mutate docker: %v", e.engine.Calls)
	}
	v, _ := e.m.Get(ctx, stuck.ID)
	if v.Status.State != StateError {
		t.Fatalf("expected error state, got %s", v.Status.State)
	}
}

func TestDeleteRemovesOnlyOwnResources(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.AddForeignContainer("plex", "plexinc/pms-docker", "running")
	e.engine.AddNetwork("bridge", nil)
	a, err := e.m.Create(ctx, phpRequest("Alpha", true))
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.m.Create(ctx, phpRequest("Beta", true))
	if err != nil {
		t.Fatal(err)
	}
	if b.Project.HTTPPort != 20001 {
		t.Fatalf("expected next free port, got %d", b.Project.HTTPPort)
	}

	if err := e.m.Delete(ctx, a.Project.ID, DeleteOptions{Confirm: "wrong"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("expected confirmation failure, got %v", err)
	}
	if err := e.m.Delete(ctx, a.Project.ID, DeleteOptions{Confirm: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.engine.ContainerNames(), ","); got != "plex,staqio-beta-php,staqio-beta-web" {
		t.Fatalf("containers after delete: %s", got)
	}
	if got := strings.Join(e.engine.NetworkNames(), ","); got != "bridge,staqio-beta" {
		t.Fatalf("networks after delete: %s", got)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "projects", a.Project.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("config dir must be removed")
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "alpha")); err != nil {
		t.Fatal("project files must be kept by default")
	}
	if _, err := e.m.Get(ctx, a.Project.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}

	if err := e.m.Delete(ctx, b.Project.ID, DeleteOptions{Confirm: "beta", DeleteFiles: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "beta")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("project files must be removed when requested")
	}
	if _, err := os.Stat(e.projDir); err != nil {
		t.Fatal("projects root must never be removed")
	}
}

func TestUpdateChangesVersionAndRecreates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Upgr", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	cfg := runtime.DefaultPHPConfig()
	cfg.MemoryLimit = "512M"
	view, err = e.m.Update(ctx, id, UpdateRequest{PHP: &PHPRequest{Version: "8.3", Config: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Service(store.ServicePHP).Version != "8.3" || view.Status.State != StateRunning {
		t.Fatalf("update: %+v", view)
	}
	php, _ := e.engine.Container("staqio-upgr-php")
	if php.Spec.Image != "php:8.3-fpm" {
		t.Fatalf("container must be recreated with new image, got %s", php.Spec.Image)
	}
	ini, _ := os.ReadFile(filepath.Join(e.cfgDir, "projects", id, "php/zz-staqio.ini"))
	if !strings.Contains(string(ini), "memory_limit = 512M") {
		t.Fatalf("ini not regenerated: %s", ini)
	}

	newName := "Renamed"
	envs := []EnvVarRequest{{Key: "APP_DEBUG", Value: "true"}}
	view, err = e.m.Update(ctx, id, UpdateRequest{Name: &newName, Env: &envs})
	if err != nil || view.Project.Name != "Renamed" || len(view.Project.Env) != 1 {
		t.Fatalf("rename/env update: %v %+v", err, view.Project)
	}
	php, _ = e.engine.Container("staqio-upgr-php")
	if !strings.Contains(strings.Join(php.Spec.Env, ","), "APP_DEBUG=true") || php.State != "running" {
		t.Fatalf("env must be applied to recreated container: %+v", php.Spec.Env)
	}
}

func TestUpdateEnvOnStoppedProjectKeepsContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Sleepy", false))
	if err != nil {
		t.Fatal(err)
	}
	envs := []EnvVarRequest{{Key: "APP_DEBUG", Value: "true"}}
	view, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Env: &envs})
	if err != nil {
		t.Fatal(err)
	}
	if view.Status.State != StateStopped {
		t.Fatalf("stopped project must stay stopped (not missing) after env update, got %s", view.Status.State)
	}
	php, ok := e.engine.Container("staqio-sleepy-php")
	if !ok || php.State == "running" || !strings.Contains(strings.Join(php.Spec.Env, ","), "APP_DEBUG=true") {
		t.Fatalf("container must be recreated but not started: %+v", php)
	}
}

func TestPreviewShowsPlanWithoutSideEffects(t *testing.T) {
	e := newEnv(t)
	pv, err := e.m.Preview(context.Background(), phpRequest("Preview Me", true))
	if err != nil {
		t.Fatal(err)
	}
	if pv.Slug != "preview-me" || pv.Network != "staqio-preview-me" || len(pv.Containers) != 2 || pv.HTTPPort != 20000 {
		t.Fatalf("preview: %+v", pv)
	}
	if len(e.engine.Calls) != 0 {
		t.Fatalf("preview must not touch docker: %v", e.engine.Calls)
	}
	if n, _ := e.store.Projects.Count(context.Background()); n != 0 {
		t.Fatal("preview must not persist")
	}
}

func TestNotConfiguredWhenHostPathsUnknown(t *testing.T) {
	e := newEnv(t)
	e.pathsErr = errors.New("host path unresolved")
	_, err := e.m.Create(context.Background(), phpRequest("NoPaths", false))
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestBusyLock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Locked", false))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := e.m.lock(view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := e.m.Start(ctx, view.Project.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}
