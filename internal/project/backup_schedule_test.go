package project

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
)

// A stopped project is backed up like a running one: its database container is started
// for the dump and stopped again.
func TestBackupOfStoppedProject(t *testing.T) {
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
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	info, err := e.m.CreateBackup(ctx, id, BackupOptions{Database: true, Files: true})
	if err != nil {
		t.Fatalf("backup of a stopped project: %v", err)
	}
	if info.Meta.Database == nil || info.Meta.Files == nil {
		t.Fatalf("backup parts: %+v", info.Meta)
	}
	if !strings.Contains(strings.Join(e.engine.Calls, " "), "start:envoryx-shop-database") {
		t.Fatalf("the database must be started for the dump: %v", e.engine.Calls)
	}
	if c, _ := e.engine.Container("envoryx-shop-database"); c.State == "running" {
		t.Fatal("the database must be stopped again after the backup")
	}
}

// Every failed scheduled run is recorded on the project, written to the audit log and
// notified with the count of failures in a row; a successful run clears the failure.
func TestSchedulerRecordsEveryFailure(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()
	failing := true
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		if failing {
			return "", 1, nil
		}
		return "-- dump\n", 0, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.SetBackupSchedule(ctx, id, store.BackupSchedule{Schedule: "daily", Hour: 3, Keep: 2}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	day := func(d int) time.Time { return time.Date(2026, 9, 10+d, 4, 0, 0, 0, time.Local) }
	for d := 0; d < 3; d++ {
		e.m.runScheduledBackups(ctx, day(d), log)
	}
	p, _ := e.m.store.Projects.Get(ctx, id)
	if p.Backup.Failures != 3 || !strings.Contains(p.Backup.LastError, "dump failed") {
		t.Fatalf("outcome after three failures: %d %q", p.Backup.Failures, p.Backup.LastError)
	}
	var failed []notify.Event
	for _, ev := range sender.events {
		if ev.Kind == "backup.failed" {
			failed = append(failed, ev)
		}
	}
	if len(failed) != 3 || !strings.Contains(failed[2].Message, "3 times in a row") || strings.Contains(failed[0].Message, "in a row") || failed[0].Key == failed[1].Key {
		t.Fatalf("every failed run must notify: %+v", failed)
	}
	entries, _ := e.store.Audit.Recent(ctx, 50)
	audited := 0
	for _, en := range entries {
		if en.Action == audit.ActionBackupFailed {
			audited++
		}
	}
	if audited != 3 {
		t.Fatalf("audit entries for failed runs: %d", audited)
	}
	failing = false
	e.m.runScheduledBackups(ctx, day(3), log)
	if p, _ := e.m.store.Projects.Get(ctx, id); p.Backup.Failures != 0 || p.Backup.LastError != "" {
		t.Fatalf("a successful run must clear the failure: %d %q", p.Backup.Failures, p.Backup.LastError)
	}
}

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
