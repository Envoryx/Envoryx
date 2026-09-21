package project

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
)

// Envoryx going down with "projects follow Envoryx" on takes the project containers with
// it without touching what the user wants; the next start brings exactly those projects
// back.
func TestProjectsFollowEnvoryxStopAndResume(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if e.m.ProjectsFollowEnvoryx(ctx) {
		t.Fatal("must be off by default")
	}
	if err := e.m.SetProjectsFollowEnvoryx(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !e.m.ProjectsFollowEnvoryx(ctx) {
		t.Fatal("setting not stored")
	}

	running, err := e.m.Create(ctx, phpRequest("Running", true))
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := e.m.Create(ctx, phpRequest("Stopped", false))
	if err != nil {
		t.Fatal(err)
	}
	// A shared helper outside any project is taken down as well.
	if err := e.engine.PullImage(ctx, "adminer:latest", nil); err != nil {
		t.Fatal(err)
	}
	helperID, err := e.engine.CreateContainer(ctx, docker.ContainerSpec{Name: "envoryx-helper", Image: "adminer:latest",
		Labels: map[string]string{docker.LabelManaged: "true", docker.LabelSystem: "dbtool"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.engine.StartContainer(ctx, helperID); err != nil {
		t.Fatal(err)
	}

	before := len(e.engine.Calls)
	e.m.StopAllForShutdown(ctx)
	calls := strings.Join(e.engine.Calls[before:], " ")
	if !strings.Contains(calls, "stop:envoryx-running-php") || !strings.Contains(calls, "stop:envoryx-helper") {
		t.Fatalf("expected the running project and the helper to be stopped, got %s", calls)
	}
	if strings.Contains(calls, "stop:envoryx-stopped-") {
		t.Fatalf("a stopped project has nothing to stop, got %s", calls)
	}
	view, _ := e.m.Get(ctx, running.Project.ID)
	if view.Status.State != StateStopped || view.Project.DesiredState != store.DesiredRunning {
		t.Fatalf("after shutdown stop: state=%s desired=%s, want stopped/running", view.Status.State, view.Project.DesiredState)
	}

	before = len(e.engine.Calls)
	e.m.ResumeProjects(ctx, log)
	calls = strings.Join(e.engine.Calls[before:], " ")
	if !strings.Contains(calls, "start:envoryx-running-php") {
		t.Fatalf("expected the project to be resumed, got %s", calls)
	}
	if strings.Contains(calls, "envoryx-stopped-") || strings.Contains(calls, "envoryx-helper") {
		t.Fatalf("resume must only start projects the user wants running, got %s", calls)
	}
	view, _ = e.m.Get(ctx, running.Project.ID)
	if view.Status.State != StateRunning {
		t.Fatalf("resumed project state = %s", view.Status.State)
	}
	view, _ = e.m.Get(ctx, stopped.Project.ID)
	if view.Status.State != StateStopped {
		t.Fatalf("stopped project state = %s", view.Status.State)
	}
	// The dashboard can tell the user what happened.
	if act := e.m.Activity(); len(act) != 1 || act[0].Kind != ActivityProjectsResumed || len(act[0].Items) != 1 || act[0].Items[0] != "Running" {
		t.Fatalf("activity = %+v", act)
	}

	// Nothing to do when everything already runs.
	before = len(e.engine.Calls)
	e.m.ResumeProjects(ctx, log)
	if calls := strings.Join(e.engine.Calls[before:], " "); strings.Contains(calls, "start:") {
		t.Fatalf("resume on running projects must be a no-op, got %s", calls)
	}
}
