package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
)

// A backup becomes a project of its own: the settings it was made with (services,
// variables, workers, cron jobs) and its database and files, also once the project it
// came from is deleted.
func TestRestoreBackupIntoNewProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var imported []string
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		switch cmd[0] {
		case "mariadb-dump":
			return "-- dump of shop\n", 0, nil
		case "mariadb":
			imported = append(imported, container+": "+string(stdin))
			return "", 0, nil
		}
		return "", 0, nil
	}
	req := dbRequest("Shop", false)
	req.Env = []EnvVarRequest{{Key: "API_KEY", Value: "s3cret", IsSecret: true}}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "laravel:queue", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddCronJob(ctx, id, CronJobRequest{Name: "report", Runtime: "php", Schedule: "0 3 * * *", Command: "php report.php", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetLimits(ctx, id, store.ResourceLimits{App: store.LimitSet{MemoryMB: 768}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetHealthCheck(ctx, id, store.HealthCheck{Path: "/up", Status: 200, IntervalSec: 30, TimeoutSec: 5, Failures: 3}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("v1"), 0o644)
	backup, err := e.m.CreateBackup(ctx, id, BackupOptions{Database: true, Files: true})
	if err != nil {
		t.Fatal(err)
	}

	// From an existing project, under a name of its own.
	if _, err := e.m.RestoreIntoNewProject(ctx, id, backup.ID, RestoreNewRequest{Name: "Shop", Database: true, Files: true}); err == nil {
		t.Fatal("the name of an existing project was taken")
	}
	copyView, err := e.m.RestoreIntoNewProject(ctx, id, backup.ID, RestoreNewRequest{Name: "Shop Restored", Database: true, Files: true})
	if err != nil {
		t.Fatal(err)
	}
	restored := copyView.Project
	if restored.Slug != "shop-restored" || restored.HTTPPort == view.Project.HTTPPort || restored.Lifecycle != store.LifecycleReady {
		t.Fatalf("new project: %+v", restored)
	}
	if b, _ := os.ReadFile(filepath.Join(e.projDir, "shop-restored", "public", "index.php")); string(b) != "v1" {
		t.Fatalf("files: %q", b)
	}
	if len(imported) != 1 || imported[0] != "envoryx-shop-restored-database: -- dump of shop\n" {
		t.Fatalf("database: %v", imported)
	}
	full, _ := e.store.Projects.Get(ctx, restored.ID)
	if len(full.Workers) != 1 || full.Workers[0].Name != "queue" {
		t.Fatalf("workers: %+v", full.Workers)
	}
	if len(full.Env) != 1 || full.Env[0].Value != "s3cret" || !full.Env[0].IsSecret {
		t.Fatalf("env: %+v", full.Env)
	}
	if full.Limits.App.MemoryMB != 768 || full.HealthCheck.Path != "/up" {
		t.Fatalf("limits %+v, health check %+v", full.Limits, full.HealthCheck)
	}
	if jobs, _ := e.store.CronJobs.ListByProject(ctx, restored.ID); len(jobs) != 1 || jobs[0].Command != "php report.php" {
		t.Fatalf("cron jobs: %+v", jobs)
	}

	// The original is deleted: its backup stays, is listed and can bring it back under
	// its old name, here without the database.
	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "shop", DeleteFiles: true}); err != nil {
		t.Fatal(err)
	}
	orphans, err := e.m.OrphanedBackups(ctx)
	if err != nil || len(orphans) != 1 || orphans[0].ProjectID != id || orphans[0].Slug != "shop" || orphans[0].Backup.Missing {
		t.Fatalf("orphaned backups: %+v %v", orphans, err)
	}
	imported = nil
	back, err := e.m.RestoreIntoNewProject(ctx, id, backup.ID, RestoreNewRequest{Name: "Shop", Files: true})
	if err != nil {
		t.Fatal(err)
	}
	if back.Project.Slug != "shop" || len(imported) != 0 {
		t.Fatalf("restored original: %+v, imported %v", back.Project, imported)
	}
	if b, _ := os.ReadFile(filepath.Join(e.projDir, "shop", "public", "index.php")); string(b) != "v1" {
		t.Fatalf("files of the original: %q", b)
	}
	// The project of that name exists again, but the backup still belongs to the deleted
	// one (another ID); deleting it there removes its files.
	if err := e.m.DeleteOrphanedBackup(ctx, id, backup.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "backups", "shop", backup.Dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup files left: %v", err)
	}
	if orphans, _ := e.m.OrphanedBackups(ctx); len(orphans) != 0 {
		t.Fatalf("orphans after delete: %+v", orphans)
	}
}

// A failed restore leaves nothing behind: no project, no directory.
func TestRestoreIntoNewProjectRollsBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		switch cmd[0] {
		case "mariadb-dump":
			return "-- dump\n", 0, nil
		case "mariadb":
			return "", 1, nil // the import fails
		}
		return "", 0, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	backup, err := e.m.CreateBackup(ctx, view.Project.ID, BackupOptions{Database: true, Files: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RestoreIntoNewProject(ctx, view.Project.ID, backup.ID, RestoreNewRequest{Name: "Shop Two", Database: true, Files: true}); err == nil {
		t.Fatal("a failed import must fail the restore")
	}
	if list, _ := e.store.Projects.List(ctx); len(list) != 1 {
		t.Fatalf("projects after the failed restore: %d", len(list))
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop-two")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory left behind: %v", err)
	}
}
