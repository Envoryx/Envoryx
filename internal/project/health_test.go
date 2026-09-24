package project

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// healthApp is an application whose answer the test controls; every request, wherever
// it was addressed, reaches it.
type healthApp struct {
	mu     sync.Mutex
	status int
	delay  time.Duration
	hosts  []string
	paths  []string
}

func newHealthApp(t *testing.T, e *env) *healthApp {
	a := &healthApp{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		status, delay := a.status, a.delay
		a.hosts = append(a.hosts, r.Host+"|"+r.Header.Get("X-Forwarded-Proto"))
		a.paths = append(a.paths, r.URL.RequestURI())
		a.mu.Unlock()
		time.Sleep(delay)
		if status == http.StatusFound {
			w.Header().Set("Location", "/login")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	e.m.health().client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}}
	return a
}

func (a *healthApp) answer(status int) {
	a.mu.Lock()
	a.status = status
	a.mu.Unlock()
}

func TestHealthCheckDownAndBack(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	app := newHealthApp(t, e)

	// Defaults are stored as zero and shown filled in.
	v, err = e.m.SetHealthCheck(ctx, id, store.HealthCheck{Path: "/health", Status: 200, IntervalSec: 30, Failures: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Project.HealthCheck; got != (store.HealthCheck{Path: "/health", Failures: 2}) {
		t.Fatalf("stored: %+v", got)
	}

	t0 := time.Now()
	e.m.HealthPass(ctx, t0)
	st := e.m.healthStatus(v.Project)
	if st == nil || st.State != HealthUp || st.Status != 200 {
		t.Fatalf("after a good answer: %+v", st)
	}
	if app.hosts[0] != "shop.test|https" || app.paths[0] != "/health" {
		t.Fatalf("request: %v %v", app.hosts, app.paths)
	}
	// Not due before the interval.
	e.m.HealthPass(ctx, t0.Add(10*time.Second))
	if len(app.paths) != 1 {
		t.Fatalf("checked before the interval: %d", len(app.paths))
	}

	app.answer(http.StatusInternalServerError)
	e.m.HealthPass(ctx, t0.Add(30*time.Second))
	if st := e.m.healthStatus(v.Project); st.State != HealthFailing || st.Error != "HTTP 500 instead of 200" {
		t.Fatalf("one failure: %+v", st)
	}
	if len(sender.kinds()) != 0 {
		t.Fatalf("notified after one failure: %v", sender.kinds())
	}
	e.m.HealthPass(ctx, t0.Add(60*time.Second))
	got, _ := e.m.Get(ctx, id)
	if got.Status.Health == nil || got.Status.Health.State != HealthDown {
		t.Fatalf("two failures: %+v", got.Status.Health)
	}
	if len(got.Status.Warnings) != 1 || !strings.HasPrefix(got.Status.Warnings[0], "the health check /health has failed since ") || !strings.HasSuffix(got.Status.Warnings[0], ": HTTP 500 instead of 200") {
		t.Fatalf("warnings: %q", got.Status.Warnings)
	}
	if k := sender.kinds(); len(k) != 1 || k[0] != "project.down:Shop is down" {
		t.Fatalf("notifications: %v", k)
	}
	// Staying down sends nothing more.
	e.m.HealthPass(ctx, t0.Add(90*time.Second))
	if len(sender.kinds()) != 1 {
		t.Fatalf("repeated: %v", sender.kinds())
	}

	app.answer(http.StatusOK)
	e.m.HealthPass(ctx, t0.Add(120*time.Second))
	if st := e.m.healthStatus(v.Project); st.State != HealthUp {
		t.Fatalf("back: %+v", st)
	}
	k := sender.kinds()
	if len(k) != 2 || k[1] != "project.down:Shop is back" {
		t.Fatalf("notifications: %v", k)
	}
	if !strings.Contains(sender.events[1].Message, "after 1 min down") {
		t.Fatalf("recovery message: %q", sender.events[1].Message)
	}
	if len(sender.cleared) == 0 || sender.cleared[0] != "project.down|"+id {
		t.Fatalf("cleared: %v", sender.cleared)
	}
}

func TestHealthCheckPausesAndRedirects(t *testing.T) {
	e := newEnv(t)
	sender := &fakeSender{}
	e.m.SetNotifier(sender)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	app := newHealthApp(t, e)
	if _, err := e.m.SetHealthCheck(ctx, id, store.HealthCheck{Path: "/up", Failures: 1, IntervalSec: 10, TimeoutSec: 1}); err != nil {
		t.Fatal(err)
	}

	// A redirect is a failure that says where it goes.
	app.answer(http.StatusFound)
	t0 := time.Now()
	e.m.HealthPass(ctx, t0)
	p, _ := e.store.Projects.Get(ctx, id)
	if st := e.m.healthStatus(p); st.State != HealthDown || st.Error != "HTTP 302 instead of 200 (redirect to /login)" {
		t.Fatalf("redirect: %+v", st)
	}

	// A slow answer runs into the timeout.
	app.answer(http.StatusOK)
	app.mu.Lock()
	app.delay = 1500 * time.Millisecond
	app.mu.Unlock()
	e.m.HealthPass(ctx, t0.Add(10*time.Second))
	if st := e.m.healthStatus(p); st.State != HealthDown || st.Error != "no answer within 1 s" {
		t.Fatalf("timeout: %+v", st)
	}
	app.mu.Lock()
	app.delay = 0
	app.mu.Unlock()

	// Stopping the project ends the outage without a "back" message and pauses the check.
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	p, _ = e.store.Projects.Get(ctx, id)
	requests := len(app.paths)
	e.m.HealthPass(ctx, t0.Add(20*time.Second))
	if st := e.m.healthStatus(p); st.State != HealthPaused {
		t.Fatalf("stopped: %+v", st)
	}
	if len(app.paths) != requests {
		t.Fatal("a stopped project was checked")
	}
	if k := sender.kinds(); len(k) != 1 {
		t.Fatalf("notifications: %v", k)
	}

	// Switching the check off forgets it.
	v, err = e.m.SetHealthCheck(ctx, id, store.HealthCheck{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Project.HealthCheck.Enabled() || v.Status.Health != nil {
		t.Fatalf("off: %+v %+v", v.Project.HealthCheck, v.Status.Health)
	}
}

func TestHealthCheckValidationAndTest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	for _, h := range []store.HealthCheck{
		{Path: "health"},
		{Path: "/he alth"},
		{Path: "//evil.example/"},
		{Path: "/health", Status: 99},
		{Path: "/health", IntervalSec: 5},
		{Path: "/health", TimeoutSec: 30, IntervalSec: 20},
		{Path: "/health", Failures: 21},
	} {
		if _, err := e.m.SetHealthCheck(ctx, id, h); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v: %v", h, err)
		}
	}

	app := newHealthApp(t, e)
	app.answer(http.StatusNoContent)
	res, err := e.m.CheckHealth(ctx, id, store.HealthCheck{Path: "/ping?deep=1", Status: 204})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Status != 204 || res.URL != "https://shop.test/ping?deep=1" {
		t.Fatalf("test: %+v", res)
	}
	if app.paths[0] != "/ping?deep=1" {
		t.Fatalf("path: %v", app.paths)
	}
	if p, _ := e.store.Projects.Get(ctx, id); p.HealthCheck.Enabled() {
		t.Fatal("the test stored the check")
	}

	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CheckHealth(ctx, id, store.HealthCheck{Path: "/"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stopped project: %v", err)
	}
}

func TestHealthCheckManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	if _, err := e.m.SetHealthCheck(ctx, id, store.HealthCheck{Path: "/health", IntervalSec: 120, Status: 204}); err != nil {
		t.Fatal(err)
	}
	mf, err := e.m.ExportManifest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if h := mf.HealthCheck; h == nil || *h != (manifest.HealthCheck{Path: "/health", Status: 204, Interval: "2m"}) {
		t.Fatalf("exported: %+v", mf.HealthCheck)
	}

	// Spelling out a default is no change; a different timeout is.
	mf.HealthCheck = &manifest.HealthCheck{Path: "/health", Status: 204, Interval: "120s", Timeout: "5s", Failures: 3}
	plan, err := e.m.PlanManifest(ctx, id, mf, ManifestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Changes {
		if c.Section == "healthcheck" {
			t.Fatalf("spurious change: %+v", c)
		}
	}
	mf.HealthCheck.Timeout = "10s"
	if _, err := e.m.ApplyManifest(ctx, id, mf, ManifestOptions{}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.store.Projects.Get(ctx, id)
	if p.HealthCheck != (store.HealthCheck{Path: "/health", Status: 204, IntervalSec: 120, TimeoutSec: 10}) {
		t.Fatalf("applied: %+v", p.HealthCheck)
	}
}
