package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

const shopManifest = `
version: 1
name: shop
docroot: public
web:
  server: nginx
php:
  version: "8.3"
  extensions: [intl, redis, pdo_mysql]
  memoryLimit: 512M
database:
  type: mariadb
  version: "11.4"
  exposePort: true
redis: true
mailpit: true
domains: [api.shop.example]
env:
  APP_ENV: local
secrets: [STRIPE_SECRET]
workers:
  - name: queue
    preset: laravel:queue
    arg: default
cron:
  - name: prune
    schedule: "0 3 * * *"
    command: php artisan model:prune
    timeout: 5m
`

func mustManifest(t *testing.T, src string) manifest.Manifest {
	t.Helper()
	mf, err := manifest.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return mf
}

func changeKeys(p ManifestPlan) []string {
	var out []string
	for _, c := range p.Changes {
		k := c.Section + ":" + c.Action
		if c.Item != "" {
			k = c.Section + "/" + c.Item + ":" + c.Action
		}
		if c.Skipped != "" {
			k += "(" + c.Skipped + ")"
		}
		out = append(out, k)
	}
	return out
}

func TestCreateFromManifestIsInSync(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mf := mustManifest(t, shopManifest)
	res, err := e.m.CreateFromManifest(ctx, mf, ManifestCreateRequest{Secrets: map[string]string{"STRIPE_SECRET": "sk_test"}})
	if err != nil {
		t.Fatal(err)
	}
	p := res.View.Project
	if p.Service(store.ServiceRedis) == nil || p.Service(store.ServiceMailpit) == nil || p.Service(store.ServiceWeb).Variant != "nginx" {
		t.Fatalf("services: %+v", p.Services)
	}
	// Workers, cron jobs and domains follow the create.
	workers, _ := e.store.Workers.ListByProject(ctx, p.ID)
	jobs, _ := e.store.CronJobs.ListByProject(ctx, p.ID)
	domains, _ := e.store.Domains.ListByProject(ctx, p.ID)
	if len(workers) != 1 || len(jobs) != 1 || len(domains) != 1 {
		t.Fatalf("workers=%d cron=%d domains=%d", len(workers), len(jobs), len(domains))
	}
	plan, err := e.m.PlanManifest(ctx, p.ID, mf, ManifestOptions{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.InSync {
		t.Fatalf("a project created from a manifest must be in sync with it: %+v", plan.Changes)
	}
	// The export describes the same project: applying it changes nothing either.
	exported, err := e.m.ExportManifest(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := manifest.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server: nginx", `version: "8.3"`, "memoryLimit: 512M", "exposePort: true", "redis:", "STRIPE_SECRET", "preset: laravel:queue", "timeout: 5m"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("export lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "sk_test") || strings.Contains(string(data), "password") {
		t.Fatalf("export leaks a secret:\n%s", data)
	}
	back := mustManifest(t, string(data))
	plan, err = e.m.PlanManifest(ctx, p.ID, back, ManifestOptions{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.InSync {
		t.Fatalf("the exported manifest must be in sync: %+v\n%s", plan.Changes, data)
	}
}

func TestApplyManifestChangesAndPrune(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, err := e.m.CreateFromManifest(ctx, mustManifest(t, shopManifest), ManifestCreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := res.View.Project.ID
	if !slices.Equal(res.Plan.MissingSecrets, []string{"STRIPE_SECRET"}) {
		t.Fatalf("missing secrets: %v", res.Plan.MissingSecrets)
	}

	// PHP 8.4, Redis gone, Memcached new, another variable, no worker, the cron job
	// disabled, the domain gone.
	next := mustManifest(t, `
version: 1
docroot: public
web: {server: nginx}
php:
  version: "8.4"
  extensions: [intl, redis, pdo_mysql]
  memoryLimit: 512M
database: {type: mariadb, version: "11.4", exposePort: true}
mailpit: true
memcached: true
env:
  APP_ENV: local
  APP_DEBUG: "1"
secrets: [STRIPE_SECRET]
cron:
  - {name: prune, schedule: "0 3 * * *", command: php artisan model:prune, timeout: 5m, enabled: false}
`)
	plan, err := e.m.PlanManifest(ctx, id, next, ManifestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"php:change", "redis:remove(prune)", "memcached:add", "env/APP_DEBUG:add", "domain/api.shop.example:remove(prune)", "worker/queue:remove(prune)", "cron/prune:change"}
	if got := changeKeys(plan); !slices.Equal(got, want) {
		t.Fatalf("plan\n got %v\nwant %v", got, want)
	}

	// Without prune nothing is removed.
	res, err = e.m.ApplyManifest(ctx, id, next, ManifestOptions{Secrets: map[string]string{"STRIPE_SECRET": "sk_live"}})
	if err != nil {
		t.Fatal(err)
	}
	p := res.View.Project
	if p.Service(store.ServicePHP).Version != "8.4" || p.Service(store.ServiceMemcached) == nil || p.Service(store.ServiceRedis) == nil {
		t.Fatalf("after apply: %+v", p.Services)
	}
	var secret store.EnvVar
	for _, v := range p.Env {
		if v.Key == "STRIPE_SECRET" {
			secret = v
		}
	}
	if secret.Value != "sk_live" || !secret.IsSecret {
		t.Fatalf("secret: %+v", secret)
	}
	jobs, _ := e.store.CronJobs.ListByProject(ctx, id)
	if len(jobs) != 1 || jobs[0].Enabled {
		t.Fatalf("cron: %+v", jobs)
	}
	plan, _ = e.m.PlanManifest(ctx, id, next, ManifestOptions{})
	if plan.Pending() || plan.InSync {
		t.Fatalf("only the skipped removals should be left: %v", changeKeys(plan))
	}

	// With prune the rest goes, and a given secret is kept afterwards.
	if _, err := e.m.ApplyManifest(ctx, id, next, ManifestOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	plan, _ = e.m.PlanManifest(ctx, id, next, ManifestOptions{Prune: true})
	if !plan.InSync {
		t.Fatalf("after prune: %v", changeKeys(plan))
	}
	v, _ := e.m.Get(ctx, id)
	if v.Project.Service(store.ServiceRedis) != nil {
		t.Fatal("redis should be removed")
	}
	for _, env := range v.Project.Env {
		if env.Key == "STRIPE_SECRET" && env.Value != "sk_live" {
			t.Fatalf("secret lost: %+v", env)
		}
	}
}

func TestApplyManifestDatabaseRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, err := e.m.CreateFromManifest(ctx, mustManifest(t, "version: 1\nname: db\nphp: {version: \"8.4\"}\ndatabase: {type: mariadb, version: \"11.4\"}\n"), ManifestCreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := res.View.Project.ID
	plan, _ := e.m.PlanManifest(ctx, id, mustManifest(t, "version: 1\nphp: {version: \"8.4\"}\ndatabase: {type: mariadb, version: \"10.11\"}\n"), ManifestOptions{Prune: true})
	if got := changeKeys(plan); !slices.Equal(got, []string{"database:change(downgrade)"}) {
		t.Fatalf("downgrade: %v", got)
	}
	other := mustManifest(t, "version: 1\nphp: {version: \"8.4\"}\ndatabase: {type: postgres}\n")
	plan, _ = e.m.PlanManifest(ctx, id, other, ManifestOptions{})
	// PostgreSQL brings its PHP driver along.
	if got := changeKeys(plan); !slices.Equal(got, []string{"php:change", "database:change(prune)"}) {
		t.Fatalf("type change without prune: %v", got)
	}
	res, err = e.m.ApplyManifest(ctx, id, other, ManifestOptions{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if db := res.View.Project.Service(store.ServiceDatabase); db == nil || db.Variant != "postgresql" {
		t.Fatalf("database after type change: %+v", db)
	}
	if php := res.View.Project.Service(store.ServicePHP); php == nil || !strings.Contains(string(php.Config), `"pdo_pgsql"`) {
		t.Fatalf("php after the change to PostgreSQL: %+v", php)
	}
}

func TestManifestRejectsWhatCreateRejects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for name, src := range map[string]string{
		"php version":    "version: 1\nname: x1\nphp: {version: \"5.6\"}\n",
		"extension":      "version: 1\nname: x2\nphp: {extensions: [nope]}\n",
		"worker preset":  "version: 1\nname: x3\nphp: {}\nworkers: [{name: w, preset: nope}]\n",
		"cron schedule":  "version: 1\nname: x4\nphp: {}\ncron: [{name: c, schedule: often, command: x}]\n",
		"undeclared key": "version: 1\nname: x5\n",
	} {
		t.Run(name, func(t *testing.T) {
			req := ManifestCreateRequest{}
			if name == "undeclared key" {
				req.Secrets = map[string]string{"TOKEN": "x"}
			}
			_, err := e.m.CreateFromManifest(ctx, mustManifest(t, src), req)
			if !errors.Is(err, validate.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
			if list, _ := e.store.Projects.List(ctx); len(list) != 0 {
				t.Fatalf("nothing may be created: %d projects", len(list))
			}
		})
	}
}

func TestRepositoryManifestFile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, phpRequest("files", false))
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	st, err := e.m.RepositoryManifestStatus(ctx, id)
	if err != nil || st.Present {
		t.Fatalf("no file yet: %+v %v", st, err)
	}
	data, err := e.m.WriteRepositoryManifest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join(e.projDir, v.Project.Path, manifest.FileName))
	if err != nil || string(onDisk) != string(data) {
		t.Fatalf("file: %v", err)
	}
	st, err = e.m.RepositoryManifestStatus(ctx, id)
	if err != nil || !st.Present || st.Plan == nil || !st.Plan.InSync {
		t.Fatalf("written file must be in sync: %+v %v", st, err)
	}

	// A broken file is reported, not an error of the request.
	if err := os.WriteFile(filepath.Join(e.projDir, v.Project.Path, manifest.FileName), []byte("version: 1\nredsi: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = e.m.RepositoryManifestStatus(ctx, id)
	if err != nil || !st.Present || st.Error == "" {
		t.Fatalf("broken file: %+v %v", st, err)
	}

	// A symlink out of the project directory is not followed.
	outside := filepath.Join(t.TempDir(), "secret.yml")
	_ = os.WriteFile(outside, []byte("version: 1\n"), 0o644)
	target := filepath.Join(e.projDir, v.Project.Path, manifest.FileName)
	_ = os.Remove(target)
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.m.ReadRepositoryManifest(ctx, id); err == nil {
		t.Fatal("reading through a symlink out of the project must fail")
	}
}

func TestCreateFromRepositoryAppliesTheClonedManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	withManifest := true
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		if len(spec.Cmd) > 3 && spec.Cmd[3] == "clone" {
			dir := filepath.Join(e.projDir, validate.Slugify(spec.Labels[docker.LabelProjectName]))
			_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
			if withManifest {
				_ = os.WriteFile(filepath.Join(dir, manifest.FileName), []byte("version: 1\ndocroot: web\nphp: {version: \"8.3\"}\nredis: true\nenv: {APP_ENV: repo}\n"), 0o644)
			}
		}
		return docker.ExecResult{}, nil
	}
	// The wizard asked for PHP 8.4 with a database; the repository's manifest wins.
	req := phpRequest("repo", true)
	req.Database = &DatabaseRequest{Type: "mariadb"}
	req.Git = &GitRequest{URL: "https://example.com/repo.git"}
	view, plan, err := e.m.CreateFromRepository(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	p := view.Project
	if plan == nil || p.Service(store.ServicePHP).Version != "8.3" || p.Service(store.ServiceRedis) == nil || p.Service(store.ServiceDatabase) != nil || p.Docroot != "web" {
		t.Fatalf("manifest not applied: plan=%v services=%+v docroot=%q", plan, p.Services, p.Docroot)
	}
	if p.DesiredState != store.DesiredRunning {
		t.Fatalf("the project should be started afterwards: %s", p.DesiredState)
	}

	// Without a manifest in the repository the request stands as it is.
	withManifest = false
	req = phpRequest("plain", false)
	req.Git = &GitRequest{URL: "https://example.com/plain.git"}
	view, plan, err = e.m.CreateFromRepository(ctx, req)
	if err != nil || plan != nil || view.Project.Service(store.ServicePHP).Version != "8.4" {
		t.Fatalf("no manifest: %v %v %+v", err, plan, view.Project.Services)
	}
}

func TestManifestLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := "version: 1\nname: capped\nphp: {version: \"8.4\"}\nredis: true\nlimits:\n  app: {cpus: 2, memory: 1536M}\n  services: {memory: 1G}\n  pids: 2048\n"
	res, err := e.m.CreateFromManifest(ctx, mustManifest(t, src), ManifestCreateRequest{Start: true})
	if err != nil {
		t.Fatal(err)
	}
	id := res.View.Project.ID
	want := store.ResourceLimits{App: store.LimitSet{CPUs: 2, MemoryMB: 1536}, Services: store.LimitSet{MemoryMB: 1024}, Pids: 2048}
	if res.View.Project.Limits != want {
		t.Fatalf("limits: %+v", res.View.Project.Limits)
	}
	if d, _ := e.engine.InspectContainer(ctx, "envoryx-capped-redis"); d.Resources != (docker.Resources{MemoryBytes: 1 << 30, PidsLimit: 2048}) {
		t.Fatalf("redis: %+v", d.Resources)
	}
	plan, _ := e.m.PlanManifest(ctx, id, mustManifest(t, src), ManifestOptions{Prune: true})
	if !plan.InSync {
		t.Fatalf("created from the file, the project must match it: %v", changeKeys(plan))
	}
	exported, _ := e.m.ExportManifest(ctx, id)
	data, _ := manifest.Marshal(exported)
	if !strings.Contains(string(data), "memory: 1536M") || !strings.Contains(string(data), "memory: 1G") {
		t.Fatalf("export:\n%s", data)
	}

	// Changed in the file: applied to the running containers.
	changed := strings.Replace(src, "cpus: 2,", "cpus: 1,", 1)
	if _, err := e.m.ApplyManifest(ctx, id, mustManifest(t, changed), ManifestOptions{}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.engine.InspectContainer(ctx, "envoryx-capped-php"); d.Resources.NanoCPUs != 1_000_000_000 {
		t.Fatalf("php after the change: %+v", d.Resources)
	}
	// Gone from the file: kept without prune, lifted with it.
	bare := "version: 1\nname: capped\nphp: {version: \"8.4\"}\nredis: true\n"
	plan, _ = e.m.PlanManifest(ctx, id, mustManifest(t, bare), ManifestOptions{})
	if got := changeKeys(plan); !slices.Equal(got, []string{"limits:remove(prune)"}) {
		t.Fatalf("plan: %v", got)
	}
	if _, err := e.m.ApplyManifest(ctx, id, mustManifest(t, bare), ManifestOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.engine.InspectContainer(ctx, "envoryx-capped-php"); d.Resources != (docker.Resources{PidsLimit: DefaultPidsLimit}) {
		t.Fatalf("after pruning: %+v", d.Resources)
	}
}
