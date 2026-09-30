package instance

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/db"
)

func TestLoadIDCreatesOnceAndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	id, created, err := LoadID(dir)
	if err != nil || !created || !validID.MatchString(id) {
		t.Fatalf("first start: %q %v %v", id, created, err)
	}
	again, created, err := LoadID(dir)
	if err != nil || created || again != id {
		t.Fatalf("second start: %q %v %v, want %q", again, created, err, id)
	}
	// A damaged file is not silently replaced: a new ID would disown every resource.
	if err := os.WriteFile(filepath.Join(dir, IDFile), []byte("Not An ID!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadID(dir); err == nil {
		t.Fatal("invalid ID accepted")
	}
}

// Like the secret key, the instance ID stays out of backups and a restore keeps the
// running instance's: a backup restored into a second instance on the same Docker host
// must not give it the first one's ID.
func TestInstanceIDStaysOutOfBackups(t *testing.T) {
	s, sqlDB := newStore(t)
	ctx := context.Background()
	path := filepath.Join(s.ConfigDir, IDFile)
	if _, _, err := LoadID(s.ConfigDir); err != nil {
		t.Fatal(err)
	}
	info, err := s.Create(ctx, sqlDB, KindManual, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleRestore(info.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	openRaw := func(ctx context.Context, path string) (*sql.DB, error) { return db.OpenRaw(ctx, path, s.Log) }
	if _, err := s.ApplyPendingRestore(ctx, openRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the backup brought an instance ID back: %v", err)
	}

	// The running instance's ID survives a restore.
	if err := os.WriteFile(path, []byte("second-instance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleRestore(info.ID, "admin", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPendingRestore(ctx, openRaw); err != nil {
		t.Fatal(err)
	}
	if id, _, err := LoadID(s.ConfigDir); err != nil || id != "second-instance" {
		t.Fatalf("after restore: %q %v", id, err)
	}
}
