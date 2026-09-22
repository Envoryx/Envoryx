package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

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
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "runs from the PHP image – this project has no PHP service") {
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
	if c.Spec.Image != "ghcr.io/envoryx/envoryx-node:24" || strings.Join(c.Spec.Cmd, " ") != "npm run worker" {
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
