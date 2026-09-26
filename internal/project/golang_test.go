package project

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// goRequest is a project without PHP whose Go server is the application.
func goRequest(name string, start bool) CreateRequest {
	return CreateRequest{
		Name:          name,
		Go:            &GoRequest{Version: "1.27", Config: runtime.GoConfig{Server: true, Package: "./cmd/server"}},
		CreateStarter: true,
		Start:         start,
	}
}

func TestGoServerServesProject(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	v, err := e.m.Create(ctx, goRequest("Svc", true))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-svc-go")
	if !ok {
		t.Fatal("go container missing")
	}
	// Behind the go.mod guard, air builds the package outside the project directory.
	if c.Spec.Cmd[0] != "sh" || !strings.Contains(c.Spec.Cmd[2], "[ -e go.mod ]") || !slices.Contains(c.Spec.Cmd, "go build -o /tmp/envoryx-go/app ./cmd/server") {
		t.Fatalf("go command: %q", c.Spec.Cmd)
	}
	if c.Spec.Image != "ghcr.io/envoryx/envoryx-go:1.27" || c.Spec.User != "1000:1000" || c.Spec.WorkingDir != "/var/www/html" {
		t.Fatalf("go container: %+v", c.Spec)
	}
	for _, want := range []string{"PORT=8080", "HOST=0.0.0.0", "GIN_MODE=debug", "GOPATH=/home/envoryx/go", "GOMODCACHE=/var/cache/envoryx/gomod", "GOCACHE=/var/cache/envoryx/gobuild"} {
		if !slices.Contains(c.Spec.Env, want) {
			t.Fatalf("go env lacks %s: %v", want, c.Spec.Env)
		}
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 8080 {
		t.Fatalf("server port: %+v", c.Spec.Ports)
	}
	web, _ := e.engine.Container("envoryx-svc-web")
	if len(web.Spec.Ports) != 0 {
		t.Fatalf("web port must stay unpublished: %+v", web.Spec.Ports)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "svc", "index.html")); err == nil {
		t.Fatal("no starter page while the Go server serves the project")
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	if Serves(proj) != "go" {
		t.Fatalf("Serves = %s", Serves(proj))
	}
	if kind, _ := AppKind(proj); kind != store.ServiceGo {
		t.Fatalf("AppKind = %s", kind)
	}
	// The proxy sends the project URL to the Go container.
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range table.Routes {
		if r.ProjectID == proj.ID && r.Dial == "envoryx-svc-go:8080" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no route to the Go server: %+v", table.Routes)
	}
	// SSH picks the Go container by <slug>.go.
	target, err := e.m.ResolveSSHUser(ctx, "svc.go")
	if err != nil || target.Kind != store.ServiceGo {
		t.Fatalf("ssh user: %+v %v", target, err)
	}
}

// Delve runs the server once debugging is on; switching it recreates the container, and
// the ports survive the edit.
func TestGoDebugAndUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, goRequest("Dbg", true))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.engine.Container("envoryx-dbg-go")
	hostPort := before.Spec.Ports[0].HostPort
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Go: &GoUpdate{Enabled: true, Version: "1.27", Config: runtime.GoConfig{Server: true, Package: "./cmd/server", Debug: true}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-dbg-go")
	if !slices.Contains(c.Spec.Cmd, "--build.full_bin") || len(c.Spec.Ports) != 2 || c.Spec.Ports[0].HostPort != hostPort || c.Spec.Ports[1].ContainerPort != runtime.DefaultDelvePort {
		t.Fatalf("debug: cmd %q ports %+v", c.Spec.Cmd, c.Spec.Ports)
	}
	// Removing Go takes the container.
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Go: &GoUpdate{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-dbg-go"); ok {
		t.Fatal("go container must be gone")
	}
}

func TestGoTemplateWorkersTestsAndManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := CreateRequest{Name: "Gin App", Go: &GoRequest{Version: "1.27"}, Template: "gin"}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, s := range e.engine.OneShots {
		if s.Image == "ghcr.io/envoryx/envoryx-go:1.27" {
			steps = append(steps, strings.Join(s.Cmd, " "))
		}
	}
	if len(steps) != 3 || !strings.Contains(steps[0], "go mod init app") || !strings.Contains(steps[1], "go get github.com/gin-gonic/gin") {
		t.Fatalf("template steps: %q", steps)
	}
	main, err := os.ReadFile(filepath.Join(e.projDir, "gin-app", "main.go"))
	if err != nil || !strings.Contains(string(main), "gin.Default()") {
		t.Fatalf("main.go: %v", err)
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	cfg, _ := goConfig(proj)
	if !cfg.Server || cfg.Package != "." || cfg.Port != runtime.DefaultGoPort {
		t.Fatalf("template merged into the config: %+v", cfg)
	}

	// A go run worker from the Go image.
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "consumer", Preset: "go:run", Arg: "cmd/consumer", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "bad", Preset: "go:run", Arg: "../escape", Enabled: true}); err == nil {
		t.Fatal("a package outside the module must be refused")
	}

	// go test via gotestsum once go.mod exists.
	_ = os.WriteFile(filepath.Join(e.projDir, "gin-app", "go.mod"), []byte("module app\n"), 0o644)
	suites, err := e.m.TestSuites(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var gotest *TestSuite
	for i := range suites {
		if suites[i].ID == "go" {
			gotest = &suites[i]
		}
	}
	if gotest == nil || gotest.Service != store.ServiceGo {
		t.Fatalf("go test suite: %+v", suites)
	}
	argv, _ := gotest.build("TestLogin", "/tmp/report.xml")
	if strings.Join(argv, " ") != "gotestsum --format testname --junitfile /tmp/report.xml -- ./... -run TestLogin" {
		t.Fatalf("go test argv: %q", argv)
	}

	// The manifest carries the Go section.
	mf, err := e.m.ExportManifest(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Go == nil || !mf.Go.Server || mf.Go.Version != "1.27" {
		t.Fatalf("manifest go: %+v", mf.Go)
	}
	req2 := manifestRequest(manifest.Manifest{Version: 1, Go: &manifest.Go{Version: "1.26", Server: true, Package: "./cmd/api", Port: 9000}}, "From Manifest")
	if req2.Go == nil || req2.Go.Config.Package != "./cmd/api" || req2.Go.Config.Port != 9000 {
		t.Fatalf("manifest import: %+v", req2.Go)
	}
}
