package project

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
)

type fakeSender struct {
	mu      sync.Mutex
	events  []notify.Event
	cleared []string
}

func (f *fakeSender) Notify(_ context.Context, e notify.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeSender) Clear(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, key)
}

func (f *fakeSender) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, e.Kind+":"+e.Title)
	}
	return out
}

func TestNotificationsForHealthAndFailures(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()

	view, err := e.m.Create(ctx, phpRequest("Crashy", true))
	if err != nil {
		t.Fatal(err)
	}
	e.m.Reconcile(ctx)
	if len(sender.events) != 0 {
		t.Fatalf("healthy project must not notify: %v", sender.kinds())
	}
	e.engine.SetState("envoryx-crashy-php", "exited")
	e.m.Reconcile(ctx)
	e.m.Reconcile(ctx) // second run must not repeat
	if got := sender.kinds(); len(got) != 1 || got[0] != "project.unhealthy:Crashy needs attention" {
		t.Fatalf("unhealthy events: %v", got)
	}
	if _, err := e.m.Start(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	e.m.Reconcile(ctx)
	if got := sender.kinds(); len(got) != 2 || got[1] != "project.unhealthy:Crashy recovered" || len(sender.cleared) != 1 {
		t.Fatalf("recovery events: %v cleared=%v", got, sender.cleared)
	}

	// A project deleted while unhealthy did not recover: no event, the warning is cleared.
	e.engine.SetState("envoryx-crashy-php", "exited")
	e.m.Reconcile(ctx)
	if err := e.m.Delete(ctx, view.Project.ID, DeleteOptions{Confirm: view.Project.Slug}); err != nil {
		t.Fatal(err)
	}
	before := len(sender.kinds())
	e.m.Reconcile(ctx)
	if got := sender.kinds(); len(got) != before || len(sender.cleared) != 2 {
		t.Fatalf("events after deleting an unhealthy project: %v cleared=%v", got[before:], sender.cleared)
	}

	// Creation failures notify with the failing step.
	e.engine.FailCreate = map[string]error{"envoryx-broken-web": docker.ErrUnavailable}
	if _, err := e.m.Create(ctx, phpRequest("Broken", true)); err == nil {
		t.Fatal("expected failure")
	}
	got := sender.kinds()
	if last := got[len(got)-1]; !strings.HasPrefix(last, "project.failed:Creating Broken failed") {
		t.Fatalf("failure event: %v", got)
	}
}

// A stop takes as long as its slowest container needs to exit; the half-stopped project
// seen meanwhile must not raise an alarm, nor end one that was raised before.
func TestNoHealthNotificationWhileAnOperationRuns(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()

	view, err := e.m.Create(ctx, phpRequest("Slow", true))
	if err != nil {
		t.Fatal(err)
	}
	_, h := e.m.progress.begin(ctx, Operation{Action: "stop", ProjectID: view.Project.ID})
	e.engine.SetState("envoryx-slow-php", "exited")
	e.m.Reconcile(ctx)
	if got := sender.kinds(); len(got) != 0 {
		t.Fatalf("a running stop must not notify: %v", got)
	}
	h.end(nil)
	e.m.Reconcile(ctx)
	if got := sender.kinds(); len(got) != 1 || got[0] != "project.unhealthy:Slow needs attention" {
		t.Fatalf("unhealthy events: %v", got)
	}

	// Unhealthy and busy: it has not recovered yet.
	_, h = e.m.progress.begin(ctx, Operation{Action: "restart", ProjectID: view.Project.ID})
	e.m.Reconcile(ctx)
	h.end(nil)
	if got := sender.kinds(); len(got) != 1 {
		t.Fatalf("a running restart must not report a recovery: %v", got)
	}
}
