package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// externalServer fakes an external MySQL behind the client containers: it lists
// databases, dumps and restores.
type externalServer struct {
	databases []string
	fail      string // stderr for every call when set
	oneshots  [][]string
	restored  string
}

func (s *externalServer) install(e *env) {
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		argv := append(slices.Clone(spec.Entrypoint), spec.Cmd...)
		s.oneshots = append(s.oneshots, argv)
		if s.fail != "" {
			return docker.ExecResult{ExitCode: 1, Stderr: s.fail}, nil
		}
		if argv[0] == "redis-cli" {
			return docker.ExecResult{Stdout: "PONG\n"}, nil
		}
		return docker.ExecResult{Stdout: strings.Join(s.databases, "\n") + "\n"}, nil
	}
	e.engine.OneShotStreamHandler = func(spec docker.ContainerSpec, stdin []byte) (string, int, error) {
		argv := append(slices.Clone(spec.Entrypoint), spec.Cmd...)
		s.oneshots = append(s.oneshots, argv)
		if strings.HasSuffix(argv[0], "dump") {
			return "-- external dump\nCREATE TABLE orders (id int);\n", 0, nil
		}
		s.restored = string(stdin)
		return "", 0, nil
	}
}

func externalMySQL() *DatabaseRequest {
	return &DatabaseRequest{Type: "mysql", External: &ExternalDatabase{Host: "db.example.com", Username: "shop_app", Password: "p@ss", Database: "shop"}}
}

func TestExternalDatabaseHasNoContainerAndInjectsTheServer(t *testing.T) {
	e := newEnv(t)
	srv := &externalServer{databases: []string{"information_schema", "shop"}}
	srv.install(e)
	req := phpRequest("Ext Shop", true)
	req.Database = externalMySQL()
	view, err := e.m.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-ext-shop-database"); ok {
		t.Fatal("an external database must not get a container")
	}
	if len(srv.oneshots) == 0 || srv.oneshots[0][0] != "mysql" || !slices.Contains(srv.oneshots[0], "-hdb.example.com") || !slices.Contains(srv.oneshots[0], "-ushop_app") {
		t.Fatalf("the connection is checked as the project user: %v", srv.oneshots)
	}
	php, _ := e.engine.Container("envoryx-ext-shop-php")
	env := strings.Join(php.Spec.Env, "\n")
	for _, want := range []string{"DB_HOST=db.example.com", "DB_PORT=3306", "DB_USERNAME=shop_app", "DB_PASSWORD=p@ss", "DATABASE_URL=mysql://shop_app:p%40ss@db.example.com:3306/shop"} {
		if !strings.Contains(env, want) {
			t.Fatalf("php env lacks %s: %v", want, php.Spec.Env)
		}
	}
	if !slices.Contains(php.Spec.ExtraHosts, "host.docker.internal:host-gateway") {
		t.Fatalf("app containers must reach the Docker host: %v", php.Spec.ExtraHosts)
	}
	got, err := e.m.Get(context.Background(), view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.State != StateRunning {
		t.Fatalf("an external database does not make the project partial: %s", got.Status.State)
	}
	dbs, err := e.m.Databases(context.Background(), view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 1 || !dbs[0].External || dbs[0].Host != "db.example.com" || dbs[0].Port != 3306 || dbs[0].State != "external" || dbs[0].VolumeName != "" {
		t.Fatalf("database info: %+v", dbs)
	}
	creds, _ := e.m.DatabaseCredentials(context.Background(), view.Project.ID, "")
	if creds.Host != "db.example.com" || creds.RootPassword != "" || creds.Password != "p@ss" {
		t.Fatalf("credentials: %+v", creds)
	}
}

func TestExternalDatabaseIsCheckedBeforeAnythingIsCreated(t *testing.T) {
	e := newEnv(t)
	srv := &externalServer{fail: "ERROR 2005 (HY000): Unknown server host 'db.example.com'"}
	srv.install(e)
	req := phpRequest("Ext Fail", true)
	req.Database = externalMySQL()
	if _, err := e.m.Create(context.Background(), req); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "Unknown server host") {
		t.Fatalf("an unreachable server: %v", err)
	}
	list, _ := e.m.List(context.Background())
	if len(list) != 0 {
		t.Fatalf("nothing may be left behind: %d projects", len(list))
	}
	srv.fail = ""
	srv.databases = []string{"other"}
	if _, err := e.m.Create(context.Background(), req); err == nil || !strings.Contains(err.Error(), `has no database "shop"`) {
		t.Fatalf("a database the user cannot see: %v", err)
	}
	req.Database.ExposePort = true
	if _, err := e.m.Create(context.Background(), req); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("publishing a port of an external server: %v", err)
	}
}

func TestExternalDatabaseChangesAndRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	srv := &externalServer{databases: []string{"shop", "shop_test", "another_app"}}
	srv.install(e)
	req := phpRequest("Ext Ops", true)
	req.Database = externalMySQL()
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	list, err := e.m.ListDatabases(ctx, id, "")
	if err != nil || !slices.Contains(list, "another_app") {
		t.Fatalf("list: %v %v", list, err)
	}
	if err := e.m.CreateDatabase(ctx, id, "", "shop_stage"); err != nil {
		t.Fatal(err)
	}
	if last := srv.oneshots[len(srv.oneshots)-1]; strings.Contains(strings.Join(last, " "), "GRANT") || !strings.Contains(strings.Join(last, " "), "CREATE DATABASE `shop_stage`") {
		t.Fatalf("create on an external server grants nothing: %v", last)
	}
	if err := e.m.DropDatabase(ctx, id, "", "shop_test", "shop_test"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("dropping on an external server: %v", err)
	}
	if _, err := e.m.RotateDatabasePassword(ctx, id, ""); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("rotating an external password: %v", err)
	}
	if _, err := e.m.SetDatabaseExposed(ctx, id, "", true); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("publishing an external port: %v", err)
	}

	// A new host with the password left empty keeps the stored password.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, External: &ExternalDatabase{Host: "db2.example.com", Port: 3307, Username: "shop_app", Database: "shop"}}}); err != nil {
		t.Fatal(err)
	}
	php, _ := e.engine.Container("envoryx-ext-ops-php")
	if env := strings.Join(php.Spec.Env, "\n"); !strings.Contains(env, "DB_HOST=db2.example.com") || !strings.Contains(env, "DB_PORT=3307") || !strings.Contains(env, "DB_PASSWORD=p@ss") {
		t.Fatalf("after changing the connection: %v", php.Spec.Env)
	}

	// Removing it forgets the connection: no confirmation, nothing dropped anywhere.
	before := len(srv.oneshots)
	if _, err := e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if len(srv.oneshots) != before {
		t.Fatalf("removing an external database must not talk to the server: %v", srv.oneshots[before:])
	}
	p, _ := e.m.loadProject(ctx, id)
	if p.Service(store.ServiceDatabase) != nil {
		t.Fatal("the database is still configured")
	}
}

