package project

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func waitCron(t *testing.T, m *Manager) {
	t.Helper()
	m.cron().wg.Wait()
}

func TestBuildCronJob(t *testing.T) {
	ok := CronJobRequest{Name: "Report", Runtime: "php", Schedule: " */5  * * * * ", Command: " php artisan report --daily | tee -a storage/logs/report.log ", Enabled: true}
	j, err := buildCronJob("p", ok)
	if err != nil {
		t.Fatal(err)
	}
	if j.Name != "report" || j.Schedule != "*/5  * * * *" || j.Timeout != cronDefaultTimeout || !strings.HasPrefix(j.Command, "php artisan") || strings.HasSuffix(j.Command, " ") {
		t.Fatalf("normalised: %+v", j)
	}
	for name, req := range map[string]CronJobRequest{
		"name":         {Name: "Bad Name!", Runtime: "php", Schedule: "@daily", Command: "true"},
		"runtime":      {Name: "a", Runtime: "ruby", Schedule: "@daily", Command: "true"},
		"schedule":     {Name: "a", Runtime: "php", Schedule: "every day", Command: "true"},
		"never":        {Name: "a", Runtime: "php", Schedule: "0 0 30 2 *", Command: "true"},
		"empty":        {Name: "a", Runtime: "php", Schedule: "@daily", Command: "  "},
		"long":         {Name: "a", Runtime: "php", Schedule: "@daily", Command: strings.Repeat("x", cronCommandLimit+1)},
		"control":      {Name: "a", Runtime: "php", Schedule: "@daily", Command: "echo \x1b[31m"},
		"nul":          {Name: "a", Runtime: "php", Schedule: "@daily", Command: "echo \x00"},
		"timeout":      {Name: "a", Runtime: "php", Schedule: "@daily", Command: "true", Timeout: 25 * time.Hour},
		"subsecond":    {Name: "a", Runtime: "php", Schedule: "@daily", Command: "true", Timeout: 1500 * time.Millisecond},
		"carriage ret": {Name: "a", Runtime: "php", Schedule: "@daily", Command: "echo a\rb"},
	} {
		if _, err := buildCronJob("p", req); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := buildCronJob("p", CronJobRequest{Name: "multi", Runtime: "node", Schedule: "@hourly", Command: "cd scripts\n\tnode cleanup.js"}); err != nil {
		t.Errorf("newlines and tabs are allowed: %v", err)
	}
	if j, err := buildCronJob("p", CronJobRequest{Name: "crlf", Runtime: "php", Schedule: "@hourly", Command: "echo a\r\necho b\r\n"}); err != nil || j.Command != "echo a\necho b" {
		t.Errorf("CRLF: %q %v", j.Command, err)
	}
}

func TestCronJobRuns(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Cron", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	if _, err := e.m.AddCronJob(ctx, id, CronJobRequest{Name: "assets", Runtime: "node", Schedule: "@daily", Command: "npm run build", Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a node job needs a Node.js service: %v", err)
	}
	job, err := e.m.AddCronJob(ctx, id, CronJobRequest{Name: "report", Runtime: "php", Schedule: "*/5 * * * *", Command: "php artisan report && echo done", Timeout: 90 * time.Second, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.NextRun == nil || job.NextRun.Minute()%5 != 0 || job.RuntimeMissing {
		t.Fatalf("info: %+v", job)
	}

	var gotCmd, gotEnv []string
	var container string
	e.engine.StreamHandler = func(name string, cmd, env []string, _ []byte) (string, int, error) {
		if cmd[0] == "timeout" {
			container, gotCmd, gotEnv = name, cmd, env
		}
		return "report written\n", 0, nil
	}
	run, err := e.m.RunCronJobNow(ctx, id, job.ID)
	if err != nil || run.Status != store.CronRunning || run.Source != store.CronSourceManual {
		t.Fatalf("run now: %+v %v", run, err)
	}
	waitCron(t, e.m)
	if container != "envoryx-cron-php" || strings.Join(gotCmd, " ") != "timeout -s TERM -k 10 90 sh -c php artisan report && echo done" {
		t.Fatalf("exec: %s %q", container, gotCmd)
	}
	if !strings.Contains(strings.Join(gotEnv, " "), "ENVORYX_CRON_JOB=report") {
		t.Fatalf("env: %v", gotEnv)
	}
	runs, err := e.m.CronRuns(ctx, id, job.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != store.CronSucceeded || runs[0].Output != "report written\n" || runs[0].FinishedAt.IsZero() {
		t.Fatalf("runs: %+v %v", runs, err)
	}

	// A failure is reported once per job; a timeout is told apart from a plain failure.
	e.engine.StreamHandler = func(string, []string, []string, []byte) (string, int, error) { return "boom\n", 3, nil }
	if _, err := e.m.RunCronJobNow(ctx, id, job.ID); err != nil {
		t.Fatal(err)
	}
	waitCron(t, e.m)
	e.engine.StreamHandler = func(string, []string, []string, []byte) (string, int, error) { return "", 124, nil }
	if _, err := e.m.RunCronJobNow(ctx, id, job.ID); err != nil {
		t.Fatal(err)
	}
	waitCron(t, e.m)
	runs, _ = e.m.CronRuns(ctx, id, job.ID)
	if len(runs) != 3 || runs[0].Status != store.CronTimedOut || runs[1].Status != store.CronFailed || runs[1].ExitCode != 3 {
		t.Fatalf("runs: %+v", runs)
	}
	if got := sender.kinds(); len(got) != 2 || !strings.HasPrefix(got[0], "cron.failed:Cron job report of Cron failed") || !strings.Contains(sender.events[0].Message, "exit code 3") || !strings.Contains(sender.events[0].Message, "boom") {
		t.Fatalf("notifications: %v %+v", got, sender.events)
	}

	// A run is not started twice.
	release := make(chan struct{})
	e.engine.StreamHandler = func(string, []string, []string, []byte) (string, int, error) {
		<-release
		return "", 0, nil
	}
	if _, err := e.m.RunCronJobNow(ctx, id, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RunCronJobNow(ctx, id, job.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second run while the first is going: %v", err)
	}
	if jobs, _ := e.m.CronJobs(ctx, id); len(jobs) != 1 || !jobs[0].Running {
		t.Fatalf("running flag: %+v", jobs)
	}
	close(release)
	waitCron(t, e.m)
	if sender.cleared[len(sender.cleared)-1] != "cron.failed|"+job.ID {
		t.Fatalf("a success clears the failure: %v", sender.cleared)
	}

	// A stopped container gives an error run instead of an exec.
	e.engine.SetState("envoryx-cron-php", "exited")
	if _, err := e.m.RunCronJobNow(ctx, id, job.ID); err != nil {
		t.Fatal(err)
	}
	waitCron(t, e.m)
	runs, _ = e.m.CronRuns(ctx, id, job.ID)
	if runs[0].Status != store.CronError || !strings.Contains(runs[0].Output, "exited") {
		t.Fatalf("stopped container: %+v", runs[0])
	}

	if err := e.m.RemoveCronJob(ctx, id, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CronRuns(ctx, id, job.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("runs of a removed job: %v", err)
	}
}

func TestCronScheduler(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Sched", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	if _, err := e.m.AddCronJob(ctx, id, CronJobRequest{Name: "five", Runtime: "php", Schedule: "*/5 * * * *", Command: "echo five", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddCronJob(ctx, id, CronJobRequest{Name: "off", Runtime: "php", Schedule: "* * * * *", Command: "echo off", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	var fired []string
	var mu sync.Mutex
	e.engine.StreamHandler = func(_ string, cmd, _ []string, _ []byte) (string, int, error) {
		if cmd[0] == "timeout" { // not the passwd entry the first exec adds
			mu.Lock()
			fired = append(fired, cmd[len(cmd)-1])
			mu.Unlock()
		}
		return "", 0, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	next := map[string]cronNext{}
	pass := func(at string) {
		now, _ := time.ParseInLocation("2006-01-02 15:04:05", at, time.Local)
		e.m.cronPass(ctx, now, next, log)
		waitCron(t, e.m)
	}
	pass("2026-09-24 10:03:10") // not due
	pass("2026-09-24 10:05:10") // due
	pass("2026-09-24 10:05:40") // same minute: not again
	pass("2026-09-24 10:09:55")
	pass("2026-09-24 10:10:05") // due
	if strings.Join(fired, ",") != "echo five,echo five" {
		t.Fatalf("fired: %v", fired)
	}

	// A stopped project skips its jobs.
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	pass("2026-09-24 10:15:05")
	if len(fired) != 2 {
		t.Fatalf("stopped project fired: %v", fired)
	}
}

func TestCronPreview(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	got, err := CronPreview("0 3 * * 1", 2, now)
	if err != nil || len(got) != 2 || got[0].Weekday() != time.Monday || got[0].Hour() != 3 {
		t.Fatalf("preview: %v %v", got, err)
	}
	if _, err := CronPreview("61 * * * *", 2, now); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{limit: 8}
	_, _ = b.Write([]byte("hello "))
	_, _ = b.Write([]byte("world!"))
	if b.String() != "o world!" || !b.truncated {
		t.Fatalf("tail: %q %v", b.String(), b.truncated)
	}
	// A multi-byte character cut in half is dropped rather than stored broken.
	u := &tailBuffer{limit: 3}
	_, _ = u.Write([]byte("aäb"))
	if u.String() != "b" && u.String() != "äb" {
		t.Fatalf("utf-8: %q", u.String())
	}
}

// A copy takes the cron jobs along with the workers, without their history.
func TestDuplicateCopiesCronJobs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddCronJob(ctx, view.Project.ID, CronJobRequest{Name: "report", Runtime: "php", Schedule: "@daily", Command: "php artisan report", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	with, err := e.m.Duplicate(ctx, view.Project.ID, DuplicateRequest{Name: "Shop Copy", Workers: true})
	if err != nil {
		t.Fatal(err)
	}
	if jobs, _ := e.m.CronJobs(ctx, with.Project.ID); len(jobs) != 1 || jobs[0].Name != "report" || jobs[0].LastRun != nil {
		t.Fatalf("copied jobs: %+v", jobs)
	}
	without, err := e.m.Duplicate(ctx, view.Project.ID, DuplicateRequest{Name: "Shop Bare"})
	if err != nil {
		t.Fatal(err)
	}
	if jobs, _ := e.m.CronJobs(ctx, without.Project.ID); len(jobs) != 0 {
		t.Fatalf("jobs copied without workers: %+v", jobs)
	}
}
