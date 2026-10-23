package project

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

// A plan's services leave out what isn't listed: new projects and updates can't add it,
// a project that has it keeps it, and an external server doesn't count.
func TestPlanLimitsServices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	had := phpRequest("Old", false)
	had.Ollama = &ExtraRequest{}
	old, err := e.m.Create(ctx, had)
	if err != nil {
		t.Fatal(err)
	}
	e.m.SetPlan(plan.Static(&plan.Plan{Services: []string{"mariadb", "redis", "mailpit"}}))

	req := phpRequest("AI", false)
	req.Ollama = &ExtraRequest{}
	if _, err := e.m.Create(ctx, req); !errors.Is(err, plan.ErrQuota) || !strings.Contains(err.Error(), "Ollama is not available on this instance") {
		t.Errorf("new project with Ollama: %v", err)
	}
	pg := phpRequest("Pg", false)
	pg.Database = &DatabaseRequest{Type: "postgresql", Version: "17"}
	if _, err := e.m.Create(ctx, pg); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("new project on PostgreSQL: %v", err)
	}
	v, err := e.m.Create(ctx, shopRequest())
	if err != nil {
		t.Fatalf("MariaDB and Mailpit are in the plan: %v", err)
	}
	id := v.Project.ID
	on := true
	if _, err := e.m.Update(ctx, id, UpdateRequest{OpenSearch: &ExtraUpdate{Enabled: &on}}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("adding OpenSearch: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Storage: &StorageUpdate{Enabled: true}}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("adding storage: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Databases: map[string]DatabaseUpdate{"cache": {Enabled: true, Type: "mongodb"}}}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("adding MongoDB: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Redis: &ExtraUpdate{Enabled: &on}}); err != nil {
		t.Errorf("Redis is in the plan: %v", err)
	}
	// The project that had Ollama before the plan keeps it and can change other things.
	name := "Old one"
	if _, err := e.m.Update(ctx, old.Project.ID, UpdateRequest{Name: &name, Ollama: &ExtraUpdate{Enabled: &on}}); err != nil {
		t.Errorf("a project keeps the service it has: %v", err)
	}
}

// The plan caps how many projects run at once: a start or a create that would go beyond
// it is refused, stopping one makes room.
func TestPlanLimitsRunningProjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.m.SetPlan(plan.Static(&plan.Plan{Limits: plan.Limits{RunningProjects: 1}}))

	one, err := e.m.Create(ctx, phpRequest("One", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Create(ctx, phpRequest("Two", true)); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("second running project: %v", err)
	}
	two, err := e.m.Create(ctx, phpRequest("Two", false))
	if err != nil {
		t.Fatalf("a stopped project doesn't count: %v", err)
	}
	if _, err := e.m.Start(ctx, two.Project.ID); !errors.Is(err, plan.ErrQuota) || !strings.Contains(err.Error(), "at most 1 projects") {
		t.Fatalf("start beyond the limit: %v", err)
	}
	if _, err := e.m.Restart(ctx, one.Project.ID); err != nil {
		t.Fatalf("restarting the running one: %v", err)
	}
	if n, _ := e.m.RunningProjects(ctx); n != 1 {
		t.Fatalf("running = %d", n)
	}
	if _, err := e.m.Stop(ctx, one.Project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Start(ctx, two.Project.ID); err != nil {
		t.Fatalf("after a stop there is room: %v", err)
	}
}

// The plan's container limits cap what a project sets and fill in what it leaves open;
// a new plan reaches the running containers without a restart.
func TestPlanCapsContainerLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := plan.Static(&plan.Plan{})
	e.m.SetPlan(h)
	v, err := e.m.Create(ctx, shopRequest())
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	if r := resourcesOf(t, e, "envoryx-shop-php"); r.MemoryBytes != 0 || r.NanoCPUs != 0 {
		t.Fatalf("no limits without them in the plan: %+v", r)
	}

	h.Set(&plan.Plan{Limits: plan.Limits{Container: plan.ContainerLimits{CPUs: 2, MemoryMB: 1024}, ContainerDefault: plan.ContainerLimits{MemoryMB: 512}}})
	want := docker.Resources{NanoCPUs: 2_000_000_000, MemoryBytes: 512 << 20, PidsLimit: DefaultPidsLimit}
	deadline := time.Now().Add(5 * time.Second)
	for resourcesOf(t, e, "envoryx-shop-php") != want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r := resourcesOf(t, e, "envoryx-shop-php"); r != want {
		t.Fatalf("the plan's default and cap must reach the running container: %+v", r)
	}

	if _, err := e.m.SetLimits(ctx, id, store.ResourceLimits{App: store.LimitSet{MemoryMB: 2048}}); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("above the cap: %v", err)
	}
	if _, err := e.m.SetLimits(ctx, id, store.ResourceLimits{App: store.LimitSet{CPUs: 0.5, MemoryMB: 768}}); err != nil {
		t.Fatal(err)
	}
	if r := resourcesOf(t, e, "envoryx-shop-php"); r != (docker.Resources{NanoCPUs: 500_000_000, MemoryBytes: 768 << 20, PidsLimit: DefaultPidsLimit}) {
		t.Fatalf("a lower limit of the project wins: %+v", r)
	}
	if r := resourcesOf(t, e, "envoryx-shop-database"); r != want {
		t.Fatalf("the services keep the plan's: %+v", r)
	}
}
