package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	shopPath  = "/api/v1/projects/11111111-1111-4111-8111-111111111111"
	queueID   = "33333333-3333-4333-8333-333333333333"
	backupJob = "44444444-4444-4444-8444-444444444444"
)

func workerRoutes() map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/projects/{id}/workers": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"workers": []map[string]any{{"id": queueID, "name": "queue", "preset": "laravel:queue", "arg": "default,emails", "enabled": true,
					"command": []string{"php", "artisan", "queue:work", "--queue=default,emails"}}},
				"presets": []map[string]any{
					{"id": "laravel:queue", "description": "php artisan queue:work", "argLabel": "Queues", "argHint": "e.g. default,emails"},
					{"id": "laravel:schedule", "description": "php artisan schedule:work", "runtime": "php"},
					{"id": "node:script", "description": "npm run <name>", "runtime": "node"},
				},
				"suggestedPreset": "laravel:queue",
			})
		},
		"POST /api/v1/projects/{id}/workers": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"worker": map[string]any{"name": "schedule", "command": []string{"php", "artisan", "schedule:work"}}})
		},
		"PUT /api/v1/projects/{id}/workers/{worker}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"worker": map[string]any{"name": "queue"}})
		},
		"POST /api/v1/projects/{id}/workers/{worker}/restart": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
		"DELETE /api/v1/projects/{id}/workers/{worker}": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	}
}

func TestWorkerListAndPresets(t *testing.T) {
	srv := newFakeServer(t, workerRoutes())
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"worker", "list", "acme-shop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"queue", "laravel:queue", "php artisan queue:work --queue=default,emails"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := c.run(context.Background(), []string{"worker", "presets", "acme-shop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"laravel:queue *", "Queues: e.g. default,emails", "laravel:schedule", "suggested for acme-shop"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	// The Node service of acme-shop is switched off: its presets aren't offered.
	if strings.Contains(out.String(), "node:script") {
		t.Fatalf("offers a preset for a runtime the project lacks:\n%s", out.String())
	}
	out.Reset()
	// blog has only Node: no PHP preset, and the PHP suggestion isn't mentioned.
	if err := c.run(context.Background(), []string{"worker", "presets", "blog"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "node:script") || strings.Contains(got, "laravel:") || strings.Contains(got, "suggested") {
		t.Fatalf("presets for blog:\n%s", got)
	}
}

func TestWorkerAddSendsPresetAndArg(t *testing.T) {
	srv := newFakeServer(t, workerRoutes())
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"worker", "add", "acme-shop", "schedule", "--preset", "laravel:schedule", "--disabled"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(srv.bodies[shopPath+"/workers"], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["name"] != "schedule" || sent["preset"] != "laravel:schedule" || sent["enabled"] != false {
		t.Fatalf("sent %v", sent)
	}
	if !strings.Contains(out.String(), "php artisan schedule:work") {
		t.Fatalf("output: %s", out.String())
	}
	if err := c.run(context.Background(), []string{"worker", "add", "acme-shop", "schedule"}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("without --preset: %v", err)
	}
}

// An edit keeps what was not given; disable only flips enabled.
func TestWorkerEditKeepsTheRest(t *testing.T) {
	srv := newFakeServer(t, workerRoutes())
	c, _, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"worker", "disable", "acme-shop", "queue"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(srv.bodies[shopPath+"/workers/"+queueID], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["name"] != "queue" || sent["preset"] != "laravel:queue" || sent["arg"] != "default,emails" || sent["enabled"] != false {
		t.Fatalf("disable sent %v", sent)
	}
	// "--arg ''" clears the argument; a new preset without --arg drops the old one too.
	if err := c.run(context.Background(), []string{"worker", "edit", "acme-shop", "queue", "--arg", ""}); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(srv.bodies[shopPath+"/workers/"+queueID], &sent)
	if sent["arg"] != "" || sent["enabled"] != true {
		t.Fatalf("edit --arg '' sent %v", sent)
	}
	if err := c.run(context.Background(), []string{"worker", "edit", "acme-shop", "queue"}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("edit without changes: %v", err)
	}
}

func TestWorkerRestartAndRemove(t *testing.T) {
	srv := newFakeServer(t, workerRoutes())
	c, _, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"worker", "restart", "acme-shop", queueID}); err != nil {
		t.Fatal(err)
	}
	if err := c.run(context.Background(), []string{"worker", "remove", "acme-shop", "queue"}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("remove without --yes: %v", err)
	}
	if err := c.run(context.Background(), []string{"worker", "remove", "acme-shop", "queue", "--yes"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(srv.requests, "\n")
	for _, want := range []string{"POST " + shopPath + "/workers/" + queueID + "/restart", "DELETE " + shopPath + "/workers/" + queueID} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	if err := c.run(context.Background(), []string{"worker", "restart", "acme-shop", "mailer"}); err == nil || !strings.Contains(err.Error(), `no worker "mailer"`) {
		t.Fatalf("unknown worker: %v", err)
	}
}

func cronRoutes(runs func() []map[string]any) map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/projects/{id}/cron": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"timezone": "Europe/Berlin", "jobs": []map[string]any{{
				"id": backupJob, "name": "backup", "runtime": "php", "schedule": "0 3 * * *", "command": "php artisan backup:run\nphp artisan backup:clean",
				"timeoutSeconds": 600, "enabled": true, "nextRun": "2026-10-13T03:00:00+02:00",
			}}})
		},
		"POST /api/v1/projects/{id}/cron": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"job": map[string]any{"name": "report", "schedule": "@daily"}})
		},
		"PUT /api/v1/projects/{id}/cron/{job}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"job": map[string]any{"name": "backup", "schedule": "0 4 * * *"}})
		},
		"POST /api/v1/projects/{id}/cron/{job}/run": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"run": map[string]any{"id": "run-2", "status": "running"}})
		},
		"GET /api/v1/projects/{id}/cron/{job}/runs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"runs": runs()})
		},
		"DELETE /api/v1/projects/{id}/cron/{job}": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	}
}

