package project

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func dbRequest(name string, expose bool) CreateRequest {
	req := phpRequest(name, true)
	req.Database = &DatabaseRequest{Type: "mariadb", Version: "11", ExposePort: expose}
	return req
}

func TestCreateProjectWithDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, dbRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	p := view.Project
	svc := p.Service(store.ServiceDatabase)
	if svc == nil || svc.Variant != "mariadb" || svc.Image != "mariadb:11" {
		t.Fatalf("database service: %+v", svc)
	}
	var cfg runtime.DatabaseConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "shop" || cfg.Username != "shop" || len(cfg.Password) != runtime.PasswordLength || len(cfg.RootPassword) != runtime.PasswordLength || cfg.Password == cfg.RootPassword {
		t.Fatalf("generated config: %+v", cfg)
	}
	if cfg.HostPort != 20001 || p.HTTPPort != 20000 {
		t.Fatalf("ports: web %d db %d", p.HTTPPort, cfg.HostPort)
	}
	if got := strings.Join(e.engine.VolumeNames(), ","); got != "envoryx-shop-database" {
		t.Fatalf("volumes: %s", got)
	}
	db, _ := e.engine.Container("envoryx-shop-database")
	if db.State != "running" || db.Spec.Mounts[0].Type != "volume" || db.Spec.Mounts[0].Source != "envoryx-shop-database" || db.Spec.Mounts[0].Target != "/var/lib/mysql" {
		t.Fatalf("database container: %+v", db)
	}
	if db.Spec.Healthcheck == nil || db.Spec.Ports[0].HostPort != 20001 || db.Spec.Ports[0].ContainerPort != 3306 {
		t.Fatalf("database container spec: %+v", db.Spec)
	}
	envs := strings.Join(db.Spec.Env, "\n")
	if !strings.Contains(envs, "MARIADB_ROOT_PASSWORD="+cfg.RootPassword) || !strings.Contains(envs, "MARIADB_AUTO_UPGRADE=1") {
		t.Fatalf("database env: %v", db.Spec.Env)
	}

	// Start order: database before php before web.
	calls := strings.Join(e.engine.Calls, " ")
	if strings.Index(calls, "start:envoryx-shop-database") > strings.Index(calls, "start:envoryx-shop-php") {
		t.Fatalf("database must start before php: %s", calls)
	}

	// PHP receives connection variables; user variables win.
	php, _ := e.engine.Container("envoryx-shop-php")
	phpEnv := strings.Join(php.Spec.Env, "\n")
	for _, want := range []string{"DB_HOST=database", "DB_PORT=3306", "DB_DATABASE=shop", "DB_USERNAME=shop", "DB_PASSWORD=" + cfg.Password, "DATABASE_URL=mysql://shop:" + cfg.Password + "@database:3306/shop", "APP_ENV=local"} {
		if !strings.Contains(phpEnv, want) {
			t.Errorf("php env missing %q", want)
		}
	}
	if len(view.Status.Services) != 3 || view.Status.Services[0].Kind != store.ServiceDatabase {
		t.Fatalf("status services: %+v", view.Status.Services)
	}

	info, err := e.m.DatabaseInfo(ctx, p.ID)
	if err != nil || info.Database != "shop" || info.HostPort != 20001 || !info.VolumeExists || info.State != "running" {
		t.Fatalf("info: %+v %v", info, err)
	}
	creds, err := e.m.DatabaseCredentials(ctx, p.ID)
	if err != nil || creds.Password != cfg.Password || creds.RootPassword != cfg.RootPassword {
		t.Fatalf("credentials: %+v %v", creds, err)
	}
	entries, _ := e.store.Audit.Recent(ctx, 5)
	if entries[0].Action != "database.credentials_viewed" || strings.Contains(string(entries[0].Details), cfg.Password) {
		t.Fatalf("credential access must be audited without secrets: %+v", entries[0])
	}
}

