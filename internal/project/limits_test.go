package project

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func resourcesOf(t *testing.T, e *env, name string) docker.Resources {
	t.Helper()
	d, err := e.engine.InspectContainer(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return d.Resources
}

func TestLimitsApplyPerGroupAndInPlace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Shop", true)
	req.Database = &DatabaseRequest{Type: "mariadb"}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	// Without limits every container still has the default process limit.
	if r := resourcesOf(t, e, "envoryx-shop-php"); r != (docker.Resources{PidsLimit: DefaultPidsLimit}) {
		t.Fatalf("defaults: %+v", r)
	}
	phpC, _ := e.engine.Container("envoryx-shop-php")
	php := phpC.ID

	limits := store.ResourceLimits{App: store.LimitSet{CPUs: 1.5, MemoryMB: 1024}, Services: store.LimitSet{MemoryMB: 512}, Pids: 2048}
	v, err = e.m.SetLimits(ctx, id, limits)
	if err != nil {
		t.Fatal(err)
	}
	if v.Project.Limits != limits {
		t.Fatalf("stored: %+v", v.Project.Limits)
	}
	if r := resourcesOf(t, e, "envoryx-shop-php"); r != (docker.Resources{NanoCPUs: 1_500_000_000, MemoryBytes: 1 << 30, PidsLimit: 2048}) {
		t.Fatalf("php: %+v", r)
	}
	if r := resourcesOf(t, e, "envoryx-shop-web"); r.MemoryBytes != 1<<30 {
		t.Fatalf("the web server is an application container: %+v", r)
	}
	if r := resourcesOf(t, e, "envoryx-shop-database"); r != (docker.Resources{MemoryBytes: 512 << 20, PidsLimit: 2048}) {
		t.Fatalf("database: %+v", r)
	}
	// Changed in place: the same container keeps running.
	if c, _ := e.engine.Container("envoryx-shop-php"); c.ID != php || c.State != "running" {
		t.Fatalf("php must be updated, not recreated: %+v", c)
	}

	// Lifting the memory limit needs a new container, which runs again.
	limits.App.MemoryMB = 0
	if _, err := e.m.SetLimits(ctx, id, limits); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-shop-php")
	if c.ID == php || c.State != "running" {
		t.Fatalf("php must be recreated and running: %+v", c)
	}
	if r := resourcesOf(t, e, "envoryx-shop-php"); r.MemoryBytes != 0 || r.NanoCPUs != 1_500_000_000 {
		t.Fatalf("after lifting: %+v", r)
	}

	// A restart keeps them; a project that was never started gets them on its first start.
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if r := resourcesOf(t, e, "envoryx-shop-php"); r.NanoCPUs != 1_500_000_000 {
		t.Fatalf("after restart: %+v", r)
	}
}

// A JVM sizes its heap from the memory limit when it starts, so a running Java container
// gets a new one on a new memory limit; a CPU change, like elsewhere, is applied in place.
func TestMemoryLimitRecreatesRunningJVMContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, javaRequest("Spring", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "jobs", Preset: "java:jar", Arg: "target/worker.jar", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ids := func() (string, string, string) {
		j, _ := e.engine.Container("envoryx-spring-java")
		w, _ := e.engine.Container("envoryx-spring-worker-jobs")
		d, _ := e.engine.Container("envoryx-spring-database")
		return j.ID, w.ID, d.ID
	}
	java, worker, db := ids()
	if _, err := e.m.SetLimits(ctx, id, store.ResourceLimits{App: store.LimitSet{CPUs: 1}, Services: store.LimitSet{MemoryMB: 512}}); err != nil {
		t.Fatal(err)
	}
	if j, w, d := ids(); j != java || w != worker || d != db {
		t.Fatal("a CPU limit and the database's memory limit are applied in place")
	}
	if _, err := e.m.SetLimits(ctx, id, store.ResourceLimits{App: store.LimitSet{CPUs: 1, MemoryMB: 768}, Services: store.LimitSet{MemoryMB: 512}}); err != nil {
		t.Fatal(err)
	}
	j, w, d := ids()
	if j == java || w == worker {
		t.Fatal("the JVM containers must be recreated on a new memory limit")
	}
	if d != db {
		t.Fatal("the database keeps its container")
	}
	for _, name := range []string{"envoryx-spring-java", "envoryx-spring-worker-jobs"} {
		if c, _ := e.engine.Container(name); c.State != "running" {
			t.Fatalf("%s must run again: %s", name, c.State)
		}
		if r := resourcesOf(t, e, name); r.MemoryBytes != 768<<20 {
			t.Fatalf("%s: %+v", name, r)
		}
	}
}

func TestLimitsValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	for name, l := range map[string]store.ResourceLimits{
		"negative cpu":   {App: store.LimitSet{CPUs: -1}},
		"tiny cpu":       {App: store.LimitSet{CPUs: 0.05}},
		"tiny memory":    {Services: store.LimitSet{MemoryMB: 16}},
		"too many pids":  {Pids: 1 << 21},
		"too few pids":   {Pids: 10},
		"more than host": {App: store.LimitSet{CPUs: 4096}},
	} {
		if _, err := e.m.SetLimits(ctx, v.Project.ID, l); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

type oomRecorder struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *oomRecorder) Notify(_ context.Context, e notify.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}
func (r *oomRecorder) Clear(string) {}

func (r *oomRecorder) all() []notify.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Event(nil), r.events...)
}

func TestOOMKillsBecomeWarningsAndNotifications(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &oomRecorder{}
	e.m.SetNotifier(rec)
	req := phpRequest("Shop", true)
	req.Node = &NodeRequest{Version: "24", Config: runtime.NodeConfig{}}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetLimits(ctx, v.Project.ID, store.ResourceLimits{App: store.LimitSet{MemoryMB: 256}}); err != nil {
		t.Fatal(err)
	}
	go e.m.RunOOMWatcher(ctx, e.m.log)
	for e.engine.OOMWatchers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	e.engine.EmitOOM("envoryx-shop-node")
	e.engine.EmitOOM("envoryx-shop-node")
	deadline := time.Now().Add(2 * time.Second)
	var warnings []string
	for time.Now().Before(deadline) {
		view, _ := e.m.Get(ctx, v.Project.ID)
		warnings = view.Status.Warnings
		if len(warnings) > 0 && strings.Contains(warnings[len(warnings)-1], "2 times") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(warnings) == 0 || !strings.HasPrefix(warnings[len(warnings)-1], "node ran out of memory 2 times") || !strings.Contains(warnings[len(warnings)-1], "limit 256 MiB") {
		t.Fatalf("warnings: %v", warnings)
	}
	if events := rec.all(); len(events) != 2 || events[0].Kind != "project.oom" || !strings.Contains(events[0].Message, "limit 256 MiB") {
		t.Fatalf("notifications: %+v", events)
	}
}

// Some Docker versions deliver no "oom" event; the sampler then finds the kills in the
// container's counter. A kill both sources see is reported once.
func TestOOMKillsFromTheCounterWithoutEvents(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &oomRecorder{}
	e.m.SetNotifier(rec)
	v, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetLimits(ctx, v.Project.ID, store.ResourceLimits{App: store.LimitSet{MemoryMB: 64}}); err != nil {
		t.Fatal(err)
	}
	kills := func(n uint64) {
		e.engine.StatsByName = map[string]docker.Stats{"envoryx-shop-php": {MemoryBytes: 60 << 20, OOMKills: n}}
	}
	now := time.Now()
	kills(5) // older kills: only the baseline
	e.m.MetricsPass(ctx, now)
	kills(7)
	e.m.MetricsPass(ctx, now.Add(time.Minute))
	view, _ := e.m.Get(ctx, v.Project.ID)
	if w := view.Status.Warnings; len(w) != 1 || !strings.HasPrefix(w[0], "php ran out of memory 2 times") || !strings.Contains(w[0], "limit 64 MiB") {
		t.Fatalf("warnings: %v", w)
	}
	if n := len(rec.all()); n != 1 {
		t.Fatalf("%d notifications, want 1", n)
	}

	// An event first, then the counter: one kill. The counter first, then the event: one.
	go e.m.RunOOMWatcher(ctx, e.m.log)
	for e.engine.OOMWatchers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	e.engine.EmitOOM("envoryx-shop-php")
	deadline := time.Now().Add(2 * time.Second)
	for len(rec.all()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	kills(8)
	e.m.MetricsPass(ctx, now.Add(2*time.Minute))
	kills(9)
	e.m.MetricsPass(ctx, now.Add(3*time.Minute))
	e.engine.EmitOOM("envoryx-shop-php")
	time.Sleep(50 * time.Millisecond)
	view, _ = e.m.Get(ctx, v.Project.ID)
	if w := view.Status.Warnings; len(w) != 1 || !strings.HasPrefix(w[0], "php ran out of memory 4 times") {
		t.Fatalf("warnings: %v", w)
	}
	if n := len(rec.all()); n != 3 {
		t.Fatalf("%d notifications, want 3", n)
	}
}
