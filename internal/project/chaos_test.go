package project

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
)

// Chaos tests: the failures a home server actually produces – the Docker daemon dying
// under a running operation, a kill -9 in the middle of a backup, a backup share that
// went read-only – must leave Envoryx in a state it can explain and recover from.

func TestDockerVanishesMidRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	view, err := e.m.Create(ctx, phpRequest("Flaky", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	// Restart pulls first; the daemon goes away while that pull is in flight.
	e.engine.PullDelay = 300 * time.Millisecond
	go func() {
		time.Sleep(50 * time.Millisecond)
		e.engine.SetUnavailable(true)
	}()
	_, err = e.m.Restart(ctx, id)
	if !errors.Is(err, docker.ErrUnavailable) {
		t.Fatalf("restart with a vanishing daemon = %v, want ErrUnavailable", err)
	}
	e.engine.PullDelay = 0

	// While Docker is down: the failure is recorded and readable, the status carries the
	// outage as a warning, reconcile reports it instead of inventing orphans or flipping
	// lifecycles.
	view, err = e.m.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Lifecycle != store.LifecycleReady || !strings.Contains(view.Project.LastError, "unavailable") {
		t.Fatalf("after outage: lifecycle=%s lastError=%q", view.Project.Lifecycle, view.Project.LastError)
	}
	if !strings.Contains(strings.Join(view.Status.Warnings, ";"), "Docker engine unavailable") {
		t.Fatalf("status while docker is down must say so: %+v", view.Status)
	}
	report := e.m.Reconcile(ctx)
	if !strings.HasPrefix(report.Error, "docker:") || len(report.Orphans) != 0 || len(report.Issues) != 0 {
		t.Fatalf("reconcile while docker is down: %+v", report)
	}
	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "flaky"}); !errors.Is(err, docker.ErrUnavailable) {
		t.Fatalf("delete while docker is down = %v", err)
	}
	if p, err := e.store.Projects.Get(ctx, id); err != nil || p.Lifecycle != store.LifecycleReady {
		t.Fatalf("a failed delete must not change the project: %+v %v", p, err)
	}

	// Docker is back: a plain Start heals the project and clears the error.
	e.engine.SetUnavailable(false)
	view, err = e.m.Start(ctx, id)
	if err != nil {
		t.Fatalf("start after docker returned: %v", err)
	}
	if view.Status.State != StateRunning || view.Project.LastError != "" {
		t.Fatalf("after recovery: state=%s lastError=%q", view.Status.State, view.Project.LastError)
	}
	report = e.m.Reconcile(ctx)
	if report.Error != "" || len(report.Issues) != 0 {
		t.Fatalf("reconcile after recovery: %+v", report)
	}
}

func TestInterruptedBackupsSweptAtStartup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("v1"), 0o644)
	root := filepath.Join(e.cfgDir, "backups", "shop")

	// A complete backup whose record was never written (killed between the last file
	// and the database insert).
	info, err := e.m.CreateBackup(ctx, id, BackupOptions{Files: true, Note: "unrecorded"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Backups.Delete(ctx, id, info.ID); err != nil {
		t.Fatal(err)
	}
	// A backup killed half-way: files, no metadata.
	partial := filepath.Join(root, "20260919-120000-deadbeef")
	_ = os.MkdirAll(partial, 0o700)
	_ = os.WriteFile(filepath.Join(partial, backupFilesFile), []byte("half a tarball"), 0o600)
	// A backup of another installation's project with the same slug: not ours.
	foreign := filepath.Join(root, "20260919-130000-cafebabe")
	_ = os.MkdirAll(foreign, 0o700)
	_ = os.WriteFile(filepath.Join(foreign, backupMetaFile), []byte(`{"projectId":"00000000-0000-0000-0000-000000000000"}`), 0o600)
	// Something that is not a backup directory at all.
	_ = os.MkdirAll(filepath.Join(root, "notes"), 0o700)

	e.m.SweepBackups(ctx)

	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial backup must be removed, stat err=%v", err)
	}
	for _, keep := range []string{foreign, filepath.Join(root, "notes"), filepath.Join(root, info.Dir)} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s must be left alone: %v", keep, err)
		}
	}
	list, err := e.m.ListBackups(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Dir != info.Dir || list[0].Missing || list[0].Kind != "files" || list[0].Meta.Note != "unrecorded" || list[0].SizeBytes == 0 {
		t.Fatalf("adopted backup: %+v", list)
	}
	// Idempotent: a second sweep changes nothing.
	e.m.SweepBackups(ctx)
	if list2, _ := e.m.ListBackups(ctx, id); len(list2) != 1 {
		t.Fatalf("second sweep duplicated the record: %+v", list2)
	}
	// And the adopted backup restores.
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("v2"), 0o644)
	if _, err := e.m.RestoreBackup(ctx, id, list[0].ID, RestoreOptions{Files: true, Confirm: "shop"}); err != nil {
		t.Fatalf("restore adopted backup: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.projDir, "shop", "public", "index.php")); string(got) != "v1" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestBackupOnReadOnlyShareFailsCleanly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	e := newEnv(t)
	ctx := context.Background()
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	e.backupsDir = t.TempDir()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if err := os.Chmod(e.backupsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.backupsDir, 0o700) })

	_, err = e.m.CreateBackup(ctx, id, BackupOptions{Files: true})
	if err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("backup on a read-only share = %v, want a permission error", err)
	}
	kinds := sender.kinds()
	if len(kinds) != 1 || !strings.HasPrefix(kinds[0], "backup.failed:") {
		t.Fatalf("notification: %v", kinds)
	}
	if list, _ := e.m.ListBackups(ctx, id); len(list) != 0 {
		t.Fatalf("nothing must be recorded: %+v", list)
	}
	if entries, _ := os.ReadDir(e.backupsDir); len(entries) != 0 {
		t.Fatalf("nothing must be left on the share: %v", entries)
	}

	// The scheduler survives it too: the slot is consumed (no retry storm), the
	// project stays untouched and usable.
	if _, err := e.m.SetBackupSchedule(ctx, id, store.BackupSchedule{Schedule: "daily", Hour: 3, Keep: 2}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	at := time.Date(2026, 9, 20, 4, 0, 0, 0, time.Local)
	e.m.runScheduledBackups(ctx, at, log)
	p, _ := e.store.Projects.Get(ctx, id)
	if !p.Backup.LastRun.Equal(at) {
		t.Fatalf("failed scheduled backup must still consume its slot: lastRun=%v", p.Backup.LastRun)
	}
	if len(sender.kinds()) != 2 {
		t.Fatalf("scheduled failure must notify as well: %v", sender.kinds())
	}
	if view, err := e.m.Stop(ctx, id); err != nil || view.Status.State != StateStopped {
		t.Fatalf("project must stay operable after a failed backup: %v %+v", err, view.Status)
	}

	// Share writable again: the next backup just works.
	if err := os.Chmod(e.backupsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateBackup(ctx, id, BackupOptions{Files: true}); err != nil {
		t.Fatalf("backup after the share came back: %v", err)
	}
}
