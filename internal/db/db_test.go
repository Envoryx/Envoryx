package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRefusesCorruptFile(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "envoryx.db")
	sqlDB, err := Open(ctx, path, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, sqlDB); err != nil {
		t.Fatalf("healthy database: %v", err)
	}
	// Checkpoint the WAL so the main file holds the pages, then scribble over a
	// page in the middle of it.
	if _, err := sqlDB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.Close()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	junk := make([]byte, 4096)
	for i := range junk {
		junk[i] = 0xAB
	}
	if _, err := f.WriteAt(junk, 4096*2); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	_, err = Open(ctx, path, log)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt database: got %v, want ErrCorrupt", err)
	}
}
