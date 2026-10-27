package project

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/envoryx/envoryx/internal/plan"
)

func TestNewProjectsPrivate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if e.m.NewProjectsPrivate(ctx) {
		t.Fatal("a machine of one's own starts projects open")
	}
	open, err := e.m.Create(ctx, phpRequest("Open", false))
	if err != nil || open.Project.ProxyRules.Login {
		t.Fatalf("open: %v %+v", err, open.Project.ProxyRules)
	}
	// On a hoster's machine they start behind the sign-in, until an admin decides.
	e.m.cfg.Managed = func() bool { return true }
	if !e.m.NewProjectsPrivate(ctx) {
		t.Fatal("a managed instance starts projects private")
	}
	priv, err := e.m.Create(ctx, phpRequest("Private", false))
	if err != nil || !priv.Project.ProxyRules.Login {
		t.Fatalf("private: %v %+v", err, priv.Project.ProxyRules)
	}
	if err := e.m.SetNewProjectsPrivate(ctx, false); err != nil || e.m.NewProjectsPrivate(ctx) {
		t.Fatalf("the admin's choice wins: %v", err)
	}
}

func TestDomainLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.m.SetLinks(func(context.Context) (string, int, int) { return "192.168.1.5", 80, 443 })
	v, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	// On a machine of one's own the links use the published port, which needs no DNS;
	// the containers keep the domain.
	direct := fmt.Sprintf("http://192.168.1.5:%d", v.Project.HTTPPort)
	if e.m.DomainLinks(ctx) || e.m.LinkURL(ctx, v.Project) != direct {
		t.Fatalf("direct link: %q", e.m.LinkURL(ctx, v.Project))
	}
	if e.m.ProjectURL(v.Project) != "https://shop.test" {
		t.Fatalf("project URL: %q", e.m.ProjectURL(v.Project))
	}
	if err := e.m.SetDomainLinks(ctx, true); err != nil || e.m.LinkURL(ctx, v.Project) != "https://shop.test" {
		t.Fatalf("domain link: %v %q", err, e.m.LinkURL(ctx, v.Project))
	}
	if err := e.m.SetDomainLinks(ctx, false); err != nil || e.m.LinkURL(ctx, v.Project) != direct {
		t.Fatalf("back to the port: %v", err)
	}
	// A hoster's instance opens projects by domain until an admin decides, and with ports
	// on the server itself the domain is the only way in.
	e.m.cfg.Managed = func() bool { return true }
	if e.m.DomainLinks(ctx) {
		t.Fatal("the admin's choice wins over the managed default")
	}
	e.m.cfg.LocalPorts = func() bool { return true }
	if !e.m.DomainLinks(ctx) || e.m.LinkURL(ctx, v.Project) != "https://shop.test" {
		t.Fatalf("local ports: %q", e.m.LinkURL(ctx, v.Project))
	}
}

func TestPlanWithoutPublicProjectsOrShares(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	e.m.shareHosts.Store(id, "random-words.trycloudflare.com")
	defer e.m.shareHosts.Delete(id)
	e.m.SetPlan(plan.Static(&plan.Plan{Disabled: []plan.Feature{plan.FeaturePublicProjects, plan.FeaturePublicShares}}))

	table, _ := e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["shop.test"]; r.Login != LoginApp {
		t.Fatalf("the plan puts every project behind the sign-in: %+v", r)
	}
	if _, ok := table.Routes["random-words.trycloudflare.com"]; ok {
		t.Fatal("a share must not be routed while the plan leaves shares out")
	}
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{}); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("opening the project: %v", err)
	}
	if _, err := e.m.SetProxyRules(ctx, id, ProxyRulesRequest{Login: true}); err != nil {
		t.Fatalf("keeping it closed: %v", err)
	}
	if _, err := e.m.StartShare(ctx, id, 0); !errors.Is(err, plan.ErrQuota) {
		t.Fatalf("share: %v", err)
	}
}
