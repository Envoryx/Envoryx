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
	if got := strings.Join(c.Spec.Cmd, " "); got != "npm run dev -- --host 0.0.0.0 --port 5173 --strictPort" {
		t.Fatalf("dev server command: %s", got)
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 5173 || c.Spec.Ports[0].HostPort != 20001 {
		t.Fatalf("dev server port: %+v", c.Spec.Ports)
	}
	var sawAllowed bool
	for _, kv := range c.Spec.Env {
		if kv == "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=shop-dev.test" {
			sawAllowed = true
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