func TestUserEnvOverridesDatabaseDefaults(t *testing.T) {
	e := newEnv(t)
	req := dbRequest("Custom", true)
	req.Env = append(req.Env, EnvVarRequest{Key: "DB_DATABASE", Value: "legacy"})
	if _, err := e.m.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	php, _ := e.engine.Container("envoryx-custom-php")
	count := 0
	for _, kv := range php.Spec.Env {
		if strings.HasPrefix(kv, "DB_DATABASE=") {
			count++
			if kv != "DB_DATABASE=legacy" {
				t.Fatalf("user value must win: %s", kv)
			}
		}
	}
	if count != 1 {
		t.Fatalf("DB_DATABASE must appear exactly once, got %d", count)
	}
	req2 := dbRequest("Reserved", false)
	req2.Env = append(req2.Env, EnvVarRequest{Key: "MARIADB_ROOT_PASSWORD", Value: "x"})
	if _, err := e.m.Create(context.Background(), req2); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("MARIADB_* must be reserved, got %v", err)
	}
}

func TestDatabaseOperationsViaExec(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var statements []string
	var envSeen []string
	e.engine.ExecHandler = func(container string, cmd []string, env []string) (docker.ExecResult, error) {
		statements = append(statements, cmd[len(cmd)-1])
		envSeen = append(envSeen, env...)
		if strings.HasPrefix(cmd[len(cmd)-1], "SHOW DATABASES") {
			return docker.ExecResult{Stdout: "information_schema\nshop\nmysql\nperformance_schema\nreports\nsys\n"}, nil
		}
		if strings.Contains(cmd[len(cmd)-1], "`broken`") {
			return docker.ExecResult{ExitCode: 1, Stderr: "ERROR 1007 (HY000): Can't create database 'broken'; database exists"}, nil
		}
		return docker.ExecResult{}, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	creds, _ := e.m.DatabaseCredentials(ctx, id)

	dbs, err := e.m.ListDatabases(ctx, id)
	if err != nil || strings.Join(dbs, ",") != "reports,shop" {
		t.Fatalf("list: %v %v", dbs, err)
	}
	if err := e.m.CreateDatabase(ctx, id, "reports_v2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statements[len(statements)-1], "CREATE DATABASE `reports_v2`") || !strings.Contains(statements[len(statements)-1], "GRANT ALL PRIVILEGES ON `reports_v2`.* TO 'shop'@'%'") {
		t.Fatalf("create statement: %s", statements[len(statements)-1])
	}
	for _, bad := range []string{"Bad Name", "mysql", "1abc", "a;drop", "../x"} {
		if err := e.m.CreateDatabase(ctx, id, bad); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
	if err := e.m.CreateDatabase(ctx, id, "broken"); err == nil || !strings.Contains(err.Error(), "database exists") {
		t.Fatalf("server errors must surface: %v", err)
	}
	if err := e.m.DropDatabase(ctx, id, "reports", "nope"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("drop needs confirmation, got %v", err)
	}
	if err := e.m.DropDatabase(ctx, id, "shop", "shop"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("primary database must be protected, got %v", err)
	}
	if err := e.m.DropDatabase(ctx, id, "reports", "reports"); err != nil {
		t.Fatal(err)
	}
	if statements[len(statements)-1] != "DROP DATABASE `reports`" {
		t.Fatalf("drop statement: %s", statements[len(statements)-1])
	}
	// The root password travels through the environment, never in argv.
	for _, st := range statements {
		if strings.Contains(st, creds.RootPassword) {
			t.Fatal("root password leaked into a statement")
		}
	}
	if !strings.Contains(strings.Join(envSeen, ","), "MYSQL_PWD="+creds.RootPassword) {
		t.Fatal("root password must be passed via MYSQL_PWD")
	}

	// Rotation: new password on the server, stored, php recreated with it.
	phpBefore, _ := e.engine.Container("envoryx-shop-php")
	view, err = e.m.RotateDatabasePassword(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := e.m.DatabaseCredentials(ctx, id)
	if after.Password == creds.Password || len(after.Password) != runtime.PasswordLength {
		t.Fatalf("password not rotated: %q", after.Password)
	}
	if !strings.Contains(statements[len(statements)-1], "ALTER USER 'shop'@'%' IDENTIFIED BY '"+after.Password+"'") {
		t.Fatalf("alter statement: %s", statements[len(statements)-1])
	}
	phpAfter, _ := e.engine.Container("envoryx-shop-php")
	if phpAfter.ID == phpBefore.ID || !strings.Contains(strings.Join(phpAfter.Spec.Env, ","), "DB_PASSWORD="+after.Password) || phpAfter.State != "running" {
		t.Fatalf("php must be recreated with the new password: %+v", phpAfter.Spec.Env)
	}
	dbc, _ := e.engine.Container("envoryx-shop-database")
	if dbc.State != "running" {
		t.Fatal("database container must keep running during rotation")
	}
	if view.Status.State != StateRunning {
		t.Fatalf("project state after rotation: %s", view.Status.State)
	}
}

func TestDatabaseOperationsRequireRunningContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, dbRequest("Idle", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Stop(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.ListDatabases(ctx, view.Project.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict while stopped, got %v", err)
	}
	plain, _ := e.m.Create(ctx, phpRequest("NoDB", false))
	if _, err := e.m.DatabaseInfo(ctx, plain.Project.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found for project without database, got %v", err)
	}
}

func TestAddChangeAndRemoveDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Grow", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID

	// Add
	view, err = e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Type: "mariadb", Version: "10.11"}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Service(store.ServiceDatabase) == nil || view.Status.State != StateRunning || len(view.Status.Services) != 3 {
		t.Fatalf("add database: %+v", view.Status)
	}
	if got := strings.Join(e.engine.VolumeNames(), ","); got != "envoryx-grow-database" {
		t.Fatalf("volume missing: %s", got)
	}
	php, _ := e.engine.Container("envoryx-grow-php")
	if !strings.Contains(strings.Join(php.Spec.Env, ","), "DB_HOST=database") {
		t.Fatal("php must be recreated with database env")
	}

	// Downgrade refused, upgrade allowed
	if _, err := e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Version: "10.6"}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("downgrade must be refused, got %v", err)
	}
	view, err = e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Version: "11", ExposePort: true}})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := e.engine.Container("envoryx-grow-database")
	if db.Spec.Image != "mariadb:11" || len(db.Spec.Ports) != 1 || db.State != "running" {
		t.Fatalf("upgrade + expose: %+v", db.Spec)
	}
	if got := strings.Join(e.engine.VolumeNames(), ","); got != "envoryx-grow-database" {
		t.Fatalf("volume must survive upgrades: %s", got)
	}
	// The upgrade took a database dump first.
	backups, err := e.m.ListBackups(ctx, id)
	if err != nil || len(backups) != 1 || backups[0].Meta.Source != "upgrade" || backups[0].Kind != "database" || !strings.Contains(backups[0].Meta.Note, "10.11 → 11") {
		t.Fatalf("upgrade backup: %+v, %v", backups, err)
	}
	// Without a running database container no dump is possible – then no upgrade either.
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Version: "11.4", ExposePort: true}}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "upgrade refused") {
		t.Fatalf("upgrade of a stopped database must be refused, got %v", err)
	}
	if view, _ := e.m.Get(ctx, id); view.Project.Service(store.ServiceDatabase).Version != "11" {
		t.Fatal("refused upgrade must not change the recorded version")
	}
	if _, err := e.m.Start(ctx, id); err != nil {
		t.Fatal(err)
	}

	// Remove without confirmation refused; with confirmation removes container + volume.
	if _, err := e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: false}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("removal must require removeData, got %v", err)
	}
	view, err = e.m.Update(ctx, id, UpdateRequest{Database: &DatabaseUpdate{Enabled: false, RemoveData: true}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Project.Service(store.ServiceDatabase) != nil || len(e.engine.VolumeNames()) != 0 || len(view.Status.Services) != 2 {
		t.Fatalf("remove database: %+v volumes=%v", view.Status, e.engine.VolumeNames())
	}
	if _, ok := e.engine.Container("envoryx-grow-database"); ok {
		t.Fatal("database container must be removed")
	}
	php, _ = e.engine.Container("envoryx-grow-php")
	if strings.Contains(strings.Join(php.Spec.Env, ","), "DB_HOST=") {
		t.Fatal("php env must no longer contain database variables")
	}
}

func TestDeleteProjectRemovesDatabaseVolume(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, dbRequest("Gone", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.Delete(ctx, view.Project.ID, DeleteOptions{Confirm: "gone"}); err != nil {
		t.Fatal(err)
	}
	if len(e.engine.VolumeNames()) != 0 || len(e.engine.ContainerNames()) != 0 {
		t.Fatalf("leftovers: %v %v", e.engine.VolumeNames(), e.engine.ContainerNames())
	}
}

func TestRestartKeepsDatabaseVolume(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, dbRequest("Keep", true))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []func(context.Context, string) (View, error){e.m.Stop, e.m.Start, e.m.Restart} {
		if _, err := op(ctx, view.Project.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range e.engine.Calls {
		if strings.HasPrefix(c, "volume-remove:") {
			t.Fatalf("volume must never be removed by lifecycle operations: %v", e.engine.Calls)
		}
	}
}

func TestMongoDBProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var cmds [][]string
	e.engine.ExecHandler = func(container string, cmd []string, env []string) (docker.ExecResult, error) {
		cmds = append(cmds, cmd)
		for _, kv := range env {
			if strings.HasPrefix(kv, "ENVORYX_MONGO_URI=mongodb://shop:") {
				if strings.Contains(cmd[len(cmd)-1], "listDatabases") {
					return docker.ExecResult{Stdout: "admin\nconfig\nlocal\nshop\n"}, nil
				}
				return docker.ExecResult{}, nil
			}
		}
		return docker.ExecResult{ExitCode: 1, Stderr: "no credentials"}, nil
	}
	req := phpRequest("Shop", true)
	req.Database = &DatabaseRequest{Type: "mongodb", ExposePort: true}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := e.engine.Container("envoryx-shop-database")
	if !ok || c.Spec.Image != "mongo:8.2" || len(c.Spec.Cmd) != 0 || c.Spec.Ports[0].ContainerPort != 27017 || c.Spec.Mounts[0].Target != "/data/db" {
		t.Fatalf("mongo container: %+v", c.Spec)
	}
	var hasRootUser bool
	for _, kv := range c.Spec.Env {
		hasRootUser = hasRootUser || strings.HasPrefix(kv, "MONGO_INITDB_ROOT_USERNAME=shop")
	}
	if !hasRootUser {
		t.Fatalf("mongo env: %v", c.Spec.Env)
	}
	php, _ := e.engine.Container("envoryx-shop-php")
	var uri, conn string
	for _, kv := range php.Spec.Env {
		if strings.HasPrefix(kv, "MONGODB_URI=") {
			uri = kv
		}
		if strings.HasPrefix(kv, "DB_CONNECTION=") {
			conn = kv
		}
	}
	if !strings.HasPrefix(uri, "MONGODB_URI=mongodb://shop:") || !strings.HasSuffix(uri, "@database:27017/shop?authSource=admin") || conn != "DB_CONNECTION=mongodb" {
		t.Fatalf("injected env: %q %q", uri, conn)
	}
	dbs, err := e.m.ListDatabases(ctx, view.Project.ID)
	if err != nil || strings.Join(dbs, ",") != "shop" {
		t.Fatalf("list: %v %v", dbs, err)
	}
	if err := e.m.CreateDatabase(ctx, view.Project.ID, "analytics"); err != nil {
		t.Fatal(err)
	}
	last := cmds[len(cmds)-1]
	if last[0] != "mongosh" || !strings.Contains(last[len(last)-1], "getDB('analytics').createCollection") {
		t.Fatalf("create: %v", last)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Type: "mongodb", Version: "7"}}); err == nil {
		t.Fatal("downgrade must be refused")
	}
}

func TestDatabaseEnvReachesNodeApplication(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := nodeRequest("Shop", true)
	req.Database = &DatabaseRequest{Type: "postgresql"}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var cfg runtime.DatabaseConfig
	_ = json.Unmarshal(view.Project.Service(store.ServiceDatabase).Config, &cfg)
	node, _ := e.engine.Container("envoryx-shop-node")
	env := strings.Join(node.Spec.Env, "\n")
	for _, want := range []string{"DB_HOST=database", "DB_PORT=5432", "DB_DATABASE=shop", "DATABASE_URL=pgsql://shop:" + cfg.Password + "@database:5432/shop"} {
		if !strings.Contains(env, want) {
			t.Errorf("node env missing %q", want)
		}
	}
	calls := strings.Join(e.engine.Calls, " ")
	if strings.Index(calls, "start:envoryx-shop-database") > strings.Index(calls, "start:envoryx-shop-node") {
		t.Fatalf("database must start before node: %s", calls)
	}
	if info, err := e.m.DatabaseInfo(ctx, view.Project.ID); err != nil || info.State != "running" {
		t.Fatalf("info: %+v %v", info, err)
	}
}
