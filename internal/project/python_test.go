package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// pythonRequest is a project without PHP whose Python application server is the
// application.
func pythonRequest(name string, start bool) CreateRequest {
	return CreateRequest{
		Name:          name,
		Python:        &PythonRequest{Version: "3.13", Config: runtime.PythonConfig{Server: true, Preset: "asgi", App: "main:app"}},
		CreateStarter: true,
		Start:         start,
	}
}

func TestPythonServerServesProject(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	v, err := e.m.Create(ctx, pythonRequest("Api", true))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	c, ok := e.engine.Container("envoryx-api-python")
	if !ok {
		t.Fatal("python container missing")
	}
	// The server runs behind the entry-file wait guard, as the project owner, with the
	// venv first on PATH and the server port published.
	if c.Spec.Cmd[0] != "sh" || !strings.Contains(c.Spec.Cmd[2], "[ -e main.py ] || [ -d main ]") || strings.Join(c.Spec.Cmd[4:], " ") != "uvicorn main:app --host 0.0.0.0 --port 8000 --reload" {
		t.Fatalf("python command: %q", c.Spec.Cmd)
	}
	if c.Spec.User != "1000:1000" || c.Spec.WorkingDir != "/var/www/html" {
		t.Fatalf("python container user/dir: %+v", c.Spec)
	}
	if !slices.Contains(c.Spec.Env, "VIRTUAL_ENV=/var/www/html/.venv") || !slices.Contains(c.Spec.Env, "PORT=8000") || !slices.Contains(c.Spec.Env, "HOME=/home/envoryx") {
		t.Fatalf("python env: %v", c.Spec.Env)
	}
	for _, kv := range c.Spec.Env {
		if strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "PATH=/var/www/html/.venv/bin:") {
			t.Fatalf("PATH must start with the venv: %s", kv)
		}
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 8000 || c.Spec.Ports[0].HostPort != 20001 {
		t.Fatalf("server port: %+v", c.Spec.Ports)
	}
	// The web container stays but its port is withdrawn; no starter page was written.
	web, _ := e.engine.Container("envoryx-api-web")
	if len(web.Spec.Ports) != 0 {
		t.Fatalf("web port must stay unpublished: %+v", web.Spec.Ports)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "api", "index.html")); err == nil {
		t.Fatal("no starter page while the Python server serves the project")
	}
	proj, _ := e.m.loadProject(ctx, id)
	if Serves(proj) != "python" {
		t.Fatalf("Serves = %s", Serves(proj))
	}
	if kind, _ := AppKind(proj); kind != store.ServicePython {
		t.Fatalf("AppKind = %s", kind)
	}
	img, _ := e.m.toolImage(proj)
	if img != "ghcr.io/envoryx/envoryx-python:3.13" {
		t.Fatalf("tool image: %s", img)
	}

	// Routing: the project URL and extra domains reach the Python container.
	if _, err := e.m.AddDomain(ctx, id, "api.example.test"); err != nil {
		t.Fatal(err)
	}
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"api.test", "api.example.test"} {
		r, ok := table.Routes[host]
		if !ok || r.Dial != "envoryx-api-python:8000" || !r.Running || r.ProjectID != id {
			t.Fatalf("%s must reach the Python server: %+v", host, r)
		}
	}
	if _, ok := table.Routes["api-dev.test"]; ok {
		t.Fatal("no dev route without a Node dev server")
	}
	e.engine.SetState("envoryx-api-python", "exited")
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if table.Routes["api.test"].Running {
		t.Fatal("route must report the python container's state")
	}
	e.engine.SetState("envoryx-api-python", "running")
	e.selfID = ""
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["api.test"]; r.Dial != "127.0.0.1:20001" {
		t.Fatalf("bare-metal dial: %+v", r)
	}
	e.selfID = "envoryx-self"

	// SSH: the bare slug lands in the Python container, ".python" selects it explicitly.
	for _, user := range []string{"api", "api.python"} {
		target, err := e.m.ResolveSSHUser(ctx, user)
		if err != nil || target.Kind != store.ServicePython {
			t.Fatalf("ssh user %s: %+v %v", user, target, err)
		}
	}
	if _, err := e.m.ResolveSSHUser(ctx, "api.php"); err == nil {
		t.Fatal("api.php must fail without PHP")
	}

	// Production mode with debugpy: the command loses --reload, a second port is published
	// and the server port is kept across the edit.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Python: &PythonUpdate{Enabled: true, Version: "3.13", Config: runtime.PythonConfig{Server: true, Preset: "asgi", App: "main:app", Mode: "production", Debug: true}}}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-api-python")
	if strings.Join(c.Spec.Cmd[4:], " ") != "uvicorn main:app --host 0.0.0.0 --port 8000" {
		t.Fatalf("production command: %q", c.Spec.Cmd)
	}
	proj, _ = e.m.loadProject(ctx, id)
	var cfg runtime.PythonConfig
	_ = json.Unmarshal(proj.Service(store.ServicePython).Config, &cfg)
	if cfg.HostPort != 20001 || cfg.DebugHostPort == 0 || cfg.DebugPort != 5678 {
		t.Fatalf("ports after edit: %+v", cfg)
	}
	if len(c.Spec.Ports) != 2 || c.Spec.Ports[1].ContainerPort != 5678 || c.Spec.Ports[1].HostPort != cfg.DebugHostPort {
		t.Fatalf("debugpy port: %+v", c.Spec.Ports)
	}

	// Server off: the container idles, the web port returns, the project is static.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Python: &PythonUpdate{Enabled: true, Version: "3.13"}}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-api-python")
	if strings.Join(c.Spec.Cmd, " ") != "sleep infinity" || len(c.Spec.Ports) != 0 {
		t.Fatalf("after disabling the server: cmd=%v ports=%v", c.Spec.Cmd, c.Spec.Ports)
	}
	web, _ = e.engine.Container("envoryx-api-web")
	if len(web.Spec.Ports) != 1 || web.Spec.Ports[0].HostPort != 20000 {
		t.Fatalf("web port must be published again: %+v", web.Spec.Ports)
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["api.test"]; r.Dial != "envoryx-api-web:80" {
		t.Fatalf("route after disabling the server: %+v", r)
	}
	proj, _ = e.m.loadProject(ctx, id)
	if Serves(proj) != "static" {
		t.Fatalf("Serves after disabling = %s", Serves(proj))
	}

	// Removing Python removes its container and its workers' containers; the worker
	// definition survives and is listed as paused.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Python: &PythonUpdate{Enabled: true, Version: "3.13", Config: runtime.PythonConfig{Server: true, Preset: "asgi"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "tasks", Preset: "celery:worker", Arg: "config", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	w, ok := e.engine.Container("envoryx-api-worker-tasks")
	if !ok || strings.Join(w.Spec.Cmd, " ") != "celery -A config worker --loglevel=info" || w.Spec.Image != "ghcr.io/envoryx/envoryx-python:3.13" {
		t.Fatalf("celery worker: %+v", w.Spec)
	}
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "laravel:queue", Enabled: true}); err == nil {
		t.Fatal("a PHP preset must be refused without PHP")
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Python: &PythonUpdate{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-api-python"); ok {
		t.Fatal("python container must be removed")
	}
	if _, ok := e.engine.Container("envoryx-api-worker-tasks"); ok {
		t.Fatal("python worker container must be removed with the runtime")
	}
	view, _ := e.m.Get(ctx, id)
	var paused bool
	for _, s := range view.Status.Services {
		if s.WorkerID != "" && s.State == "paused" {
			paused = true
		}
	}
	if !paused || len(view.Project.Workers) != 1 {
		t.Fatalf("worker must survive as paused: %+v", view.Status.Services)
	}
	if _, err := e.m.ResolveSSHUser(ctx, "api"); err == nil {
		t.Fatal("no application container after removing Python")
	}

	// Invalid input is rejected before anything changes.
	for _, bad := range []runtime.PythonConfig{{Server: true, App: "main:app; id"}, {Server: true, Preset: "rails"}, {Server: true, Port: 80}, {Server: true, Mode: "test"}} {
		if _, err := e.m.Update(ctx, id, UpdateRequest{Python: &PythonUpdate{Enabled: true, Version: "3.13", Config: bad}}); err == nil {
			t.Fatalf("config %+v must be rejected", bad)
		}
	}
}

