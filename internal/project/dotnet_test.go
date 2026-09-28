package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// dotnetRequest is a project without PHP whose ASP.NET Core server is the application, on
// PostgreSQL.
func dotnetRequest(name string, start bool) CreateRequest {
	return CreateRequest{
		Name:          name,
		Dotnet:        &DotnetRequest{Version: "10", Config: runtime.DotnetConfig{Server: true}},
		Database:      &DatabaseRequest{Type: "postgresql"},
		CreateStarter: true,
		Start:         start,
	}
}

func TestDotnetServerServesProject(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	v, err := e.m.Create(ctx, dotnetRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-shop-dotnet")
	if !ok {
		t.Fatal("dotnet container missing")
	}
	// Behind the project-file wait and the database wait, dotnet watch in dev mode.
	if c.Spec.Cmd[0] != "sh" || !strings.Contains(c.Spec.Cmd[2], "*.csproj") || !slices.Contains(c.Spec.Cmd, "envoryx-dotnet") || !slices.Contains(c.Spec.Cmd, "aspnetcore") {
		t.Fatalf("dotnet command: %q", c.Spec.Cmd)
	}
	if c.Spec.Image != "ghcr.io/envoryx/envoryx-dotnet:10" || c.Spec.User != "1000:1000" || c.Spec.WorkingDir != "/var/www/html" {
		t.Fatalf("dotnet container: %+v", c.Spec)
	}
	env := c.Spec.Env
	cs := envValue(env, "ConnectionStrings__DefaultConnection")
	want := "Host=database;Port=5432;Database=" + envValue(env, "DB_DATABASE") + ";Username=" + envValue(env, "DB_USERNAME") + ";Password=" + envValue(env, "DB_PASSWORD")
	if envValue(env, "DB_PASSWORD") == "" || cs != want {
		t.Fatalf("connection string %q, want %q", cs, want)
	}
	for _, want := range []string{"ASPNETCORE_HTTP_PORTS=", "ASPNETCORE_ENVIRONMENT=Development", "NUGET_PACKAGES=/var/cache/envoryx/nuget/packages", "HOME=/home/envoryx"} {
		if !slices.Contains(env, want) {
			t.Fatalf("dotnet env lacks %s: %v", want, env)
		}
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 8080 {
		t.Fatalf("server port: %+v", c.Spec.Ports)
	}
	web, _ := e.engine.Container("envoryx-shop-web")
	if len(web.Spec.Ports) != 0 {
		t.Fatalf("web port must stay unpublished: %+v", web.Spec.Ports)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop", "index.html")); err == nil {
		t.Fatal("no starter page while the .NET server serves the project")
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	if Serves(proj) != "dotnet" {
		t.Fatalf("Serves = %s", Serves(proj))
	}
	if kind, _ := AppKind(proj); kind != store.ServiceDotnet {
		t.Fatalf("AppKind = %s", kind)
	}
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range table.Routes {
		if r.ProjectID == proj.ID && r.Dial == "envoryx-shop-dotnet:8080" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no route to the .NET server: %+v", table.Routes)
	}
	target, err := e.m.ResolveSSHUser(ctx, "shop.dotnet")
	if err != nil || target.Kind != store.ServiceDotnet {
		t.Fatalf("ssh user: %+v %v", target, err)
	}
	if target, _ := e.m.ResolveSSHUser(ctx, "shop"); target.Kind != store.ServiceDotnet {
		t.Fatalf("the bare user lands in the .NET container: %+v", target)
	}
}

// An additional database gets ConnectionStrings__<name> in the MySQL form, MongoDB its
// URI, Redis StackExchange's host:port form; values with separators are quoted.
func TestDotnetEnvForOtherDatabases(t *testing.T) {
	proj := store.Project{Services: []store.ProjectService{
		{Kind: store.ServiceDatabase, Variant: "mongodb", Enabled: true},
		{Kind: store.DatabaseKind("reports"), Variant: "mariadb", Enabled: true},
	}}
	env := dotnetEnv(proj, []string{
		"MONGODB_URI=mongodb://u:p@database:27017/shop?authSource=admin",
		"REPORTS_DB_HOST=reports", "REPORTS_DB_PORT=3306", "REPORTS_DB_DATABASE=reports", "REPORTS_DB_USERNAME=rep", `REPORTS_DB_PASSWORD=a;b"c`,
		"REDIS_HOST=cache.lan", "REDIS_PORT=6380", "REDIS_PASSWORD=s3cret",
	})
	for key, want := range map[string]string{
		"ConnectionStrings__MongoDB": "mongodb://u:p@database:27017/shop?authSource=admin",
		"ConnectionStrings__reports": `Server=reports;Port=3306;Database=reports;User ID=rep;Password="a;b""c"`,
		"ConnectionStrings__Redis":   "cache.lan:6380,password=s3cret",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if envValue(env, "ConnectionStrings__DefaultConnection") != "" {
		t.Fatal("MongoDB and an additional database don't become DefaultConnection")
	}
}

// Switching the preset recreates the container and keeps the server's host port; turning
// the server off unpublishes it; removing the runtime removes the container.
func TestDotnetUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, dotnetRequest("Upd", true))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.engine.Container("envoryx-upd-dotnet")
	hostPort := before.Spec.Ports[0].HostPort
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Dotnet: &DotnetUpdate{Enabled: true, Version: "8", Config: runtime.DotnetConfig{Server: true, Preset: "dll", Mode: "production", Project: "src/Worker/Worker.csproj"}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-upd-dotnet")
	if c.Spec.Image != "ghcr.io/envoryx/envoryx-dotnet:8" || !slices.Contains(c.Spec.Cmd, "dll") || !slices.Contains(c.Spec.Cmd, "src/Worker/Worker.csproj") || len(c.Spec.Ports) != 1 || c.Spec.Ports[0].HostPort != hostPort || !slices.Contains(c.Spec.Env, "ASPNETCORE_ENVIRONMENT=Production") {
		t.Fatalf("updated: image %s cmd %q ports %+v", c.Spec.Image, c.Spec.Cmd, c.Spec.Ports)
	}
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Dotnet: &DotnetUpdate{Enabled: true, Version: "8"}}); err != nil {
		t.Fatal(err)
	}
	c, _ = e.engine.Container("envoryx-upd-dotnet")
	if !slices.Equal(c.Spec.Cmd, []string{"sleep", "infinity"}) || len(c.Spec.Ports) != 0 {
		t.Fatalf("tooling container: cmd %q ports %+v", c.Spec.Cmd, c.Spec.Ports)
	}
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Dotnet: &DotnetUpdate{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-upd-dotnet"); ok {
		t.Fatal("dotnet container must be gone")
	}
}

func TestDotnetTemplateWorkersTestsAndManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := dotnetRequest("My Shop", false)
	req.Dotnet.Config = runtime.DotnetConfig{}
	req.Template = "aspnet-webapi"
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var steps [][]string
	for _, s := range e.engine.OneShots {
		if s.Image == "ghcr.io/envoryx/envoryx-dotnet:10" {
			steps = append(steps, s.Cmd)
		}
	}
	if len(steps) != 3 || !slices.Equal(steps[0], []string{"dotnet", "new", "webapi", "--name", "my-shop", "--output", ".", "--no-https"}) ||
		steps[1][2] != dotnetEFCoreScript || !slices.Equal(steps[1][4:], []string{"10", "Npgsql.EntityFrameworkCore.PostgreSQL", "UseNpgsql"}) ||
		!slices.Equal(steps[2], dotnetPrebuild) {
		t.Fatalf("template steps: %q", steps)
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	cfg, _ := dotnetConfig(proj)
	if !cfg.Server || cfg.Preset != "aspnetcore" || cfg.Port != 8080 {
		t.Fatalf("template merged into the config: %+v", cfg)
	}
	// Without a SQL database the EF Core step has nothing to add.
	if argv := dotnetEFCore(store.Project{}); argv[5] != "" || argv[6] != "" {
		t.Fatalf("EF Core without a database: %q", argv)
	}

	// A project worker and a DLL worker from the .NET image, with the connection string.
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "jobs", Preset: "dotnet:project", Arg: "./src/Jobs/Jobs.csproj", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	w, ok := e.engine.Container("envoryx-my-shop-worker-jobs")
	if !ok || w.Spec.Image != "ghcr.io/envoryx/envoryx-dotnet:10" || w.Spec.Cmd[2] != dotnetWorkerScript || w.Spec.Cmd[4] != "src/Jobs/Jobs.csproj" || envValue(w.Spec.Env, "ConnectionStrings__DefaultConnection") == "" {
		t.Fatalf("dotnet worker: %+v", w.Spec)
	}
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "tool", Preset: "dotnet:dll", Arg: "tools/Tool.dll", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	w, _ = e.engine.Container("envoryx-my-shop-worker-tool")
	if !slices.Equal(w.Spec.Cmd, []string{"dotnet", "tools/Tool.dll"}) {
		t.Fatalf("dll worker: %q", w.Spec.Cmd)
	}
	for _, bad := range []WorkerRequest{
		{Name: "bad1", Preset: "dotnet:project", Arg: "App.csproj; rm -rf /", Enabled: true},
		{Name: "bad2", Preset: "dotnet:project", Arg: "App.sln", Enabled: true},
		{Name: "bad3", Preset: "dotnet:dll", Arg: "../elsewhere.dll", Enabled: true},
	} {
		if _, err := e.m.AddWorker(ctx, v.Project.ID, bad); err == nil {
			t.Fatalf("%+v must be refused", bad)
		}
	}

	// dotnet test once there's a test project; the solution at the top runs them all.
	dir := filepath.Join(e.projDir, "my-shop")
	_ = os.MkdirAll(filepath.Join(dir, "tests", "Shop.Tests"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "tests", "Shop.Tests", "Shop.Tests.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="xunit" Version="2.9.3" /></ItemGroup></Project>`), 0o644)
	suites, err := e.m.TestSuites(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var suite TestSuite
	for _, s := range suites {
		if s.ID == "dotnet" {
			suite = s
		}
	}
	if suite.Service != store.ServiceDotnet || !suite.Report || !suite.trx || !slices.Equal(suite.Cmd, []string{"dotnet", "test", "tests/Shop.Tests/Shop.Tests.csproj"}) {
		t.Fatalf("dotnet suites: %+v", suites)
	}
	argv, _ := suite.build("FullyQualifiedName~Orders", "/tmp/report.xml")
	if argv[2] != dotnetTestScript || !slices.Equal(argv[4:], []string{"vstest", "/tmp/report.xml", "FullyQualifiedName~Orders", "tests/Shop.Tests/Shop.Tests.csproj"}) {
		t.Fatalf("dotnet test argv %q", argv)
	}

	mf, err := e.m.ExportManifest(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Dotnet == nil || !mf.Dotnet.Server || mf.Dotnet.Version != "10" || mf.Dotnet.Preset != "aspnetcore" {
		t.Fatalf("manifest dotnet: %+v", mf.Dotnet)
	}
	req2 := manifestRequest(manifest.Manifest{Version: 1, Dotnet: &manifest.Dotnet{Version: "8", Server: true, Preset: "dll", Project: "src/Worker/Worker.csproj", DLL: "out/Worker.dll", Port: 9000}}, "From Manifest")
	if req2.Dotnet == nil || req2.Dotnet.Config.Preset != "dll" || req2.Dotnet.Config.Project != "src/Worker/Worker.csproj" || req2.Dotnet.Config.DLL != "out/Worker.dll" || req2.Dotnet.Config.Port != 9000 {
		t.Fatalf("manifest import: %+v", req2.Dotnet)
	}
}

func TestDotnetTestSuiteDetection(t *testing.T) {
	project := func(files map[string]string) string {
		dir := t.TempDir()
		for f, content := range files {
			p := filepath.Join(dir, filepath.FromSlash(f))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(content), 0o644)
		}
		return dir
	}
	test := `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.14.1" /></ItemGroup></Project>`
	if _, ok := dotnetTestSuite(project(map[string]string{"App.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web"/>`}), nil); ok {
		t.Fatal("no test project, no suite")
	}
	s, ok := dotnetTestSuite(project(map[string]string{"Shop.slnx": "", "tests/A/A.csproj": test, "tests/B/B.csproj": test}), nil)
	if !ok || !s.Available || !slices.Equal(s.Cmd, []string{"dotnet", "test"}) {
		t.Fatalf("a solution runs every test project: %+v", s)
	}
	s, ok = dotnetTestSuite(project(map[string]string{"tests/A/A.csproj": test, "tests/B/B.csproj": test}), nil)
	if !ok || s.Available || !strings.Contains(s.Reason, "no solution file") {
		t.Fatalf("two test projects without a solution: %+v", s)
	}
	// Test projects in build output don't count.
	if _, ok := dotnetTestSuite(project(map[string]string{"App.csproj": "", "bin/Debug/Copy.csproj": test}), nil); ok {
		t.Fatal("bin/ is skipped")
	}
	s, ok = dotnetTestSuite(project(map[string]string{"Shop.sln": "", "Shop.Tests/Shop.Tests.csproj": test}), []byte(`{"test":{"runner":"Microsoft.Testing.Platform"}}`))
	if !ok || s.Report || s.FilterHint != "" {
		t.Fatalf("Microsoft.Testing.Platform runs without report and filter: %+v", s)
	}
	argv, _ := s.build("ignored", "/tmp/r.xml")
	if !slices.Equal(argv[4:], []string{"mtp", "/tmp/r.xml", "", ""}) {
		t.Fatalf("mtp argv %q", argv)
	}
}

// TestDotnetTestScript runs the test script against a stand-in dotnet: the primary
// connection string points at <database>_test, and the TRX files of two test projects
// (one with a byte order mark) end up in one report.
func TestDotnetTestScript(t *testing.T) {
	bin, dir := t.TempDir(), t.TempDir()
	stub := `#!/bin/sh
out=; while [ $# -gt 0 ]; do [ "$1" = --results-directory ] && out=$2; printf '%s ' "$1"; shift; done
printf '|%s\n' "$ConnectionStrings__DefaultConnection"
mkdir -p "$out"
printf '\357\273\277<?xml version="1.0" encoding="utf-8"?>\n<TestRun><Results><UnitTestResult testId="1" testName="A.Pass" outcome="Passed" duration="00:00:00.5000000"/></Results></TestRun>\n' > "$out/a.trx"
printf '<?xml version="1.0" encoding="utf-8"?>\n<TestRun><Results><UnitTestResult testId="2" testName="B.Fail" outcome="Failed" duration="00:00:01.2500000"/></Results></TestRun>\n' > "$out/b.trx"
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "dotnet"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report.xml")
	cmd := exec.Command("sh", "-c", dotnetTestScript, "envoryx-dotnet-test", "vstest", report, "FullyQualifiedName~B", "")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "ConnectionStrings__DefaultConnection=Host=database;Port=5432;Database=shop;Username=shop;Password=pw"}
	out, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("the test run's exit code must come through: %v", err)
	}
	if want := "test --filter FullyQualifiedName~B --logger trx --logger console;verbosity=normal --results-directory /tmp/envoryx-trx |Host=database;Port=5432;Database=shop_test;Username=shop;Password=pw\n"; string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	merged, _ := os.ReadFile(report)
	res, err := parseTRX(strings.NewReader(string(merged)), dir)
	if err != nil || strings.Contains(string(merged), "<?xml") || res.Tests != 2 || res.Failures != 1 || res.Seconds != 1.75 {
		t.Fatalf("merged report %q: %+v %v", merged, res, err)
	}
}

func TestParseTRX(t *testing.T) {
	trx := `<trx><TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010">
  <Results>
    <UnitTestResult testId="t1" testName="Shop.Tests.OrderTests.Pays" outcome="Passed" duration="00:00:00.0100000" />
    <UnitTestResult testId="t2" testName="Shop.Tests.OrderTests.Ships(count: 2)" outcome="Failed" duration="00:00:00.0200000">
      <Output><ErrorInfo>
        <Message>Assert.Equal() Failure: Values differ
Expected: 2
Actual:   3</Message>
        <StackTrace>   at Shop.Tests.OrderTests.Ships(Int32 count) in /var/www/html/tests/OrderTests.cs:line 21</StackTrace>
      </ErrorInfo></Output>
    </UnitTestResult>
    <UnitTestResult testId="t3" testName="Shop.Tests.OrderTests.Later" outcome="NotExecuted" duration="00:00:00" />
    <UnitTestResult testId="t4" testName="Shop.Tests.Hangs" outcome="Timeout" duration="00:01:00" />
  </Results>
  <TestDefinitions>
    <UnitTest id="t2" name="Ships(count: 2)"><TestMethod className="Shop.Tests.OrderTests" name="Ships" /></UnitTest>
  </TestDefinitions>
</TestRun></trx>`
	res, err := parseTRX(strings.NewReader(trx), "/var/www/html")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tests != 4 || res.Failures != 1 || res.Errors != 1 || res.Skipped != 1 || res.Seconds != 60.03 || len(res.Failed) != 2 {
		t.Fatalf("counts: %+v", res)
	}
	f := res.Failed[0]
	if f.Name != "Ships(count: 2)" || f.Class != "Shop.Tests.OrderTests" || f.Kind != "failure" || f.File != "tests/OrderTests.cs" || f.Line != 21 || f.Message != "Assert.Equal() Failure: Values differ" || !strings.Contains(f.Details, "tests/OrderTests.cs:line 21") || strings.Contains(f.Details, "/var/www/html/") {
		t.Fatalf("failed case: %+v", f)
	}
	if res.Failed[1].Kind != "error" || res.Failed[1].Name != "Shop.Tests.Hangs" {
		t.Fatalf("timeout: %+v", res.Failed[1])
	}
}

func TestDotnetActions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, dotnetRequest("Tools", true))
	if err != nil {
		t.Fatal(err)
	}
	actions, err := e.m.ListActions(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	reason := func(id string) string {
		for _, a := range actions {
			if a.ID == id {
				return a.Reason
			}
		}
		return "missing"
	}
	if r := reason("dotnet:build"); r != "*.sln or *.slnx or *.csproj or *.fsproj or *.vbproj not found in project" {
		t.Fatalf("dotnet:build without a project: %q", r)
	}
	_ = os.WriteFile(filepath.Join(e.projDir, "tools", "Tools.slnx"), []byte(""), 0o644)
	actions, _ = e.m.ListActions(ctx, v.Project.ID)
	if r := reason("dotnet:build"); r != "" {
		t.Fatalf("dotnet:build takes the solution: %q", r)
	}
	if r := reason("ef:database-update"); r == "" {
		t.Fatal("dotnet ef needs a project file at the top, a solution isn't enough")
	}
}
