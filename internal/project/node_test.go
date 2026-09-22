package project

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

func TestNodeDevServer(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	req := phpRequest("Shop", true)
	req.Node = &NodeRequest{Version: "24", Config: runtime.NodeConfig{DevServer: true, Preset: "vite", Script: "dev"}}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-shop-node")
	if !ok {
		t.Fatal("node container missing")
	}
	// Exact argv, no wait wrapper: PHP+Node containers keep their spec fingerprint so
	// existing projects are not recreated by the Node-only feature.
	if got := strings.Join(c.Spec.Cmd, " "); got != "npm run dev -- --host 0.0.0.0 --port 5173 --strictPort" {
		t.Fatalf("dev server command: %s", got)
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 5173 || c.Spec.Ports[0].HostPort != 20001 {
		t.Fatalf("dev server port: %+v", c.Spec.Ports)
	}
	// A single leading-dot entry (Vite < 8.3 takes the variable as one host): it
	// suffix-matches shop.test, shop-dev.test and every extra domain under the base.
	var sawAllowed bool
	for _, kv := range c.Spec.Env {
		if kv == "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.test" {
			sawAllowed = true
		}
		if strings.HasPrefix(kv, "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=") && strings.Contains(kv, ",") {
			t.Fatalf("vite allow-list must be a single host: %s", kv)
		}
	}
	if !sawAllowed {
		t.Fatalf("vite allowed host env missing: %v", c.Spec.Env)
	}

	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dev, ok := table.Routes["shop-dev.test"]
	if !ok || dev.Dial != "envoryx-shop-node:5173" || !dev.Running {
		t.Fatalf("dev route: %+v", dev)
	}
	// With PHP the primary host name keeps pointing at the web server.
	if r := table.Routes["shop.test"]; r.Dial != "envoryx-shop-web:80" {
		t.Fatalf("primary route with PHP: %+v", r)
	}

	// Switching the dev server off recreates the idle tooling container and frees the port.
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24"}}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-shop-node")
	if strings.Join(c.Spec.Cmd, " ") != "sleep infinity" || len(c.Spec.Ports) != 0 {
		t.Fatalf("after disabling: cmd=%v ports=%v", c.Spec.Cmd, c.Spec.Ports)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if _, ok := table.Routes["shop-dev.test"]; ok {
		t.Fatal("dev route must disappear")
	}

	// Enabling again with a different preset allocates a port and uses the preset's flags.
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24", Config: runtime.NodeConfig{DevServer: true, Preset: "next", Port: 3000, PackageManager: "pnpm"}}}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-shop-node")
	if got := strings.Join(c.Spec.Cmd, " "); got != "pnpm run dev -- -H 0.0.0.0 -p 3000" {
		t.Fatalf("next command: %s", got)
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	var cfg runtime.NodeConfig
	_ = json.Unmarshal(proj.Service(store.ServiceNode).Config, &cfg)
	if cfg.HostPort == 0 || c.Spec.Ports[0].HostPort != cfg.HostPort || c.Spec.Ports[0].ContainerPort != 3000 {
		t.Fatalf("port after re-enable: %+v cfg=%+v", c.Spec.Ports, cfg)
	}

	// Invalid input is rejected before anything changes.
	for _, bad := range []runtime.NodeConfig{{DevServer: true, Script: "dev; rm -rf /"}, {DevServer: true, PackageManager: "bun"}, {DevServer: true, Port: 80}, {DevServer: true, Preset: "angular"}} {
		if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24", Config: bad}}); err == nil {
			t.Fatalf("config %+v must be rejected", bad)
		}
	}
}

func TestNodeOnlyDevServerRoutes(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	v, err := e.m.Create(ctx, nodeRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	if _, err := e.m.AddDomain(ctx, id, "app.example.test"); err != nil {
		t.Fatal(err)
	}
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"shop.test", "shop-dev.test", "app.example.test"} {
		r, ok := table.Routes[host]
		if !ok || r.Dial != "envoryx-shop-node:5173" || !r.Running || r.ProjectID != id {
			t.Fatalf("%s must reach the dev server: %+v", host, r)
		}
	}
	// Running follows the node container, not the web container.
	e.engine.SetState("envoryx-shop-node", "exited")
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if table.Routes["shop.test"].Running || table.Routes["app.example.test"].Running {
		t.Fatalf("routes must report the node container's state: %+v", table.Routes["shop.test"])
	}
	e.engine.SetState("envoryx-shop-node", "running")

	// Bare metal dials the node host port instead of the container name.
	e.selfID = ""
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["shop.test"]; r.Dial != "127.0.0.1:20001" {
		t.Fatalf("bare-metal dial: %+v", r)
	}
	e.selfID = "envoryx-self"

	// Turning the dev server off hands the host name back to the web server, whose
	// container is recreated with the HTTP port published.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24"}}); err != nil {
		t.Fatal(err)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["shop.test"]; r.Dial != "envoryx-shop-web:80" || !r.Running {
		t.Fatalf("primary route after disabling the dev server: %+v", r)
	}
	if r := table.Routes["app.example.test"]; r.Dial != "envoryx-shop-web:80" {
		t.Fatalf("extra domain after disabling the dev server: %+v", r)
	}
	if _, ok := table.Routes["shop-dev.test"]; ok {
		t.Fatal("dev route must disappear")
	}
	web, _ := e.engine.Container("envoryx-shop-web")
	if len(web.Spec.Ports) != 1 || web.Spec.Ports[0].HostPort != 20000 || web.State != "running" {
		t.Fatalf("web container must publish the port again: %+v state=%s", web.Spec.Ports, web.State)
	}
	node, _ := e.engine.Container("envoryx-shop-node")
	if strings.Join(node.Spec.Cmd, " ") != "sleep infinity" {
		t.Fatalf("node container must idle: %v", node.Spec.Cmd)
	}
	proj, _ := e.m.loadProject(ctx, id)
	if Serves(proj) != "static" {
		t.Fatalf("Serves after disabling = %s", Serves(proj))
	}

	// And back on: the wrapper returns and the web port is withdrawn again.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24", Config: runtime.NodeConfig{DevServer: true, Preset: "next"}}}); err != nil {
		t.Fatal(err)
	}
	node, _ = e.engine.Container("envoryx-shop-node")
	if node.Spec.Cmd[0] != "sh" || strings.Join(node.Spec.Cmd[4:], " ") != "npm run dev -- -H 0.0.0.0 -p 3000" {
		t.Fatalf("node cmd after re-enable: %q", node.Spec.Cmd)
	}
	web, _ = e.engine.Container("envoryx-shop-web")
	if len(web.Spec.Ports) != 0 {
		t.Fatalf("web port must be withdrawn: %+v", web.Spec.Ports)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["shop.test"]; r.Dial != "envoryx-shop-node:3000" {
		t.Fatalf("primary route after re-enable: %+v", r)
	}
	// The web container is still part of the project although nothing routes to it: a
	// missing one is reported by Reconcile and recreated by Start (port still unpublished).
	if err := e.engine.RemoveContainer(ctx, web.ID); err != nil {
		t.Fatal(err)
	}
	report := e.m.Reconcile(ctx)
	if len(report.Issues) != 1 || !strings.Contains(report.Issues[0].Message, "observed partial") {
		t.Fatalf("reconcile issues: %+v", report.Issues)
	}
	if _, err := e.m.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	web, ok := e.engine.Container("envoryx-shop-web")
	if !ok || web.State != "running" || len(web.Spec.Ports) != 0 {
		t.Fatalf("start must recreate the web container without a published port: %+v", web)
	}
}
