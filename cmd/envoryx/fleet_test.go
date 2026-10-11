package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/offsite"
	"github.com/envoryx/envoryx/internal/store"
)

func TestRemoteBackups(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	cfgDir := t.TempDir()
	backups := &instance.Store{ConfigDir: cfgDir, DBPath: filepath.Join(cfgDir, "none.db"), Dir: filepath.Join(t.TempDir(), "_instance"), Version: "test", LatestSchema: db.LatestVersion(), Log: log}
	done := make(chan struct{}, 1)
	r := &remoteBackups{backups: backups, db: sqlDB, st: st, log: log, done: func() { done <- struct{}{} }}

	if s := r.Status(ctx); len(s.Latest) != 0 || s.Running {
		t.Fatalf("before: %+v", s)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the backup didn't finish")
	}
	s := r.Status(ctx)
	if len(s.Latest) != 1 || s.Latest[0].Kind != instance.KindManual || s.Running || s.LastError != "" || s.Latest[0].Offsite {
		t.Fatalf("after: %+v", s)
	}
	// A copy on the hoster's target shows.
	if err := st.Offsite.Record(ctx, store.OffsiteUpload{TargetID: offsite.ManagedID, Scope: store.OffsiteInstance, BackupID: s.Latest[0].ID, Source: "manual", RemoteKey: "instance/x.tar.gz", SizeBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if s = r.Status(ctx); !s.Latest[0].Offsite || !s.OffsiteAt.Equal(s.Latest[0].CreatedAt) {
		t.Fatalf("with a copy: %+v", s)
	}
}
