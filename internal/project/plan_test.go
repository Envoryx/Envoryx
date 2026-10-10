package project

import (
	"context"
	"errors"
	"testing"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/store"
)

func TestPlanLimitsProjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.m.SetPlan(plan.Static(&plan.Plan{Limits: plan.Limits{Projects: 1}}))

	v, err := e.m.Create(ctx, phpRequest("One", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Create(ctx, phpRequest("Two", false)); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("second project: %v", err)
	}
	if _, err := e.m.Duplicate(ctx, v.Project.ID, DuplicateRequest{Name: "Copy"}); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("duplicate: %v", err)
	}
	if n, _ := e.store.Projects.Count(ctx); n != 1 {
		t.Fatalf("projects = %d", n)
	}
}

func TestPlanLimitsRuntimes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.m.SetPlan(plan.Static(&plan.Plan{Runtimes: []string{"php"}}))

	if _, err := e.m.Create(ctx, nodeRequest("Vite", false)); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("node project: %v", err)
	}
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24"}}); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("adding node: %v", err)
	}
	// A static site has no runtime and is always possible.
	if _, err := e.m.Create(ctx, staticRequest("Docs", false)); err != nil {
		t.Fatal(err)
	}
}

func TestPlanSwitchesFeaturesOff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	e.m.SetPlan(plan.Static(&plan.Plan{Disabled: plan.Features}))

	on := true
	if _, err := e.m.Update(ctx, id, UpdateRequest{IDEGateway: &on}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("IDE gateway: %v", err)
	}
	if _, err := e.m.SetCustomImage(ctx, id, store.ServicePHP, CustomImageRequest{Image: "example/php:1"}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("custom image: %v", err)
	}
	if _, err := e.m.SetBranchSettings(ctx, id, BranchSettingsRequest{Watch: true}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("branch environments: %v", err)
	}
	if _, err := e.m.CreateBranchEnvironment(ctx, id, "main"); err == nil {
		t.Error("branch environment created")
	}
	if _, err := e.m.InstallAddon(ctx, []byte("name: x")); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("addon: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Addons: map[string]AddonUpdate{"x": {Enabled: true}}}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("addon in project: %v", err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, External: &ExternalDatabase{}}}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("external database: %v", err)
	}
	// What the plan doesn't touch still works.
	name := "Shop 2"
	if _, err := e.m.Update(ctx, id, UpdateRequest{Name: &name}); err != nil {
		t.Errorf("rename: %v", err)
	}
	// An IDE gateway switched on before the plan doesn't forward any more.
	if err := e.store.Projects.SetIDEGateway(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	target, err := e.m.ResolveSSHUser(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if target.Gateway {
		t.Error("the plan's IDE gateway switch doesn't reach the SSH server")
	}
}

func TestPlanDiskLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	e.m.SetPlan(plan.Static(&plan.Plan{Limits: plan.Limits{DiskGB: 1}}))
	e.m.usage.diskBytes = 2 << 30

	if _, err := e.m.Create(ctx, phpRequest("Blog", false)); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("create over the disk limit: %v", err)
	}
	if _, err := e.m.CreateBackup(ctx, v.Project.ID, BackupOptions{Files: true}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("backup over the disk limit: %v", err)
	}
	e.m.usage.diskBytes = 1 << 20
	if _, err := e.m.Create(ctx, phpRequest("Blog", false)); err != nil {
		t.Errorf("create under the disk limit: %v", err)
	}
}