func TestPythonWithNodeFrontend(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	req := pythonRequest("Shop", true)
	req.Python.Config = runtime.PythonConfig{Server: true, Preset: "django"}
	req.Node = &NodeRequest{Version: "24", Config: runtime.NodeConfig{DevServer: true, Preset: "vite"}}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	if Serves(proj) != "python" {
		t.Fatalf("Serves = %s", Serves(proj))
	}
	table, _ := e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["shop.test"]; r.Dial != "envoryx-shop-python:8000" {
		t.Fatalf("project URL must reach Django: %+v", r)
	}
	if r := table.Routes["shop-dev.test"]; r.Dial != "envoryx-shop-node:5173" {
		t.Fatalf("the Vite dev server keeps its route: %+v", r)
	}
	// Both application containers publish a port; the web container does not.
	py, _ := e.engine.Container("envoryx-shop-python")
	node, _ := e.engine.Container("envoryx-shop-node")
	web, _ := e.engine.Container("envoryx-shop-web")
	if len(py.Spec.Ports) != 1 || len(node.Spec.Ports) != 1 || len(web.Spec.Ports) != 0 || py.Spec.Ports[0].HostPort == node.Spec.Ports[0].HostPort {
		t.Fatalf("ports: py=%+v node=%+v web=%+v", py.Spec.Ports, node.Spec.Ports, web.Spec.Ports)
	}
	// The Node container keeps the plain dev command: only the container that answers the
	// project URL gets the wait guard.
	if node.Spec.Cmd[0] != "npm" {
		t.Fatalf("node command: %q", node.Spec.Cmd)
	}
	if strings.Join(py.Spec.Cmd[4:], " ") != "python manage.py runserver 0.0.0.0:8000" {
		t.Fatalf("django command: %q", py.Spec.Cmd)
	}

	// Adding PHP makes PHP the application: the Python server keeps running on its host
	// port but the routes go to the web server.
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{PHP: &PHPUpdate{Enabled: true, Version: "8.4"}}); err != nil {
		t.Fatal(err)
	}
	proj, _ = e.m.loadProject(ctx, v.Project.ID)
	if Serves(proj) != "php" {
		t.Fatalf("Serves with PHP = %s", Serves(proj))
	}
	table, _ = e.m.RouteTable(ctx, ProxyOptions{})
	if r := table.Routes["shop.test"]; r.Dial != "envoryx-shop-web:80" {
		t.Fatalf("with PHP the web server serves: %+v", r)
	}
	py, _ = e.engine.Container("envoryx-shop-python")
	if py.Spec.Cmd[0] != "python" || len(py.Spec.Ports) != 1 {
		t.Fatalf("python container with PHP: cmd=%q ports=%+v", py.Spec.Cmd, py.Spec.Ports)
	}
}

