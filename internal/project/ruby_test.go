package project

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
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
	for _, want := range []string{"PORT=3000", "RAILS_ENV=development", "RAILS_DEVELOPMENT_HOSTS=.test", "GEM_HOME=/home/envoryx/.gem/ruby/3.4.0", "BUNDLE_APP_CONFIG=/var/www/html/.bundle", "BUNDLE_USER_CACHE=/var/cache/envoryx/bundler"} {
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
	// ~/.gem exists before any gem install, so RubyGems puts Gem.user_dir (where
	// --user-install goes) below it, at GEM_HOME, not below ~/.local/share/gem.
	if fi, err := os.Stat(filepath.Join(e.cfgDir, "projects", proj.ID, "home", ".gem", "ruby", "3.4.0")); err != nil || !fi.IsDir() {
		t.Fatalf("GEM_HOME not created in the project home: %v", err)
	}
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

// Only PHP knows the pgsql scheme: every other runtime gets postgresql://.
func TestPostgresURLs(t *testing.T) {
	env := postgresURLs([]string{"DATABASE_URL=pgsql://u:p@database:5432/shop", "ANALYTICS_DATABASE_URL=pgsql://u:p@analytics:5432/a", "DB_CONNECTION=pgsql", "OTHER=pgsql://x"})
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
			// rails and the bundle land where the server looks for them.
			if !slices.Contains(s.Env, "GEM_HOME=/home/envoryx/.gem/ruby/3.4.0") || !slices.Contains(s.Env, "HOME=/home/envoryx") {
				t.Fatalf("template env: %v", s.Env)
			}
		}
	}
	if len(steps) != 2 || !slices.Contains(steps[0], "--name=blog") || !slices.Contains(steps[0], "--database=sqlite3") || !strings.Contains(steps[0][2], "gem install") {
		t.Fatalf("template steps: %q", steps)
	}
	if !slices.Equal(steps[1], []string{"ruby", "-e", railsQueueDatabase}) {
		t.Fatalf("queue database step: %q", steps[1])
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
	if !slices.Contains(w.Spec.Env, "RAILS_ENV=development") || !slices.Contains(w.Spec.Env, "GEM_HOME=/home/envoryx/.gem/ruby/3.4.0") {
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
	// rails test writes no report: its counts and failed tests come from its output.
	s := &TestSession{Suite: rails, runID: store.NewID(), projectID: v.Project.ID, Release: func() {}}
	run, err := e.m.FinishTestRun(ctx, s, 1, false, []byte("# Running:\r\n\r\nF\r\n\r\nFailure:\r\nPostTest#test_title [test/models/post_test.rb:9]:\r\nExpected: \"a\"\r\n  Actual: \"b\"\r\n\r\nbin/rails test test/models/post_test.rb:8\r\n\r\nFinished in 0.01s, 1 runs/s.\r\n2 runs, 2 assertions, 1 failures, 0 errors, 0 skips\r\n"))
	s.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !run.Result.Report || run.Result.Tests != 2 || run.Result.Failures != 1 || len(run.Result.Failed) != 1 || run.Result.Failed[0].Name != "test_title" || run.Status != store.TestFailed {
		t.Fatalf("rails test run: %+v", run)
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
		"postgresql://u:p@database:5432/shop": "postgresql://u:p@database:5432/shop_test",
		"":                                    "",
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

// Solid Queue keeps its jobs in <database>_queue: the Ruby containers get its URL, the
// Solid Queue worker waits for its tables, and Envoryx creates it for an application
// whose database.yml has a queue entry (MySQL's project login may not).
func TestRailsQueueDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var sql []string
	e.engine.ExecHandler = func(container string, cmd []string, env []string) (docker.ExecResult, error) {
		if container == "envoryx-blog-database" {
			sql = append(sql, strings.Join(cmd, " "))
			return docker.ExecResult{Stdout: "blog\n"}, nil
		}
		return docker.ExecResult{}, nil
	}
	// rails new writes the Rails 8 database.yml with a queue entry.
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		if spec.Cmd[0] == "sh" {
			writeProjectFiles(t, filepath.Join(e.projDir, "blog"), map[string]string{"bin/rails": "", "config/database.yml": "production:\n  queue:\n    database: blog_production_queue\n"})
		}
		return docker.ExecResult{}, nil
	}
	req := CreateRequest{Name: "Blog", Ruby: &RubyRequest{Version: "3.4"}, Template: "rails", Database: &DatabaseRequest{Type: "postgresql"}, Start: true}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	created := func() int {
		n := 0
		for _, s := range sql {
			if strings.Contains(s, `CREATE DATABASE "blog_queue"`) {
				n++
			}
		}
		return n
	}
	if created() != 1 {
		t.Fatalf("the template must create the queue database: %q", sql)
	}
	var cfg runtime.DatabaseConfig
	_ = json.Unmarshal(v.Project.Service(store.ServiceDatabase).Config, &cfg)
	c, _ := e.engine.Container("envoryx-blog-ruby")
	if want := "QUEUE_DATABASE_URL=postgresql://blog:" + cfg.Password + "@database:5432/blog_queue"; !slices.Contains(c.Spec.Env, want) {
		t.Fatalf("ruby env lacks %s: %v", want, c.Spec.Env)
	}

	// The Rails database actions create it again when it went missing.
	sql = nil
	if _, _, release, err := e.m.RunAction(ctx, v.Project.ID, "rails:db-prepare", 0, 0); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	if created() != 1 {
		t.Fatalf("rails db:prepare must create the queue database: %q", sql)
	}
	// Not for an application without a queue entry.
	writeProjectFiles(t, filepath.Join(e.projDir, "blog"), map[string]string{"config/database.yml": "development:\n  database: blog\n"})
	sql = nil
	if _, _, release, err := e.m.RunAction(ctx, v.Project.ID, "rails:db-migrate", 0, 0); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	if len(sql) != 0 {
		t.Fatalf("no queue database without a queue entry: %q", sql)
	}
	// The production entries of Solid Cache and Solid Cable get theirs, too.
	writeProjectFiles(t, filepath.Join(e.projDir, "blog"), map[string]string{"config/database.yml": "production:\n  primary:\n    database: blog\n  cache:\n    database: blog_production_cache\n  cable:\n    database: blog_production_cable\n"})
	sql = nil
	if _, _, release, err := e.m.RunAction(ctx, v.Project.ID, "rails:db-prepare", 0, 0); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	if joined := strings.Join(sql, "\n"); !strings.Contains(joined, `CREATE DATABASE "blog_cache"`) || !strings.Contains(joined, `CREATE DATABASE "blog_cable"`) || created() != 0 {
		t.Fatalf("cache and cable databases: %q", sql)
	}
	for _, want := range []string{"CACHE_DATABASE_URL=postgresql://blog:" + cfg.Password + "@database:5432/blog_cache", "CABLE_DATABASE_URL=postgresql://blog:" + cfg.Password + "@database:5432/blog_cable"} {
		if !slices.Contains(c.Spec.Env, want) {
			t.Fatalf("ruby env lacks %s: %v", want, c.Spec.Env)
		}
	}

	// The Solid Queue worker waits for its tables instead of crash-looping.
	if _, err := e.m.AddWorker(ctx, v.Project.ID, WorkerRequest{Name: "jobs", Preset: "solidqueue:start", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	w, _ := e.engine.Container("envoryx-blog-worker-jobs")
	if !strings.Contains(w.Spec.Cmd[2], "bundle check") || !strings.Contains(w.Spec.Cmd[2], "SolidQueue::Job.table_exists?") || !slices.Equal(w.Spec.Cmd[4:], []string{"bin/jobs", "start"}) {
		t.Fatalf("solid queue worker: %q", w.Spec.Cmd)
	}
	if !slices.ContainsFunc(w.Spec.Env, func(kv string) bool { return strings.HasPrefix(kv, "QUEUE_DATABASE_URL=postgresql://") }) {
		t.Fatalf("worker env: %v", w.Spec.Env)
	}
}

// The project's own QUEUE_, CACHE_ and CABLE_DATABASE_URL win; MongoDB has none of them.
func TestRubyQueueDatabaseURL(t *testing.T) {
	pg := store.ProjectService{Kind: store.ServiceDatabase, Variant: "postgresql", Enabled: true, Config: json.RawMessage(`{"database":"shop","username":"shop","password":"pw"}`)}
	got := rubyDatabaseEnv(store.Project{Services: []store.ProjectService{pg}}, []string{"DATABASE_URL=pgsql://shop:pw@database:5432/shop"})
	if !slices.Contains(got, "QUEUE_DATABASE_URL=postgresql://shop:pw@database:5432/shop_queue") {
		t.Fatalf("postgres: %v", got)
	}
	for _, want := range []string{"CACHE_DATABASE_URL=postgresql://shop:pw@database:5432/shop_cache", "CABLE_DATABASE_URL=postgresql://shop:pw@database:5432/shop_cable"} {
		if !slices.Contains(got, want) {
			t.Fatalf("postgres lacks %s: %v", want, got)
		}
	}
	own := []string{"QUEUE_DATABASE_URL=postgresql://elsewhere/q", "CACHE_DATABASE_URL=postgresql://elsewhere/c", "CABLE_DATABASE_URL=postgresql://elsewhere/w"}
	if got := rubyDatabaseEnv(store.Project{Services: []store.ProjectService{pg}}, own); !slices.Equal(got, own) {
		t.Fatalf("own URL: %v", got)
	}
	mongo := store.ProjectService{Kind: store.ServiceDatabase, Variant: "mongodb", Enabled: true, Config: json.RawMessage(`{"database":"shop"}`)}
	if got := rubyDatabaseEnv(store.Project{Services: []store.ProjectService{mongo}}, nil); len(got) != 0 {
		t.Fatalf("mongodb: %v", got)
	}
	if got := rubyDatabaseEnv(store.Project{}, nil); len(got) != 0 {
		t.Fatalf("no database: %v", got)
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

// GEM_HOME is Gem.user_dir, RbConfig::CONFIG["ruby_version"] of the version: X.Y.0.
func TestRubyGemHome(t *testing.T) {
	for version, want := range map[string]string{
		"4.0":    "/home/envoryx/.gem/ruby/4.0.0",
		"3.4":    "/home/envoryx/.gem/ruby/3.4.0",
		"3.3":    "/home/envoryx/.gem/ruby/3.3.0",
		"3.4.2":  "/home/envoryx/.gem/ruby/3.4.0",
		"":       "/home/envoryx/.gem/ruby",
		"latest": "/home/envoryx/.gem/ruby",
		"3.x":    "/home/envoryx/.gem/ruby",
	} {
		if got := rubyGemHome(version); got != want {
			t.Errorf("rubyGemHome(%q) = %s, want %s", version, got, want)
		}
	}
	env := rubyEnv("3.4")
	for _, want := range []string{"GEM_HOME=/home/envoryx/.gem/ruby/3.4.0", "GEM_PATH=/home/envoryx/.gem/ruby/3.4.0:/usr/local/bundle"} {
		if !slices.Contains(env, want) {
			t.Fatalf("rubyEnv lacks %s: %v", want, env)
		}
	}
	if !slices.ContainsFunc(env, func(v string) bool { return strings.HasPrefix(v, "PATH=/home/envoryx/.gem/ruby/3.4.0/bin:") }) {
		t.Fatalf("PATH misses GEM_HOME/bin: %v", env)
	}
}
