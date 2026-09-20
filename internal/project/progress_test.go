package project

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A create shows up as a running operation with its current step while the image pull
// is in flight, is attached to the project view, and stays visible as finished afterwards.
func TestOperationsReportStepsAndOutcome(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.PullDelay = 300 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		view, err := e.m.Create(ctx, phpRequest("Acme Shop", true))
		if err == nil && view.Status.Operation != nil {
			err = errors.New("create result must not carry its own finished operation")
		}
		done <- err
	}()

	var running Operation
	deadline := time.Now().Add(5 * time.Second)
	for {
		ops := e.m.Operations()
		if len(ops) == 1 && ops[0].Running() && ops[0].ProjectID != "" {
			running = ops[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no running create operation: %+v", ops)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if running.Action != "create" || running.ProjectSlug != "acme-shop" || running.ProjectName != "Acme Shop" {
		t.Fatalf("operation: %+v", running)
	}
	if running.Step != "Pulling the image {{image}}: {{status}}" || running.StepArgs["image"] == "" || !strings.HasPrefix(running.StepText(), "Pulling the image "+running.StepArgs["image"]+": contacting the registry") {
		t.Fatalf("expected the pull step on the running operation: %+v", running)
	}
	if view, err := e.m.Get(ctx, running.ProjectID); err != nil {
		t.Fatalf("get during create: %v", err)
	} else if view.Status.Operation == nil || view.Status.Operation.ID != running.ID {
		t.Fatalf("project view must carry the running operation: %+v", view.Status.Operation)
	}

	if err := <-done; err != nil {
		t.Fatalf("create: %v", err)
	}
	if view, _ := e.m.Get(ctx, running.ProjectID); view.Status.Operation != nil {
		t.Fatalf("finished operation must not stick to the project: %+v", view.Status.Operation)
	}
	ops := e.m.Operations()
	if len(ops) != 1 || ops[0].Running() || ops[0].Error != "" || ops[0].Step != "" {
		t.Fatalf("finished operation: %+v", ops)
	}
	if view, _ := e.m.Get(ctx, running.ProjectID); view.Status.Operation != nil {
		t.Fatalf("finished operation must not stick to the project: %+v", view.Status.Operation)
	}

	// Failures keep their message; successes expire first.
	e.engine.FailStart = map[string]error{"envoryx-acme-shop-php": errors.New("boom")}
	e.engine.PullDelay = 0
	if _, err := e.m.Restart(ctx, running.ProjectID); err == nil {
		t.Fatal("restart should fail")
	}
	ops = e.m.Operations()
	if len(ops) != 2 || ops[0].Action != "restart" || !strings.Contains(ops[0].Error, "boom") {
		t.Fatalf("failed operation: %+v", ops)
	}
	e.m.progress.now = func() time.Time { return time.Now().Add(keepSucceeded + time.Second) }
	ops = e.m.Operations()
	if len(ops) != 1 || ops[0].Action != "restart" {
		t.Fatalf("expected only the failed restart to remain: %+v", ops)
	}
}
