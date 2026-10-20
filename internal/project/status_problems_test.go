package project

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
)

// A server that waits on a failed bundle install is no running project: the status names
// the service and the cause instead of the generic "should be running", and the container
// still counts as running for the actions that fix it.
func TestWaitingServerIsAProblem(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, rubyRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-shop-ruby")
	if c.Spec.Healthcheck == nil || !slices.Equal(c.Spec.Healthcheck.Test, runtime.ServerHealthTest) {
		t.Fatalf("the server container needs the waiting check: %+v", c.Spec.Healthcheck)
	}
	if v.Status.State != StateRunning {
		t.Fatalf("before: %s %v", v.Status.State, v.Status.Warnings)
	}

	e.engine.SetHealth("envoryx-shop-ruby", "unhealthy", runtime.WaitBundle+"\n")
	v, _ = e.m.Get(ctx, v.Project.ID)
	joined := strings.Join(v.Status.Warnings, "\n")
	if v.Status.State != StatePartial || !strings.Contains(joined, "the ruby server waits because bundle install failed") || strings.Contains(joined, "should be running") {
		t.Fatalf("waiting server: %s %q", v.Status.State, v.Status.Warnings)
	}
	for _, s := range v.Status.Services {
		if s.Kind == "ruby" && !s.Running {
			t.Fatal("the container runs: actions and the terminal must stay available")
		}
	}

	// An unhealthy check without a note of ours is left to the health badge.
	e.engine.SetHealth("envoryx-shop-ruby", "unhealthy", "something else")
	if v, _ = e.m.Get(ctx, v.Project.ID); v.Status.State != StateRunning {
		t.Fatalf("foreign health output: %s %v", v.Status.State, v.Status.Warnings)
	}
}

func TestServiceProblemMessages(t *testing.T) {
	for out, want := range map[string]string{
		runtime.WaitNodeDeps:              "the node dev server waits for node_modules",
		runtime.WaitVenv:                  "built for another Python version",
		runtime.WaitPythonDeps + ":flask": "waits for flask in .venv",
		runtime.WaitServerExited:          "exited while an action was running",
		runtime.WaitPythonDeps:            "",
		"":                                "",
	} {
		kind := "python"
		if out == runtime.WaitNodeDeps {
			kind = "node"
		}
		got := serviceProblem(kind, dockerUnhealthy(out))
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%q: %q", out, got)
		}
	}
}

// Actions carry the marker the server's supervisor looks for.
func TestActionsAreMarked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, rubyRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	_, _, release, err := e.m.RunAction(ctx, v.Project.ID, "ruby:version", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	release()
	terms := e.engine.Terminals()
	if len(terms) == 0 || !slices.Contains(terms[len(terms)-1].Opts.Env, runtime.ActionEnv+"=ruby:version") {
		t.Fatalf("action env: %+v", terms)
	}
}

// A crash-looping server is named in the status, and its actions stay available while the
// container is up: the supervisor holds it up for them, and the install that fixes the
// crash must be possible.
func TestCrashLoopingServerKeepsItsActions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, rubyRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	e.engine.SetRestarts("envoryx-shop-ruby", 5)
	got, _ := e.m.Get(ctx, v.Project.ID)
	if got.Status.State != StatePartial || !slices.Contains(got.Status.Warnings, "ruby keeps restarting - its main process exits shortly after each start; its logs say why") {
		t.Fatalf("crash loop: %s %q", got.Status.State, got.Status.Warnings)
	}
	actions, err := e.m.ListActions(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.ID == "ruby:version" && !a.Available {
			t.Fatalf("actions of a crash-looping server that is up: %s", a.Reason)
		}
	}
	e.engine.SetState("envoryx-shop-ruby", "restarting")
	actions, _ = e.m.ListActions(ctx, v.Project.ID)
	for _, a := range actions {
		if a.ID == "ruby:version" && (a.Available || !strings.Contains(a.Reason, "keeps restarting")) {
			t.Fatalf("between two starts there is nothing to run it in: %v %s", a.Available, a.Reason)
		}
	}
}

func dockerUnhealthy(output string) docker.Container {
	return docker.Container{State: "running", Health: "unhealthy", HealthOutput: output}
}