func TestExternalDatabaseSnapshotAndDuplicate(t *testing.T) {
	e := newEnv(t)
	e.backupsDir = t.TempDir()
	ctx := context.Background()
	srv := &externalServer{databases: []string{"shop"}}
	srv.install(e)
	req := phpRequest("Ext Snap", true)
	req.Database = externalMySQL()
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := e.m.CreateSnapshot(ctx, view.Project.ID, "", "before the migration")
	if err != nil {
		t.Fatal(err)
	}
	if snap.SizeBytes == 0 {
		t.Fatalf("snapshot: %+v", snap)
	}
	var dump []string
	for _, argv := range srv.oneshots {
		if argv[0] == "mysqldump" {
			dump = argv
		}
	}
	if dump == nil || !slices.Contains(dump, "--no-tablespaces") || !slices.Contains(dump, "-hdb.example.com") {
		t.Fatalf("the dump runs in a client container against the server: %v", srv.oneshots)
	}
	if _, err := e.m.RestoreSnapshot(ctx, view.Project.ID, "", snap.ID, "ext-snap"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(srv.restored, "CREATE TABLE orders") {
		t.Fatalf("the restore goes to the external server: %q", srv.restored)
	}

	// The copy gets a database of its own, filled from the external one.
	var imported string
	e.engine.StreamHandler = func(container string, cmd []string, _ []string, stdin []byte) (string, int, error) {
		if strings.HasSuffix(container, "-database") && cmd[0] == "mysql" {
			imported = string(stdin)
		}
		return "", 0, nil
	}
	copied, err := e.m.Duplicate(ctx, view.Project.ID, DuplicateRequest{Name: "Ext Snap Copy", Database: true, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	db, ok := e.engine.Container("envoryx-ext-snap-copy-database")
	if !ok || db.Spec.Image != "mysql:8.4" {
		t.Fatalf("the copy runs a local database: %+v", db.Spec)
	}
	if !strings.Contains(imported, "CREATE TABLE orders") {
		t.Fatalf("the copy is filled from the external server: %q", imported)
	}
	cp, _ := e.m.loadProject(ctx, copied.Project.ID)
	if externalService(cp.Service(store.ServiceDatabase)) {
		t.Fatal("the copy must not point at the external server")
	}
}

func TestExternalRedis(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	srv := &externalServer{}
	srv.install(e)
	req := phpRequest("Ext Cache", true)
	req.Redis = &ExtraRequest{External: &ExternalRedis{Host: "cache.lan", Password: "s3cret"}}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.engine.Container("envoryx-ext-cache-redis"); ok {
		t.Fatal("an external Redis must not get a container")
	}
	var ping []string
	for _, argv := range srv.oneshots {
		if argv[0] == "redis-cli" {
			ping = argv
		}
	}
	if ping == nil || slices.Contains(ping, "s3cret") || !slices.Contains(ping, "cache.lan") {
		t.Fatalf("redis is pinged without the password on the command line: %v", srv.oneshots)
	}
	php, _ := e.engine.Container("envoryx-ext-cache-php")
	if env := strings.Join(php.Spec.Env, "\n"); !strings.Contains(env, "REDIS_HOST=cache.lan") || !strings.Contains(env, "REDIS_PASSWORD=s3cret") || !strings.Contains(env, "REDIS_URL=redis://:s3cret@cache.lan:6379") {
		t.Fatalf("php env: %v", php.Spec.Env)
	}
	extras, _ := e.m.ExtraServices(ctx, view.Project.ID)
	if len(extras) != 1 || !extras[0].External || extras[0].Host != "cache.lan" || extras[0].VolumeName != "" {
		t.Fatalf("extras: %+v", extras)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Redis: &ExtraUpdate{Enabled: false}}); err != nil {
		t.Fatalf("removing an external Redis needs no confirmation: %v", err)
	}
}

func TestExternalDatabaseInTheManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	srv := &externalServer{databases: []string{"shop"}}
	srv.install(e)
	req := phpRequest("Ext Manifest", false)
	req.Database = externalMySQL()
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	mf, err := e.m.ExportManifest(ctx, view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Database == nil || mf.Database.External == nil || mf.Database.External.Host != "db.example.com" || mf.Database.External.Username != "shop_app" {
		t.Fatalf("export: %+v", mf.Database)
	}
	// Unchanged it is in sync; another host is applied without the password.
	plan, err := e.m.PlanManifest(ctx, view.Project.ID, mf, ManifestOptions{})
	if err != nil || !plan.InSync {
		t.Fatalf("an exported manifest is in sync: %+v %v", plan, err)
	}
	mf.Database.External.Host = "db2.example.com"
	if _, err := e.m.ApplyManifest(ctx, view.Project.ID, mf, ManifestOptions{}); err != nil {
		t.Fatal(err)
	}
	creds, _ := e.m.DatabaseCredentials(ctx, view.Project.ID, "")
	if creds.Host != "db2.example.com" || creds.Password != "p@ss" {
		t.Fatalf("after applying: %+v", creds)
	}
	// A new external connection cannot come from the file.
	mf.Databases = map[string]manifest.Database{"analytics": {Type: "postgres", External: &manifest.External{Host: "pg.lan", Username: "a", Database: "a"}}}
	plan, err = e.m.PlanManifest(ctx, view.Project.ID, mf, ManifestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	skipped := false
	for _, c := range plan.Changes {
		if c.Section == "databases.analytics" && c.Skipped == "external" {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("a new external database is skipped: %+v", plan.Changes)
	}
}

// A client container that happens to run while the status is read is no stray service.
func TestTransientHelpersAreNoStrayServices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Helpers", true))
	if err != nil {
		t.Fatal(err)
	}
	image := view.Project.Service(store.ServiceWeb).Image
	for _, svc := range []string{"dbclient", "git", "template"} {
		if _, err := e.engine.CreateContainer(ctx, docker.ContainerSpec{Name: "envoryx-helpers-" + svc, Image: image, Labels: docker.ManagedLabels(view.Project.ID, "helpers", svc, "")}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.m.Get(ctx, view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Warnings) != 0 {
		t.Fatalf("helpers reported as removed services: %v", got.Status.Warnings)
	}
}
