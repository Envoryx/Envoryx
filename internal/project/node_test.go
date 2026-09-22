package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// Production mode: the node container builds and then serves; the inspector gets a host
// port of its own that survives edits and is published next to the dev-server port.
func TestNodeProductionModeAndInspector(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := nodeRequest("Shop", true)
	req.Node.Config = runtime.NodeConfig{DevServer: true, Preset: "next", Mode: "production", Inspect: true}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	var cfg runtime.NodeConfig
	if err := json.Unmarshal(view.Project.Service(store.ServiceNode).Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Script != "start" || cfg.BuildScript != "build" || cfg.InspectPort != 9229 || cfg.InspectHostPort == 0 || cfg.InspectHostPort == cfg.HostPort {
		t.Fatalf("stored config: %+v", cfg)
	}
	c, ok := e.engine.Container("envoryx-shop-node")
	if !ok {
		t.Fatal("node container missing")
	}
	cmd := strings.Join(c.Spec.Cmd, " ")
	// Blank project: the package.json guard wraps the production wrapper.
	if !strings.HasPrefix(cmd, "sh -c until [ -f package.json ]") || !strings.Contains(cmd, `npm run build && NODE_ENV=production exec "$@" envoryx-start npm run start -- -H 0.0.0.0 -p 3000`) {
		t.Fatalf("production command: %s", cmd)
	}
	for _, env := range c.Spec.Env {
		if env == "NODE_ENV=production" {
			t.Fatal("NODE_ENV=production belongs to the serve process only, not the container (npm install would skip devDependencies)")
		}
	}
	ports := map[int]int{}
	for _, p := range c.Spec.Ports {
		ports[p.ContainerPort] = p.HostPort
	}
	if ports[3000] != cfg.HostPort || ports[9229] != cfg.InspectHostPort {
		t.Fatalf("published ports: %v (config %+v)", ports, cfg)
	}

	// Turning the inspector off frees its port; turning it on again keeps the dev port.
	cfg.Inspect = false
	if _, err := e.m.Update(ctx, id, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24", Config: cfg}}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-shop-node")
	if len(c.Spec.Ports) != 1 {
		t.Fatalf("inspector port must be gone: %+v", c.Spec.Ports)
	}
	v, _ := e.m.Get(ctx, id)
	var after runtime.NodeConfig
	_ = json.Unmarshal(v.Project.Service(store.ServiceNode).Config, &after)
	if after.InspectHostPort != 0 || after.HostPort != cfg.HostPort {
		t.Fatalf("config after disabling the inspector: %+v", after)
	}

	// Back to a plain dev server: no wrapper for the build any more.
	after.Mode = "dev"
	after.Script = ""
	if _, err := e.m.Update(ctx, id, UpdateRequest{Node: &NodeUpdate{Enabled: true, Version: "24", Config: after}}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-shop-node")
	if cmd := strings.Join(c.Spec.Cmd, " "); strings.Contains(cmd, "run build") || !strings.Contains(cmd, "npm run dev -- -H 0.0.0.0 -p 3000") {
		t.Fatalf("dev command after switching back: %s", cmd)
	}
}

// PHP can join a project later and leave it again: the web server switches between static
// and FastCGI, the proxy between the dev server and the web container, the SPA fallback is
// dropped when PHP arrives and PHP workers pause while PHP is gone.
func TestAddAndRemovePHP(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := staticRequest("Site", true)
	spa := true
	req.Web = WebRequest{Type: "caddy", Version: "2", SPAFallback: &spa}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	caddy := func() string {
		b, err := os.ReadFile(filepath.Join(e.cfgDir, "projects", id, "web", "Caddyfile"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if cf := caddy(); !strings.Contains(cf, "try_files") || strings.Contains(cf, "php_fastcgi") {
		t.Fatalf("static Caddyfile: %s", cf)
	}

	cfg := runtime.DefaultPHPConfig()
	view, err = e.m.Update(ctx, id, UpdateRequest{PHP: &PHPUpdate{Enabled: true, Version: "8.4", Config: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Service(store.ServicePHP) == nil || Serves(view.Project) != "php" {
		t.Fatalf("PHP must be added and serve: %+v", view.Project.Services)
	}
	if _, ok := e.engine.Container("envoryx-site-php"); !ok {
		t.Fatal("php container must exist after adding PHP")
	}
	if cf := caddy(); strings.Contains(cf, "try_files") || !strings.Contains(cf, "php_fastcgi") {
		t.Fatalf("Caddyfile with PHP: %s", cf)
	}
	if wcfg, _ := webServiceConfig(*view.Project.Service(store.ServiceWeb)); wcfg.SPAFallback {
		t.Fatal("the SPA fallback must be dropped when PHP arrives")
	}
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "cron", Preset: "php:script", Arg: "bin/cron.php", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-site-worker-cron"); !ok {
		t.Fatal("worker container must run with PHP")
	}

	view, err = e.m.Update(ctx, id, UpdateRequest{PHP: &PHPUpdate{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Service(store.ServicePHP) != nil || Serves(view.Project) != "static" {
		t.Fatalf("PHP must be gone: %+v", view.Project.Services)
	}
	if _, ok := e.engine.Container("envoryx-site-php"); ok {
		t.Fatal("php container must be removed")
	}
	if _, ok := e.engine.Container("envoryx-site-worker-cron"); ok {
		t.Fatal("PHP worker container must pause without PHP")
	}
	if len(view.Project.Workers) != 1 {
		t.Fatal("the worker definition must survive – it comes back with PHP")
	}
	paused := false
	for _, svc := range view.Status.Services {
		if svc.Kind == "worker" && svc.State == "paused" {
			paused = true
		}
	}
	if !paused || len(view.Status.Warnings) != 0 {
		t.Fatalf("a PHP worker without PHP must be listed as paused, not warned about: %+v %v", view.Status.Services, view.Status.Warnings)
	}
	if cf := caddy(); strings.Contains(cf, "php_fastcgi") {
		t.Fatalf("Caddyfile after removing PHP: %s", cf)
	}
	if v, _ := e.m.Get(ctx, id); v.Status.State != StateRunning {
		t.Fatalf("project state after removing PHP: %s", v.Status.State)
	}
}