func TestCronListShowsNextRunAndTimezone(t *testing.T) {
	srv := newFakeServer(t, cronRoutes(nil))
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"cron", "list", "acme-shop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backup", "0 3 * * *", "php artisan backup:run …", "Europe/Berlin"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
}

// The command comes after "--", flags included; the runtime defaults to the application's.
func TestCronAddTakesTheCommandAfterDashDash(t *testing.T) {
	srv := newFakeServer(t, cronRoutes(nil))
	c, _, _ := newTestCLI(t, srv, "")
	err := c.run(context.Background(), []string{"cron", "add", "acme-shop", "report", "--schedule", "@daily", "--max-time", "2m", "--", "php", "artisan", "report", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(srv.bodies[shopPath+"/cron"], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["command"] != "php artisan report --json" || sent["runtime"] != "php" || sent["timeoutSeconds"] != float64(120) || sent["enabled"] != true {
		t.Fatalf("sent %v", sent)
	}
	if err := c.run(context.Background(), []string{"cron", "add", "acme-shop", "report", "--", "true"}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("without --schedule: %v", err)
	}
}

// The words after "--" come split by the local shell; the job's command is a shell line,
// so each word keeps its quoting. A single word is a whole command line.
func TestCronCommandKeepsTheWords(t *testing.T) {
	for _, tc := range []struct {
		after []string
		want  string
	}{
		{[]string{"php", "artisan", "report", "--json"}, "php artisan report --json"},
		{[]string{"php", "bin/console", "app:mail", "--subject=Daily report", "it's"}, `php bin/console app:mail '--subject=Daily report' 'it'\''s'`},
		{[]string{"echo", "", "$HOME", "a&&b"}, `echo '' '$HOME' 'a&&b'`},
		{[]string{"php artisan schedule:run && php artisan queue:prune"}, "php artisan schedule:run && php artisan queue:prune"},
	} {
		if got := cronCommandArg("", tc.after); got != tc.want {
			t.Errorf("%q: got %s, want %s", tc.after, got, tc.want)
		}
	}
	if got := cronCommandArg("from the flag", nil); got != "from the flag" {
		t.Errorf("--command: %q", got)
	}
}

func TestCronEditKeepsTheRest(t *testing.T) {
	srv := newFakeServer(t, cronRoutes(nil))
	c, _, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"cron", "edit", "acme-shop", "backup", "--schedule", "0 4 * * *"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(srv.bodies[shopPath+"/cron/"+backupJob], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["schedule"] != "0 4 * * *" || sent["command"] != "php artisan backup:run\nphp artisan backup:clean" || sent["timeoutSeconds"] != float64(600) || sent["enabled"] != true {
		t.Fatalf("sent %v", sent)
	}
}

// --wait follows the run and hands its exit code to the shell.
func TestCronRunWaitReturnsTheExitCode(t *testing.T) {
	old := cronPollInterval
	cronPollInterval = time.Millisecond
	t.Cleanup(func() { cronPollInterval = old })
	polls := 0
	srv := newFakeServer(t, cronRoutes(func() []map[string]any {
		polls++
		if polls < 3 {
			return []map[string]any{{"id": "run-2", "status": "running"}, {"id": "run-1", "status": "succeeded"}}
		}
		return []map[string]any{{"id": "run-2", "status": "failed", "exitCode": 3, "output": "disk full", "finishedAt": "2026-10-12T10:00:05Z"}}
	}))
	c, out, _ := newTestCLI(t, srv, "")
	err := c.run(context.Background(), []string{"cron", "run", "acme-shop", "backup", "--wait"})
	var exit *exitCodeError
	if !errors.As(err, &exit) || exit.code != 3 {
		t.Fatalf("exit: %v", err)
	}
	if out.String() != "disk full\n" {
		t.Fatalf("output %q", out.String())
	}
}

func TestCronRunsAndRemove(t *testing.T) {
	srv := newFakeServer(t, cronRoutes(func() []map[string]any {
		return []map[string]any{{"id": "run-1", "source": "schedule", "status": "succeeded", "exitCode": 0, "output": "done\n",
			"startedAt": "2026-10-12T03:00:00Z", "finishedAt": "2026-10-12T03:00:42Z"}}
	}))
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"cron", "runs", "acme-shop", "backup"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"schedule", "succeeded", "42s"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := c.run(context.Background(), []string{"cron", "runs", "acme-shop", "backup", "--output"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "done\n" {
		t.Fatalf("output %q", out.String())
	}
	if err := c.run(context.Background(), []string{"cron", "remove", "acme-shop", "backup", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(srv.requests, "\n"), "DELETE "+shopPath+"/cron/"+backupJob) {
		t.Fatalf("requests: %v", srv.requests)
	}
}
