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

// rubyRequest is a project without PHP whose Rails server is the application.
func rubyRequest(name string, start bool) CreateRequest {
	return CreateRequest{
		Name:          name,
		Ruby:          &RubyRequest{Version: "3.4", Config: runtime.RubyConfig{Server: true, Preset: "rails"}},
		CreateStarter: true,
		Start:         start,
	}
}

func TestRubyServerServesProject(t *testing.T) {
	e := newEnv(t)
	e.selfID = "envoryx-self"
	e.engine.AddForeignContainer("envoryx-self", "ghcr.io/envoryx/envoryx", "running")
	ctx := context.Background()

	v, err := e.m.Create(ctx, rubyRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-shop-ruby")
	if !ok {
		t.Fatal("ruby container missing")
	}
	// Behind the bin/rails wait and the bundle install, Rails serves on all interfaces.
	if c.Spec.Cmd[0] != "sh" || !strings.Contains(c.Spec.Cmd[2], "[ -e bin/rails ]") || !strings.Contains(c.Spec.Cmd[2], "bundle install") || !slices.Contains(c.Spec.Cmd, "bin/rails") {
		t.Fatalf("ruby command: %q", c.Spec.Cmd)
	}
	if c.Spec.Image != "ghcr.io/envoryx/envoryx-ruby:3.4" || c.Spec.User != "1000:1000" || c.Spec.WorkingDir != "/var/www/html" {
		t.Fatalf("ruby container: %+v", c.Spec)
	}
	for _, want := range []string{"PORT=3000", "RAILS_ENV=development", "RAILS_DEVELOPMENT_HOSTS=.test", "GEM_HOME=/home/envoryx/.gem/ruby", "BUNDLE_APP_CONFIG=/var/www/html/.bundle", "BUNDLE_USER_CACHE=/var/cache/envoryx/bundler"} {
		if !slices.Contains(c.Spec.Env, want) {
			t.Fatalf("ruby env lacks %s: %v", want, c.Spec.Env)
		}
	}
	if len(c.Spec.Ports) != 1 || c.Spec.Ports[0].ContainerPort != 3000 {
		t.Fatalf("server port: %+v", c.Spec.Ports)
	}
	web, _ := e.engine.Container("envoryx-shop-web")
	if len(web.Spec.Ports) != 0 {
		t.Fatalf("web port must stay unpublished: %+v", web.Spec.Ports)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop", "index.html")); err == nil {
		t.Fatal("no starter page while the Ruby server serves the project")
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	if Serves(proj) != "ruby" {
		t.Fatalf("Serves = %s", Serves(proj))
	}
	if kind, _ := AppKind(proj); kind != store.ServiceRuby {
		t.Fatalf("AppKind = %s", kind)
	}
	table, err := e.m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range table.Routes {
		if r.ProjectID == proj.ID && r.Dial == "envoryx-shop-ruby:3000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no route to the Ruby server: %+v", table.Routes)
	}
	target, err := e.m.ResolveSSHUser(ctx, "shop.ruby")
	if err != nil || target.Kind != store.ServiceRuby {
		t.Fatalf("ssh user: %+v %v", target, err)
	}
}

// Active Record does not know the pgsql scheme: the Ruby containers get postgresql://.
func TestRubyDatabaseURLs(t *testing.T) {
	env := rubyDatabaseURLs([]string{"DATABASE_URL=pgsql://u:p@database:5432/shop", "ANALYTICS_DATABASE_URL=pgsql://u:p@analytics:5432/a", "DB_CONNECTION=pgsql", "OTHER=pgsql://x"})
	want := []string{"DATABASE_URL=postgresql://u:p@database:5432/shop", "ANALYTICS_DATABASE_URL=postgresql://u:p@analytics:5432/a", "DB_CONNECTION=pgsql", "OTHER=pgsql://x"}
	if !slices.Equal(env, want) {
		t.Fatalf("got %q", env)
	}
}

// rdbg runs the server once debugging is on; switching it recreates the container, and
// the ports survive the edit.
func TestRubyDebugAndUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, rubyRequest("Dbg", true))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.engine.Container("envoryx-dbg-ruby")
	hostPort := before.Spec.Ports[0].HostPort
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Ruby: &RubyUpdate{Enabled: true, Version: "3.4", Config: runtime.RubyConfig{Server: true, Preset: "rails", Debug: true}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-dbg-ruby")
	if !slices.Contains(c.Spec.Cmd, "envoryx-rdbg") || len(c.Spec.Ports) != 2 || c.Spec.Ports[0].HostPort != hostPort || c.Spec.Ports[1].ContainerPort != runtime.DefaultRdbgPort {
		t.Fatalf("debug: cmd %q ports %+v", c.Spec.Cmd, c.Spec.Ports)
	}
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{Ruby: &RubyUpdate{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-dbg-ruby"); ok {
		t.Fatal("ruby container must be gone")
	}
}

func TestRubyTemplateWorkersTestsAndManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := CreateRequest{Name: "Blog", Ruby: &RubyRequest{Version: "3.4"}, Template: "rails"}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var steps [][]string
	for _, s := range e.engine.OneShots {
		if s.Image == "ghcr.io/envoryx/envoryx-ruby:3.4" {
			steps = append(steps, s.Cmd)
		}
	}
	if len(steps) != 1 || !slices.Contains(steps[0], "--name=blog") || !slices.Contains(steps[0], "--database=sqlite3") || !strings.Contains(steps[0][2], "gem install") {
		t.Fatalf("template steps: %q", steps)
	}
	proj, _ := e.m.loadProject(ctx, v.Project.ID)
	cfg, _ := rubyConfig(proj)
	if !cfg.Server || cfg.Preset != "rails" || cfg.Port != 3000 {
		t.Fatalf("template merged into the config: %+v", cfg)
	}

	// A Sidekiq worker from the Ruby image, behind the bundle install, in the server's
	// environment.
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "jobs", Preset: "sidekiq", Arg: "default,mailers", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	w, ok := e.engine.Container("envoryx-blog-worker-jobs")
	if !ok || w.Spec.Image != "ghcr.io/envoryx/envoryx-ruby:3.4" || !strings.Contains(w.Spec.Cmd[2], "bundle check") || !slices.Equal(w.Spec.Cmd[4:], []string{"bundle", "exec", "sidekiq", "-q", "default", "-q", "mailers"}) {
		t.Fatalf("ruby worker: %+v", w.Spec)
	}
	if !slices.Contains(w.Spec.Env, "RAILS_ENV=development") || !slices.Contains(w.Spec.Env, "GEM_HOME=/home/envoryx/.gem/ruby") {
		t.Fatalf("ruby worker env: %v", w.Spec.Env)
	}
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "bad", Preset: "rake:task", Arg: "jobs; rm -rf /", Enabled: true}); err == nil {
		t.Fatal("a task name with shell syntax must be refused")
	}

	// rails test and rspec once the files exist; both point DATABASE_URL at the test
	// database.
	dir := filepath.Join(e.projDir, "blog")
	for _, d := range []string{"bin", "test", "spec"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(dir, "Gemfile"), []byte("source \"https://rubygems.org\"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bin", "rails"), []byte("#!/usr/bin/env ruby\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "Gemfile.lock"), []byte("GEM\n  specs:\n    rspec-core (3.13.0)\n    rspec_junit_formatter (0.6.0)\n"), 0o644)
	suites, err := e.m.TestSuites(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]TestSuite{}
	for _, s := range suites {
		byID[s.ID] = s
	}
	rspec, rails := byID["rspec"], byID["rails"]
	if rspec.Service != store.ServiceRuby || !rspec.Report || rails.Service != store.ServiceRuby || rails.Report {
		t.Fatalf("ruby suites: %+v", suites)
	}
	argv, env := rspec.build("signs in", "/tmp/report.xml")
	if argv[2] != rubyTestScript || strings.Join(argv[4:], " ") != "bundle exec rspec --force-color --format progress --format RspecJunitFormatter --out /tmp/report.xml -e signs in" || !slices.Contains(env, "RAILS_ENV=test") {
		t.Fatalf("rspec argv %q env %v", argv, env)
	}
	argv, _ = rails.build("/login/", "")
	if strings.Join(argv[4:], " ") != "bin/rails test -n /login/" {
		t.Fatalf("rails test argv: %q", argv)
	}

	mf, err := e.m.ExportManifest(ctx, v.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Ruby == nil || !mf.Ruby.Server || mf.Ruby.Version != "3.4" || mf.Ruby.Preset != "rails" {
		t.Fatalf("manifest ruby: %+v", mf.Ruby)
	}
	req2 := manifestRequest(manifest.Manifest{Version: 1, Ruby: &manifest.Ruby{Version: "3.3", Server: true, Preset: "rack", Port: 4567}}, "From Manifest")
	if req2.Ruby == nil || req2.Ruby.Config.Preset != "rack" || req2.Ruby.Config.Port != 4567 {
		t.Fatalf("manifest import: %+v", req2.Ruby)
	}
}

// The test runs must never reach the development database: DATABASE_URL gains _test.
func TestRubyTestScript(t *testing.T) {
	for in, want := range map[string]string{
		"postgresql://u:p@database:5432/shop":            "postgresql://u:p@database:5432/shop_test",
		"":                                               "",
		"mongodb://u:p@database:27017/shop?authSource=a": "mongodb://u:p@database:27017/shop?authSource=a",
	} {
		cmd := exec.Command("sh", "-c", rubyTestScript, "envoryx-test", "sh", "-c", `printf %s "$DATABASE_URL"`)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "DATABASE_URL=" + in}
		out, err := cmd.Output()
		if err != nil || string(out) != want {
			t.Fatalf("%q: got %q, err %v", in, out, err)
		}
	}
}

func TestRailsNewArguments(t *testing.T) {
	for slug, want := range map[string]string{"shop-api": "shop_api", "test": "app_test", "42shop": "app_42shop"} {
		if got := railsAppName(slug); got != want {
			t.Errorf("%s: %s, want %s", slug, got, want)
		}
	}
	p := store.Project{Services: []store.ProjectService{{Kind: store.ServiceDatabase, Variant: "mariadb", Enabled: true}}}
	if got := railsDatabase(p); got != "mariadb-mysql" {
		t.Fatalf("mariadb: %s", got)
	}
}
