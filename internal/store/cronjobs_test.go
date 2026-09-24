package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/store"
)

func TestCronJobsAndRuns(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p := &store.Project{Name: "Cron", Slug: "cron", Path: "cron", HTTPPort: 20000}
	if err := st.Projects.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	j := &store.CronJob{ProjectID: p.ID, Name: "report", Runtime: "php", Schedule: "*/5 * * * *", Command: "php artisan report", Timeout: 90 * time.Second, Enabled: true}
	if err := st.CronJobs.Add(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := st.CronJobs.Add(ctx, &store.CronJob{ProjectID: p.ID, Name: "report", Runtime: "php", Schedule: "@daily", Command: "true"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	got, err := st.CronJobs.Get(ctx, p.ID, j.ID)
	if err != nil || got.Timeout != 90*time.Second || got.Command != "php artisan report" || got.LastRun != nil {
		t.Fatalf("get: %+v %v", got, err)
	}

	// Three runs with keep=2: the oldest goes, the newest is the job's last run.
	var last store.CronRun
	for i, status := range []string{store.CronSucceeded, store.CronFailed, store.CronSucceeded} {
		run := store.CronRun{JobID: j.ID, Source: store.CronSourceSchedule}
		if err := st.CronJobs.StartRun(ctx, &run); err != nil {
			t.Fatal(err)
		}
		run.Status, run.ExitCode, run.Output = status, i, "out"
		if err := st.CronJobs.FinishRun(ctx, &run, 2); err != nil {
			t.Fatal(err)
		}
		last = run
	}
	runs, err := st.CronJobs.Runs(ctx, j.ID, 10)
	if err != nil || len(runs) != 2 || runs[0].ID != last.ID || runs[0].Output != "out" || runs[1].Status != store.CronFailed {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	jobs, err := st.CronJobs.ListByProject(ctx, p.ID)
	if err != nil || len(jobs) != 1 || jobs[0].LastRun == nil || jobs[0].LastRun.ID != last.ID || jobs[0].LastRun.Output != "" {
		t.Fatalf("list: %+v %v", jobs, err)
	}

	// A run left running by a crashed process is marked interrupted.
	running := store.CronRun{JobID: j.ID, Source: store.CronSourceManual}
	if err := st.CronJobs.StartRun(ctx, &running); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CronJobs.InterruptRunning(ctx); err != nil || n != 1 {
		t.Fatalf("interrupt: %d %v", n, err)
	}

	j.Enabled = false
	if err := st.CronJobs.Update(ctx, *j); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := st.CronJobs.ListEnabled(ctx); len(enabled) != 0 {
		t.Fatalf("disabled job listed: %+v", enabled)
	}
	// Deleting the project takes jobs and runs with it.
	if err := st.Projects.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if runs, _ := st.CronJobs.Runs(ctx, j.ID, 10); len(runs) != 0 {
		t.Fatalf("runs survived the project: %+v", runs)
	}
}
