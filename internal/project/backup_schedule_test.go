package project

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/seramos/staqio/internal/store"
)

func TestBackupDue(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, loc) // Friday
	if d := backupDue(store.BackupSchedule{}, now); !d.IsZero() {
		t.Fatal("off schedule must not be due")
	}
	daily := store.BackupSchedule{Schedule: "daily", Hour: 3}
	if d := backupDue(daily, now); d != time.Date(2026, 9, 18, 3, 0, 0, 0, loc) {
		t.Fatalf("daily due: %v", d)
	}
	if d := backupDue(store.BackupSchedule{Schedule: "daily", Hour: 22}, now); d != time.Date(2026, 9, 17, 22, 0, 0, 0, loc) {
		t.Fatalf("daily (later today) must point to yesterday: %v", d)
	}
	weekly := store.BackupSchedule{Schedule: "weekly", Hour: 3, Weekday: 0} // Sunday
	if d := backupDue(weekly, now); d != time.Date(2026, 9, 13, 3, 0, 0, 0, loc) {
		t.Fatalf("weekly due: %v", d)
	}
}

func TestSchedulerCreatesBackupsAndAppliesRetention(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		return "-- dump\n", 0, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.SetBackupSchedule(ctx, id, store.BackupSchedule{Schedule: "hourly"}); err == nil {
		t.Fatal("invalid schedule must be rejected")
	}
	if _, err := e.m.SetBackupSchedule(ctx, id, store.BackupSchedule{Schedule: "daily", Hour: 3, Keep: 2}); err != nil {
		t.Fatal(err)
	}
	// A manual backup is never subject to retention.
	if _, err := e.m.CreateBackup(ctx, id, BackupOptions{Database: true, Files: true, Note: "manual"}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	day := func(d int) time.Time { return time.Date(2026, 9, 10+d, 4, 0, 0, 0, time.Local) }
	for d := 0; d < 4; d++ {
		e.m.runScheduledBackups(ctx, day(d), log)
		e.m.runScheduledBackups(ctx, day(d).Add(time.Minute), log) // same slot: no second backup
	}
	list, err := e.m.ListBackups(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var manual, scheduled int
	for _, b := range list {
		if b.Meta.Source == "scheduled" {
			scheduled++
		} else {
			manual++
		}
	}
	if manual != 1 || scheduled != 2 {
		t.Fatalf("retention: manual=%d scheduled=%d (want 1/2)", manual, scheduled)
	}
	p, _ := e.m.store.Projects.Get(ctx, id)
	if p.Backup.LastRun.IsZero() {
		t.Fatal("last run must be recorded")
	}
	// Switching the schedule off stops the scheduler.
	if _, err := e.m.SetBackupSchedule(ctx, id, store.BackupSchedule{Schedule: "", Hour: 3, Keep: 2}); err != nil {
		t.Fatal(err)
	}
	e.m.runScheduledBackups(ctx, day(10), log)
	list, _ = e.m.ListBackups(ctx, id)
	if len(list) != 3 {
		t.Fatalf("no new backups after disabling: %d", len(list))
	}
}
