package instance

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/validate"
)

func newStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfgDir := t.TempDir()
	s := &Store{ConfigDir: cfgDir, DBPath: filepath.Join(cfgDir, "envoryx.db"), Dir: filepath.Join(t.TempDir(), "_instance"), Version: "test", LatestSchema: db.LatestVersion(), Log: log}
	sqlDB, err := db.Open(context.Background(), s.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('marker', 'before', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	must := func(rel, content string) {
		p := filepath.Join(cfgDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("ca/ca.key", "secret")
	must("ssh/host_ed25519", "hostkey")
	must("notify.json", `{"x":1}`)
	must("projects/p1/php/zz-envoryx.ini", "memory_limit=1G")
	must("projects/p1/ssh/id_ed25519", "deploykey")
	must("projects/p1/home/.composer/cache/big", "cache")
	must("jetbrains/cache", "cache")
	must("backups/old/backup.json", "{}")
	return s, sqlDB
}

func TestCreateListRestoreRoundTrip(t *testing.T) {
	s, sqlDB := newStore(t)
	ctx := context.Background()
	info, err := s.Create(ctx, sqlDB, KindManual, " before upgrade ")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindManual || info.Meta.Note != "before upgrade" || info.Meta.Schema != db.LatestVersion() || info.SizeBytes == 0 {
		t.Fatalf("unexpected info %+v", info)
	}
	// 5 config files: ca.key, host_ed25519, notify.json, zz-envoryx.ini, id_ed25519.
	if info.Meta.Entries != 5 {
		t.Fatalf("entries = %d, want 5", info.Meta.Entries)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].ID != info.ID || list[0].Meta.Note != "before upgrade" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.Get("manual-00000000-000000-0000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get unknown = %v", err)
	}
	if _, err := s.Get("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get traversal = %v", err)
	}

	// Change state after the backup: DB row, a config file, a new file, a new cache.
	if _, err := sqlDB.Exec(`UPDATE settings SET value = 'after' WHERE key = 'marker'`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ConfigDir, "ca", "ca.key"), []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ConfigDir, "ca", "extra.crt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.ConfigDir, "projects", "p2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ConfigDir, "projects", "p1", "home", "later"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.ScheduleRestore(info.ID, "admin (token: cli)"); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleRestore(info.ID, "admin"); !errors.Is(err, ErrPending) {
		t.Fatalf("second schedule = %v", err)
	}
	if err := s.Delete(info.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("delete scheduled = %v", err)
	}
	p, err := s.PendingRestore()
	if err != nil || p == nil || p.ID != info.ID || p.RequestedBy != "admin (token: cli)" {
		t.Fatalf("pending = %+v, %v", p, err)
	}
	_ = sqlDB.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func(ctx context.Context, path string) (*sql.DB, error) { return db.OpenRaw(ctx, path, log) }
	restored, err := s.ApplyPendingRestore(ctx, open)
	if err != nil || restored == nil || restored.ID != info.ID || restored.RequestedBy != "admin (token: cli)" || !strings.HasPrefix(restored.PreRestoreID, "pre-restore-") {
		t.Fatalf("apply = %+v, %v", restored, err)
	}
	if p, _ := s.PendingRestore(); p != nil {
		t.Fatal("marker not consumed")
	}
	// Second call is a no-op.
	if again, err := s.ApplyPendingRestore(ctx, open); err != nil || again != nil {
		t.Fatalf("apply again = %+v, %v", again, err)
	}

	sqlDB2, err := db.Open(ctx, s.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB2.Close()
	var v string
	if err := sqlDB2.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil || v != "before" {
		t.Fatalf("marker = %q, %v", v, err)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(s.ConfigDir, rel))
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(b)
	}
	if got := read("ca/ca.key"); got != "secret" {
		t.Fatalf("ca.key = %q", got)
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "ca", "extra.crt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file added after the backup survived the restore")
	}
	if _, err := os.Stat(filepath.Join(s.ConfigDir, "projects", "p2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("project config added after the backup survived the restore")
	}
	for _, kept := range []string{"projects/p1/home/.composer/cache/big", "projects/p1/home/later", "jetbrains/cache", "backups/old/backup.json"} {
		if read(kept) == "<"+os.ErrNotExist.Error()+">" || strings.HasPrefix(read(kept), "<") {
			t.Fatalf("%s should be kept: %s", kept, read(kept))
		}
	}
	if got := read("projects/p1/ssh/id_ed25519"); got != "deploykey" {
		t.Fatalf("deploy key = %q", got)
	}

	// The pre-restore safety backup exists next to the manual one.
	list, err = s.List()
	if err != nil || len(list) != 2 || list[0].Kind != KindPreRestore || list[1].ID != info.ID {
		t.Fatalf("list after restore = %+v, %v", list, err)
	}
	if err := s.Delete(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
}

func TestImportValidatesArchives(t *testing.T) {
	s, sqlDB := newStore(t)
	ctx := context.Background()
	if _, err := s.Import(strings.NewReader("not an archive")); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("garbage import = %v", err)
	}
	info, err := s.Create(ctx, sqlDB, KindManual, "")
	if err != nil {
		t.Fatal(err)
	}
	rc, name, size, err := s.Open(info.ID)
	if err != nil || name != "envoryx-"+info.ID+".tar.gz" || size != info.SizeBytes {
		t.Fatalf("open = %q %d %v", name, size, err)
	}
	raw, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := s.Import(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if imported.Kind != KindUpload || imported.Meta.Kind != KindManual || imported.SizeBytes != size {
		t.Fatalf("imported = %+v", imported)
	}
	// A backup from a newer build is refused up front.
	s.LatestSchema = info.Meta.Schema - 1
	if _, err := s.Import(bytes.NewReader(raw)); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("newer import = %v", err)
	}
	if err := s.ScheduleRestore(info.ID, "admin"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("newer restore = %v", err)
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 2 {
		t.Fatalf("refused upload left files behind: %d entries", len(entries))
	}
}

func TestAutomaticBackupsArePruned(t *testing.T) {
	s, sqlDB := newStore(t)
	ctx := context.Background()
	for i := 0; i < keepAutomatic+2; i++ {
		if _, err := s.Create(ctx, sqlDB, KindPreMigrate, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, sqlDB, KindManual, ""); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	auto, manual := 0, 0
	for _, b := range list {
		switch b.Kind {
		case KindPreMigrate:
			auto++
		case KindManual:
			manual++
		}
	}
	if auto != keepAutomatic || manual != 1 {
		t.Fatalf("auto = %d, manual = %d", auto, manual)
	}
}

func TestBeforeMigrateHookTakesBackup(t *testing.T) {
	// Simulate a database one version behind by undoing the newest migration
	// (0006 adds projects.ide_gateway) and forgetting that it ran.
	s, sqlDB := newStore(t)
	ctx := context.Background()
	latest := db.LatestVersion()
	if latest != 6 {
		t.Fatalf("schema is at %d: this test undoes migration 0006, teach it to undo the newest one", latest)
	}
	if _, err := sqlDB.Exec(`ALTER TABLE projects DROP COLUMN ide_gateway`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, latest); err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	called := 0
	sqlDB2, err := db.OpenWith(ctx, s.DBPath, log, db.Options{BeforeMigrate: func(ctx context.Context, raw *sql.DB, from, to int) error {
		called++
		if from != latest-1 || to != latest {
			t.Fatalf("from/to = %d/%d", from, to)
		}
		_, err := s.Create(ctx, raw, KindPreMigrate, "")
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB2.Close()
	list, _ := s.List()
	if called != 1 || len(list) != 1 || list[0].Kind != KindPreMigrate || list[0].Meta.Schema != latest-1 {
		t.Fatalf("called = %d, list = %+v", called, list)
	}
	// Fresh databases and up-to-date ones do not trigger the hook.
	fresh, err := db.OpenWith(ctx, filepath.Join(t.TempDir(), "fresh.db"), log, db.Options{BeforeMigrate: func(context.Context, *sql.DB, int, int) error {
		t.Fatal("hook ran for a fresh database")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = fresh.Close()
}
