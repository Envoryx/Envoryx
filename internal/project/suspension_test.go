package project

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

func TestSuspensionStopsProjectsAndRefusesOperations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	holder := plan.Static(nil)
	e.m.SetPlan(holder)
	v, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		e.m.RunSuspension(runCtx, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	defer func() { cancel(); <-done }()

	holder.SetSuspension(&plan.Suspension{Since: time.Now(), Message: "Invoice 1234 is open"})
	waitUntil(t, "the project stopped", func() bool {
		p, err := e.store.Projects.Get(ctx, id)
		return err == nil && p.DesiredState == store.DesiredStopped && e.runningContainers() == 0
	})
	if _, err := e.m.Start(ctx, id); !errors.Is(err, plan.ErrSuspended) {
		t.Fatalf("start while suspended: %v", err)
	}
	if _, err := e.m.Create(ctx, phpRequest("Blog", false)); !errors.Is(err, plan.ErrSuspended) {
		t.Fatalf("create while suspended: %v", err)
	}

	// Lifting it starts nothing; the customer starts the projects again.
	holder.SetSuspension(nil)
	time.Sleep(50 * time.Millisecond)
	if e.runningContainers() != 0 {
		t.Fatal("lifting the suspension started containers")
	}
	if _, err := e.m.Start(ctx, id); err != nil {
		t.Fatalf("start after lifting: %v", err)
	}
	entries, _, err := e.store.Audit.Query(ctx, store.AuditQuery{Actions: []string{"instance."}})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, en := range entries {
		actions = append(actions, en.Action)
	}
	if !slices.Equal(actions, []string{"instance.unsuspended", "instance.suspended"}) {
		t.Errorf("audit = %v", actions)
	}
}

// runningContainers counts the containers the fake engine runs.
func (e *env) runningContainers() int {
	list, _ := e.engine.ListContainers(context.Background(), true, "")
	n := 0
	for _, c := range list {
		if c.State == "running" {
			n++
		}
	}
	return n
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
