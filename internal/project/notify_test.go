package project

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/notify"
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
	e.engine.SetState("staqio-crashy-php", "exited")
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

	// Creation failures notify with the failing step.
	e.engine.FailCreate = map[string]error{"staqio-broken-web": docker.ErrUnavailable}
	if _, err := e.m.Create(ctx, phpRequest("Broken", true)); err == nil {
		t.Fatal("expected failure")
	}
	got := sender.kinds()
	if last := got[len(got)-1]; !strings.HasPrefix(last, "project.failed:Creating Broken failed") {
		t.Fatalf("failure event: %v", got)
	}
}
