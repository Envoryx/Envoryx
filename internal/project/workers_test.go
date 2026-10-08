package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestWorkersRunAsExtraContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	for _, bad := range []WorkerRequest{
		{Name: "Bad Name", Preset: "laravel:queue", Enabled: true},
		{Name: "q", Preset: "shell", Enabled: true},
		{Name: "q", Preset: "laravel:queue", Arg: "default; rm -rf /", Enabled: true},
		{Name: "q", Preset: "laravel:schedule", Arg: "extra", Enabled: true},
		{Name: "s", Preset: "php:script", Arg: "../../etc/passwd", Enabled: true},
	} {
		if _, err := e.m.AddWorker(ctx, id, bad); !errors.Is(err, validate.ErrInvalid) {
			t.Fatalf("%+v must be rejected, got %v", bad, err)
		}
	}
	q, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "laravel:queue", Arg: "default,emails", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-shop-worker-queue")
	if !ok {
		t.Fatal("worker container missing")
	}
	if got := strings.Join(c.Spec.Cmd, " "); got != "php artisan queue:work --tries=3 --sleep=3 --max-time=3600 --queue=default,emails" {
		t.Fatalf("cmd: %s", got)
	}
	if c.State != "running" || c.Spec.User != "1000:1000" || c.Spec.Labels["envoryx.service"] != "worker:"+q.ID || c.Spec.Image != "ghcr.io/envoryx/envoryx-php:8.4" {
		t.Fatalf("worker container: state=%s %+v", c.State, c.Spec)
	}
	v, _ := e.m.Get(ctx, id)
	var found bool
	for _, s := range v.Status.Services {
		if s.Kind == "worker" && s.Variant == "queue" && s.Running && s.WorkerID == q.ID {
			found = true
		}
	}
	if !found || v.Status.State != StateRunning {
		t.Fatalf("status must include the worker: %+v", v.Status)
	}
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "laravel:schedule", Enabled: true}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name must conflict: %v", err)
	}

	// Changing the argument recreates the container; disabling removes it.
	if _, err := e.m.UpdateWorker(ctx, id, q.ID, WorkerRequest{Name: "queue", Preset: "laravel:queue", Arg: "high", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-shop-worker-queue")
	if !strings.Contains(strings.Join(c.Spec.Cmd, " "), "--queue=high") {
		t.Fatalf("cmd after update: %v", c.Spec.Cmd)
	}
	if _, err := e.m.UpdateWorker(ctx, id, q.ID, WorkerRequest{Name: "queue", Preset: "laravel:queue", Arg: "high", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-shop-worker-queue"); ok {
		t.Fatal("disabled worker container must be removed")
	}
	v, _ = e.m.Get(ctx, id)
	if v.Status.State != StateRunning {
		t.Fatalf("disabled worker must not affect state: %s", v.Status.State)
	}

	// Stop/Start covers workers; Remove deletes the definition and container.
	if _, err := e.m.UpdateWorker(ctx, id, q.ID, WorkerRequest{Name: "queue", Preset: "laravel:queue", Arg: "high", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-shop-worker-queue")
	if c.State == "running" {
		t.Fatal("stop must stop workers")
	}
	if err := e.m.RemoveWorker(ctx, id, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-shop-worker-queue"); ok {
		t.Fatal("removed worker container must be gone")
	}
	if _, err := e.m.ServiceContainer(ctx, id, WorkerKind(q)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed worker lookup: %v", err)
	}
}

// The Messenger consumer sets up its transports first: with the recipe's
// doctrine://default?auto_setup=0 nothing else creates messenger_messages, and the
// consumer crash-looped on the missing table. The Workers section still shows the plain
// command.
func TestMessengerWorkerSetsUpItsTransports(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	w, err := e.m.AddWorker(ctx, view.Project.ID, WorkerRequest{Name: "messenger", Preset: "symfony:messenger", Arg: "async,high", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-shop-worker-messenger")
	consume := []string{"php", "bin/console", "messenger:consume", "async", "high", "--time-limit=3600", "-vv"}
	if len(c.Spec.Cmd) < 4 || c.Spec.Cmd[0] != "sh" || !slices.Equal(c.Spec.Cmd[4:], consume) {
		t.Fatalf("cmd: %q", c.Spec.Cmd)
	}
	for _, want := range []string{"until php bin/console messenger:setup-transports --no-interaction async; do", "messenger:setup-transports --no-interaction high; do", "sleep 10", `exec "$@"`} {
		if !strings.Contains(c.Spec.Cmd[2], want) {
			t.Errorf("guard misses %q: %s", want, c.Spec.Cmd[2])
		}
	}
	if shown, _ := WorkerDisplayCommand(w); !slices.Equal(shown, consume) {
		t.Errorf("display: %q", shown)
	}

	// Presets without a guard keep their plain command.
	if _, err := e.m.AddWorker(ctx, view.Project.ID, WorkerRequest{Name: "schedule", Preset: "symfony:scheduler", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-shop-worker-schedule")
	if c.Spec.Cmd[0] != "php" {
		t.Errorf("scheduler cmd: %q", c.Spec.Cmd)
	}
}

// A worker runs in the runtime of its preset: PHP presets need the PHP service, Node
// presets the Node service and its image, with the project home mounted.
func TestWorkersFollowTheirRuntime(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, nodeRequest("Front", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	_, err = e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "laravel:queue", Enabled: true})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "runs from the PHP image - this project has no PHP service") {
		t.Fatalf("PHP worker on a node-only project: %v", err)
	}
	if v, _ := e.m.Get(ctx, id); len(v.Project.Workers) != 0 {
		t.Fatal("no worker may be stored")
	}
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "bad", Preset: "node:file", Arg: "../outside.js", Enabled: true}); err == nil {
		t.Fatal("a script path outside the project must be rejected")
	}
	w, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "jobs", Preset: "node:script", Arg: "worker", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-front-worker-jobs")
	if !ok || c.State != "running" {
		t.Fatalf("node worker container: %+v", c)
	}
	if c.Spec.Image != "ghcr.io/envoryx/envoryx-node:24" || strings.Join(c.Spec.Cmd[4:], " ") != "npm run worker" || !strings.Contains(c.Spec.Cmd[2], "node_modules") {
		t.Fatalf("node worker spec: image=%s cmd=%v", c.Spec.Image, c.Spec.Cmd)
	}
	home := false
	for _, m := range c.Spec.Mounts {
		if m.Target == homeMountTarget {
			home = true
		}
		if m.Target == phpIniTarget {
			t.Fatal("a Node worker must not mount the PHP ini")
		}
	}
	if !home {
		t.Fatalf("node worker must get the project home: %+v", c.Spec.Mounts)
	}
	if !slices.Contains(c.Spec.Env, "NODE_ENV=development") {
		t.Fatalf("node worker env: %v", c.Spec.Env)
	}
	if _, err := e.m.UpdateWorker(ctx, id, w.ID, WorkerRequest{Name: "jobs", Preset: "php:script", Arg: "bin/w.php", Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("switching a worker to a runtime the project lacks must fail: %v", err)
	}

	// Presets carry their runtime for the UI.
	for _, p := range WorkerPresets() {
		if p.Runtime == "" {
			t.Fatalf("preset %s has no runtime", p.ID)
		}
	}
}

// A worker runs in its runtime's environment (RAILS_ENV …). Switching the runtime to
// production recreates the runtime's container at once; the worker is reported as
// outdated until a restart gives it the new environment too.
func TestWorkersFollowTheRuntimeModeOnRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, rubyRequest("Modes", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	w, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "jobs", Preset: "ruby:file", Arg: "bin/worker.rb", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	name := "envoryx-modes-worker-" + w.Name
	hasVar := func(want string) bool {
		c, ok := e.engine.Container(name)
		if !ok {
			t.Fatalf("worker container %s missing", name)
		}
		return slices.Contains(c.Spec.Env, want)
	}
	if !hasVar("RAILS_ENV=development") {
		t.Fatal("worker without the development environment")
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Ruby: &RubyUpdate{Enabled: true, Version: "3.4", Config: runtime.RubyConfig{Server: true, Preset: "rails", Mode: "production"}}}); err != nil {
		t.Fatal(err)
	}
	outdated := false
	for _, i := range e.m.Reconcile(ctx).Issues {
		if strings.Contains(i.Message, "older setup") {
			outdated = true
		}
	}
	if !outdated && !hasVar("RAILS_ENV=production") {
		t.Fatal("a worker left in development must be reported")
	}
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !hasVar("RAILS_ENV=production") {
		t.Fatal("the restart must give the worker the production environment")
	}
}

// A worker Docker keeps restarting is shown as restarting although the listing says
// running, and it can be restarted on its own; the restart resets Docker's count.
func TestCrashLoopingWorkerAndRestart(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	q, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "laravel:queue", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	workerState := func() (string, bool, State) {
		v, _ := e.m.Get(ctx, id)
		for _, s := range v.Status.Services {
			if s.WorkerID == q.ID {
				return s.State, s.Running, v.Status.State
			}
		}
		return "", false, v.Status.State
	}
	// One restart while it comes up is no loop.
	e.engine.SetRestarts("envoryx-shop-worker-queue", 1)
	if state, running, _ := workerState(); state != "running" || !running {
		t.Fatalf("a single restart: worker %s running=%v", state, running)
	}
	e.engine.SetRestarts("envoryx-shop-worker-queue", 4)
	if state, running, project := workerState(); state != "restarting" || running || project != StatePartial {
		t.Fatalf("crash loop: worker %s running=%v, project %s", state, running, project)
	}
	before, _ := e.engine.Container("envoryx-shop-worker-queue")
	web, _ := e.engine.Container("envoryx-shop-web")
	if err := e.m.RestartWorker(ctx, id, q.ID); err != nil {
		t.Fatal(err)
	}
	if state, running, _ := workerState(); state != "running" || !running {
		t.Fatalf("after the restart: %s running=%v", state, running)
	}
	if c, _ := e.engine.Container("envoryx-shop-worker-queue"); c.ID != before.ID {
		t.Fatal("a restart keeps the container")
	}
	if c, _ := e.engine.Container("envoryx-shop-web"); c.ID != web.ID || c.State != "running" {
		t.Fatal("the rest of the project keeps running")
	}

	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := e.m.RestartWorker(ctx, id, q.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stopped project's worker is not restarted: %v", err)
	}
}
