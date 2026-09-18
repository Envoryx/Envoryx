package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestRouteTableAndDomains(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	shop, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	idle, err := e.m.Create(ctx, phpRequest("Idle", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddDomain(ctx, shop.Project.ID, "Shop.Example.COM"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "-x.test", "shop.test", "idle.test", "envoryx.test", "a b", "*.shop.test"} {
		if _, err := e.m.AddDomain(ctx, shop.Project.ID, bad); !errors.Is(err, validate.ErrInvalid) && !errors.Is(err, store.ErrConflict) {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
	if _, err := e.m.AddDomain(ctx, idle.Project.ID, "shop.example.com"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate hostname across projects must conflict, got %v", err)
	}

	table, err := e.m.RouteTable(ctx, ProxyOptions{HTTPSPort: 8443, EnvoryxURL: "https://envoryx.test:8443", ExtraUIHosts: []string{"192.168.1.10"}})
	if err != nil {
		t.Fatal(err)
	}
	if !table.UIHosts["envoryx.test"] || !table.UIHosts["192.168.1.10"] || table.HTTPSPort != 8443 {
		t.Fatalf("ui hosts: %+v", table)
	}
	r, ok := table.Routes["shop.test"]
	if !ok || !r.Running || r.Dial != "127.0.0.1:20000" || r.ProjectName != "Shop" {
		t.Fatalf("shop route (bare metal dial): %+v", r)
	}
	if r2, ok := table.Routes["shop.example.com"]; !ok || r2.ProjectID != shop.Project.ID {
		t.Fatalf("extra domain route: %+v", r2)
	}
	if r3 := table.Routes["idle.test"]; r3.Running {
		t.Fatalf("stopped project must not be running: %+v", r3)
	}

	hosts, _ := e.m.ProjectHostnames(ctx, shop.Project)
	if strings.Join(hosts, ",") != "shop.test,shop.example.com" {
		t.Fatalf("hostnames: %v", hosts)
	}
	if err := e.m.SetBaseDomain(ctx, "Dev.Local"); err != nil {
		t.Fatal(err)
	}
	hosts, _ = e.m.ProjectHostnames(ctx, shop.Project)
	if hosts[0] != "shop.dev.local" {
		t.Fatalf("base domain change must apply: %v", hosts)
	}
	if err := e.m.SetBaseDomain(ctx, "bad domain"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("invalid base domain must be rejected, got %v", err)
	}
}

func TestProxyAttachesToProjectNetworksInsideDocker(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	selfID := e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx", Labels: map[string]string{"x": "y"}}, "running")
	e.selfID = selfID

	view, err := e.m.Create(ctx, phpRequest("Web", true))
	if err != nil {
		t.Fatal(err)
	}
	nets, _ := e.engine.ContainerNetworks(ctx, selfID)
	if strings.Join(nets, ",") != "envoryx-web" {
		t.Fatalf("proxy must be attached to the project network: %v", nets)
	}
	table, _ := e.m.RouteTable(ctx, ProxyOptions{})
	if table.Routes["web.test"].Dial != "envoryx-web-web:80" {
		t.Fatalf("in-docker dial must use the container name: %+v", table.Routes["web.test"])
	}
	// Delete detaches first so the network can be removed.
	if err := e.m.Delete(ctx, view.Project.ID, DeleteOptions{Confirm: "web"}); err != nil {
		t.Fatal(err)
	}
	if len(e.engine.NetworkNames()) != 0 {
		t.Fatalf("network must be removed: %v", e.engine.NetworkNames())
	}
	nets, _ = e.engine.ContainerNetworks(ctx, selfID)
	if len(nets) != 0 {
		t.Fatalf("proxy must be detached: %v", nets)
	}
}
