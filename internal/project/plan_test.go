package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/plan"
	"github.com/envoryx/envoryx/internal/runtime"
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

// A plan without end-of-life and preview versions refuses them for new projects, copies
// and version switches, keeps them where a project runs one already, and says so.
func TestPlanLeavesOutEOLAndPreviewVersions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	legacy := phpRequest("Legacy", false)
	legacy.PHP = &PHPRequest{Version: "8.1", Config: runtime.DefaultPHPConfig()}
	old, err := e.m.Create(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := e.m.Create(ctx, phpRequest("Current", false))
	if err != nil {
		t.Fatal(err)
	}
	e.m.SetPlan(plan.Static(&plan.Plan{Disabled: []plan.Feature{plan.FeatureEOLVersions, plan.FeaturePreviewVersions}}))

	again := phpRequest("Again", false)
	again.PHP = &PHPRequest{Version: "8.1", Config: runtime.DefaultPHPConfig()}
	if _, err := e.m.Create(ctx, again); !errors.Is(err, plan.ErrQuota) || !strings.Contains(err.Error(), "PHP 8.1 has reached its end of life") {
		t.Errorf("new project on 8.1: %v", err)
	}
	preview := phpRequest("Preview", false)
	preview.PHP = &PHPRequest{Version: "8.6", Config: runtime.DefaultPHPConfig()}
	if _, err := e.m.Create(ctx, preview); !errors.Is(err, plan.ErrQuota) || !strings.Contains(err.Error(), "preview version") {
		t.Errorf("new project on 8.6: %v", err)
	}
	if _, err := e.m.Duplicate(ctx, old.Project.ID, DuplicateRequest{Name: "Legacy copy"}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("copy of an 8.1 project: %v", err)
	}
	if _, err := e.m.Update(ctx, cur.Project.ID, UpdateRequest{PHP: &PHPUpdate{Enabled: true, Version: "8.1", Config: runtime.DefaultPHPConfig()}}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("switch to 8.1: %v", err)
	}

	// The project on 8.1 keeps it, changes its settings and may leave it.
	name := "Legacy shop"
	if _, err := e.m.Update(ctx, old.Project.ID, UpdateRequest{Name: &name, PHP: &PHPUpdate{Enabled: true, Version: "8.1", Config: runtime.DefaultPHPConfig()}}); err != nil {
		t.Errorf("an 8.1 project must keep its version: %v", err)
	}
	got, err := e.m.Update(ctx, old.Project.ID, UpdateRequest{PHP: &PHPUpdate{Enabled: true, Version: "8.4", Config: runtime.DefaultPHPConfig()}})
	if err != nil {
		t.Fatalf("leaving 8.1: %v", err)
	}
	if v := got.Project.Service(store.ServicePHP).Version; v != "8.4" {
		t.Fatalf("version %s", v)
	}
	if _, err := e.m.Update(ctx, old.Project.ID, UpdateRequest{PHP: &PHPUpdate{Enabled: true, Version: "8.1", Config: runtime.DefaultPHPConfig()}}); !errors.Is(err, plan.ErrQuota) {
		t.Errorf("going back to 8.1: %v", err)
	}

	// Allowed again, everything goes.
	e.m.SetPlan(plan.Static(&plan.Plan{}))
	if _, err := e.m.Create(ctx, again); err != nil {
		t.Errorf("without the switch: %v", err)
	}
}

// A project on a preview version the plan leaves out says so.
func TestPlanPreviewWarning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Preview", false)
	req.PHP = &PHPRequest{Version: "8.6", Config: runtime.DefaultPHPConfig()}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(v.Status.Warnings, "preview version") {
		t.Fatalf("no warning while the plan allows previews: %v", v.Status.Warnings)
	}
	e.m.SetPlan(plan.Static(&plan.Plan{Disabled: []plan.Feature{plan.FeaturePreviewVersions}}))
	got, _ := e.m.Get(ctx, v.Project.ID)
	if !hasWarning(got.Status.Warnings, "PHP 8.6 is a preview version, which this instance's plan doesn't include") {
		t.Fatalf("the project must say so: %v", got.Status.Warnings)
	}
}
