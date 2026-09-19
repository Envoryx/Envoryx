package project

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/store"
)

// A browser that navigates away mid-pull cancels the request context; the project must
// still be created rather than rolled back.
func TestOperationsOutliveTheCaller(t *testing.T) {
	e := newEnv(t)
	e.engine.PullDelay = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	view, err := e.m.Create(ctx, phpRequest("Detached", true))
	if err != nil {
		t.Fatalf("create must survive the caller's cancellation: %v", err)
	}
	if view.Project.Lifecycle != store.LifecycleReady {
		t.Fatalf("lifecycle = %s", view.Project.Lifecycle)
	}
	if view.Status.State != StateRunning {
		t.Fatalf("state = %s, want running", view.Status.State)
	}
}

// At shutdown a running operation gets the grace period; when that runs out it is cut
// off with a readable reason, and nothing new is accepted.
func TestShutdownInterruptsAfterGrace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Grace", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	// Strip the project down so Start has to pull again – the pull is where a real
	// engine call blocks and where the shutdown interrupts it.
	containers, _ := e.engine.ListContainers(ctx, true, id)
	for _, c := range containers {
		if err := e.engine.RemoveContainer(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		if imgID, err := e.engine.ImageID(ctx, c.Image); err == nil {
			_ = e.engine.RemoveImage(ctx, imgID)
		}
	}

	e.engine.PullDelay = 2 * time.Second
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, err := e.m.Start(ctx, id)
		result <- err
	}()
	<-started
	time.Sleep(50 * time.Millisecond)

	begin := time.Now()
	if finished := e.m.Shutdown(100 * time.Millisecond); finished {
		t.Fatal("Shutdown reported all operations finished although one was blocked")
	}
	if took := time.Since(begin); took > time.Second {
		t.Fatalf("Shutdown waited %v, must return right after the grace period", took)
	}

	err = <-result
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("interrupted operation returned %v, want ErrInterrupted", err)
	}
	p, err := e.store.Projects.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.LastError, "interrupted by an Envoryx restart") {
		t.Fatalf("last error must explain the interruption, got %q", p.LastError)
	}
	if p.Lifecycle != store.LifecycleReady {
		t.Fatalf("lifecycle = %s, an interrupted restart must not mark the project failed", p.Lifecycle)
	}

	if _, err := e.m.Start(ctx, id); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("start during shutdown = %v, want ErrShuttingDown", err)
	}
	if _, err := e.m.Create(ctx, phpRequest("Late", false)); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("create during shutdown = %v, want ErrShuttingDown", err)
	}
}

// Operations that finish within the grace period are not disturbed.
func TestShutdownWaitsForRunningOperations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.PullDelay = 150 * time.Millisecond

	result := make(chan error, 1)
	go func() {
		_, err := e.m.Create(ctx, phpRequest("Finishes", true))
		result <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if !e.m.Shutdown(2 * time.Second) {
		t.Fatal("Shutdown must report success when the operation finished within the grace period")
	}
	if err := <-result; err != nil {
		t.Fatalf("operation must complete unharmed: %v", err)
	}
}

// The periodic reconcile must not declare a project "interrupted" while its creation
// is still running and holds the lock.
func TestReconcileLeavesRunningCreateAlone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.PullDelay = 300 * time.Millisecond

	result := make(chan error, 1)
	go func() {
		_, err := e.m.Create(ctx, phpRequest("Slow", true))
		result <- err
	}()
	time.Sleep(80 * time.Millisecond) // the row exists, images are still "pulling"

	report := e.m.Reconcile(ctx)
	for _, is := range report.Issues {
		if strings.Contains(is.Message, "interrupted") {
			t.Fatalf("reconcile flagged the running create: %+v", is)
		}
	}
	if err := <-result; err != nil {
		t.Fatalf("create: %v", err)
	}
	projects, _ := e.store.Projects.List(ctx)
	if len(projects) != 1 || projects[0].Lifecycle != store.LifecycleReady || projects[0].LastError != "" {
		t.Fatalf("project after create: %+v", projects)
	}
}