func TestPythonTemplateMergesServerDefaults(t *testing.T) {
	e := newEnv(t)
	req := CreateRequest{Name: "Blog", Template: "django", Python: &PythonRequest{Version: "3.13"}, Database: &DatabaseRequest{Type: "postgresql"}}
	p, err := e.m.buildProject(req)
	if err != nil {
		t.Fatal(err)
	}
	var cfg runtime.PythonConfig
	_ = json.Unmarshal(p.Service(store.ServicePython).Config, &cfg)
	if !cfg.Server || cfg.Preset != "django" || cfg.Port != 8000 || cfg.App != "config.wsgi:application" {
		t.Fatalf("template defaults not merged: %+v", cfg)
	}
	// A request that configured the server keeps its own values.
	req.Python.Config = runtime.PythonConfig{Server: true, Preset: "wsgi", App: "config.wsgi:app", Port: 9000}
	p, _ = e.m.buildProject(req)
	_ = json.Unmarshal(p.Service(store.ServicePython).Config, &cfg)
	if cfg.Preset != "wsgi" || cfg.Port != 9000 || cfg.App != "config.wsgi:app" {
		t.Fatalf("request values must win: %+v", cfg)
	}
	if _, err := e.m.buildProject(CreateRequest{Name: "Blog", Template: "flask"}); err == nil {
		t.Fatal("a Python template needs the Python runtime")
	}
	if _, err := e.m.buildProject(CreateRequest{Name: "Blog", Template: "flask", Python: &PythonRequest{Version: "3.13"}, PHP: &PHPRequest{Version: "8.4"}}); err != nil {
		t.Fatalf("Python template next to PHP is allowed (Python becomes tooling): %v", err)
	}
}

// A Python container without an application server still publishes debugpy: the process a
// developer steps through is usually one they start themselves in the terminal.
func TestDebugpyPortWithoutApplicationServer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	req := phpRequest("Tools", true)
	req.Python = &PythonRequest{Version: "3.13", Config: runtime.PythonConfig{Server: false, Debug: true}}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := pythonConfig(v.Project)
	if !ok || cfg.Server || !cfg.Debug || cfg.DebugPort != runtime.DefaultDebugpyPort || cfg.DebugHostPort == 0 {
		t.Fatalf("python config: %+v", cfg)
	}
	py, _ := e.engine.Container("envoryx-tools-python")
	if len(py.Spec.Cmd) == 0 || py.Spec.Cmd[0] != "sleep" {
		t.Fatalf("tooling container must idle: %v", py.Spec.Cmd)
	}
	if len(py.Spec.Ports) != 1 || py.Spec.Ports[0].ContainerPort != runtime.DefaultDebugpyPort || py.Spec.Ports[0].HostPort != cfg.DebugHostPort {
		t.Fatalf("debugpy port: %+v", py.Spec.Ports)
	}

	// Switching it off frees the port again.
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Python: &PythonUpdate{Enabled: true, Version: "3.13", Config: runtime.PythonConfig{Server: false, Debug: false}}}); err != nil {
		t.Fatal(err)
	}
	after, err := e.m.Get(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = pythonConfig(after.Project)
	if cfg.Debug || cfg.DebugHostPort != 0 {
		t.Fatalf("debug off must release the port: %+v", cfg)
	}
	py, _ = e.engine.Container("envoryx-tools-python")
	if len(py.Spec.Ports) != 0 {
		t.Fatalf("no ports expected: %+v", py.Spec.Ports)
	}
}
