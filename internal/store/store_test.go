package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), ":memory:", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return store.New(sqlDB)
}

func TestMigrationsAreIdempotentOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "envoryx.db")
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	for i := 0; i < 2; i++ {
		sqlDB, err := db.Open(context.Background(), path, log)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		v, err := db.SchemaVersion(context.Background(), sqlDB)
		if err != nil || v < 1 {
			t.Fatalf("schema version %d %v", v, err)
		}
		_ = sqlDB.Close()
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("database file should be 0600, got %o", perm)
	}
}

func TestUsersAndSessions(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u, err := st.Users.Create(ctx, "Admin", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Users.Create(ctx, "admin", "hash", "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected case-insensitive conflict, got %v", err)
	}
	got, err := st.Users.ByUsername(ctx, "admin")
	if err != nil || got.ID != u.ID {
		t.Fatalf("lookup failed: %+v %v", got, err)
	}
	if _, err := st.Users.ByID(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	n, _ := st.Users.Count(ctx)
	if n != 1 {
		t.Fatalf("expected 1 user, got %d", n)
	}
}

func TestProjectsCRUD(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := &store.Project{
		Name: "Acme Shop", Slug: "acme-shop", Path: "acme-shop", Docroot: "public", HTTPPort: 20000,
		Services: []store.ProjectService{
			{Kind: store.ServicePHP, Variant: "php", Version: "8.4", Image: "php:8.4-fpm", Enabled: true, Config: json.RawMessage(`{"memoryLimit":"256M"}`), Position: 10},
			{Kind: store.ServiceWeb, Variant: "caddy", Version: "2", Image: "caddy:2-alpine", Enabled: true, Position: 20},
		},
		Env: []store.EnvVar{{Key: "APP_ENV", Value: "local"}, {Key: "APP_KEY", Value: "secret", IsSecret: true}},
	}
	if err := st.Projects.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Lifecycle != store.LifecycleCreating {
		t.Fatalf("unexpected project after create: %+v", p)
	}

	dup := &store.Project{Name: "acme shop", Slug: "other", Path: "other"}
	if err := st.Projects.Create(ctx, dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected name conflict, got %v", err)
	}
	dupPort := &store.Project{Name: "Other", Slug: "other", Path: "other", HTTPPort: 20000}
	if err := st.Projects.Create(ctx, dupPort); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected port conflict, got %v", err)
	}

	got, err := st.Projects.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 2 || got.Services[0].Kind != store.ServicePHP || len(got.Env) != 2 {
		t.Fatalf("children not loaded: %+v", got)
	}
	if got.Service(store.ServiceWeb) == nil || got.Service(store.ServiceRedis) != nil {
		t.Fatal("Service lookup broken")
	}

	if err := st.Projects.UpdateState(ctx, p.ID, store.DesiredRunning, store.LifecycleReady, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Projects.UpdateServiceConfig(ctx, p.ID, store.ServicePHP, "8.3", "php:8.3-fpm", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.Projects.ReplaceEnv(ctx, p.ID, []store.EnvVar{{Key: "ONLY", Value: "one"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Projects.Get(ctx, p.ID)
	if got.DesiredState != store.DesiredRunning || got.Lifecycle != store.LifecycleReady || got.Service(store.ServicePHP).Version != "8.3" || len(got.Env) != 1 {
		t.Fatalf("updates not applied: %+v", got)
	}

	ports, _ := st.Projects.UsedPorts(ctx)
	if len(ports) != 1 || ports[0] != 20000 {
		t.Fatalf("used ports: %v", ports)
	}
	list, err := st.Projects.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if err := st.Projects.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Projects.Get(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected not found after delete, got %v", err)
	}
	// Cascade removed children.
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM project_services`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("services not cascaded: %d %v", n, err)
	}
}

func TestSettingsAndAudit(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.Settings.Get(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected not found")
	}
	if err := st.Settings.Set(ctx, "x", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Settings.Set(ctx, "x", "2"); err != nil {
		t.Fatal(err)
	}
	v, _ := st.Settings.Get(ctx, "x")
	if v != "2" {
		t.Fatalf("expected upsert, got %q", v)
	}
	if err := st.Audit.Append(ctx, store.AuditEntry{Action: "test", Username: "admin"}); err != nil {
		t.Fatal(err)
	}
	entries, err := st.Audit.Recent(ctx, 10)
	if err != nil || len(entries) != 1 || entries[0].Action != "test" || string(entries[0].Details) != "{}" {
		t.Fatalf("audit: %v %+v", err, entries)
	}
}
