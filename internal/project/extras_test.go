package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/docker/dockertest"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// PostgreSQL 18 stores its cluster in a version subdirectory and the image refuses to
// start when a volume sits on the old /var/lib/postgresql/data, so the volume has to take
// the whole directory from 18 on.
func TestPostgres18MountsTheWholeDataDirectory(t *testing.T) {
	e := newEnv(t)
	req := phpRequest("Shop PG18", true)
	req.Database = &DatabaseRequest{Type: "postgresql", Version: "18"}
	if _, err := e.m.Create(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	db, _ := e.engine.Container("envoryx-shop-pg18-database")
	if db.Spec.Image != "postgres:18-alpine" || db.Spec.Mounts[0].Target != "/var/lib/postgresql" {
		t.Fatalf("postgres 18 container: image %s, mounts %+v", db.Spec.Image, db.Spec.Mounts)
	}
}

func TestPostgresAndMySQLDialects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var cmds [][]string
	var envs [][]string
	e.engine.ExecHandler = func(_ string, cmd []string, env []string) (docker.ExecResult, error) {
		cmds = append(cmds, cmd)
		envs = append(envs, env)
		if strings.Contains(cmd[len(cmd)-1], "pg_database") {
			return docker.ExecResult{Stdout: "postgres\nshop_pg\nreports\n"}, nil
		}
		return docker.ExecResult{}, nil
	}

	req := phpRequest("Shop PG", true)
	req.Database = &DatabaseRequest{Type: "postgresql", Version: "17"}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := e.engine.Container("envoryx-shop-pg-database")
	env := strings.Join(db.Spec.Env, "\n")
	if db.Spec.Image != "postgres:17-alpine" || !strings.Contains(env, "POSTGRES_USER=shop_pg") || db.Spec.Mounts[0].Target != "/var/lib/postgresql/data" || db.Spec.Healthcheck.Test[0] != "pg_isready" {
		t.Fatalf("postgres container: %+v", db.Spec)
	}
	php, _ := e.engine.Container("envoryx-shop-pg-php")
	phpEnv := strings.Join(php.Spec.Env, "\n")
	if !strings.Contains(phpEnv, "DB_CONNECTION=pgsql") || !strings.Contains(phpEnv, "DB_PORT=5432") || !strings.Contains(phpEnv, "DATABASE_URL=pgsql://shop_pg:") {
		t.Fatalf("php env for postgres: %v", php.Spec.Env)
	}
	creds, _ := e.m.DatabaseCredentials(ctx, view.Project.ID)
	if creds.Port != 5432 || creds.RootPassword != "" {
		t.Fatalf("postgres credentials: %+v", creds)
	}
	dbs, err := e.m.ListDatabases(ctx, view.Project.ID)
	if err != nil || strings.Join(dbs, ",") != "reports,shop_pg" {
		t.Fatalf("list: %v %v", dbs, err)
	}
	if cmds[0][0] != "psql" || !strings.Contains(strings.Join(envs[0], ","), "PGPASSWORD="+creds.Password) {
		t.Fatalf("psql invocation: %v %v", cmds[0], envs[0])
	}
	if err := e.m.CreateDatabase(ctx, view.Project.ID, "analytics"); err != nil {
		t.Fatal(err)
	}
	if last := cmds[len(cmds)-1]; last[len(last)-1] != `CREATE DATABASE "analytics" OWNER "shop_pg" ENCODING 'UTF8'` {
		t.Fatalf("create statement: %s", last[len(last)-1])
	}
	// Major upgrades are refused for PostgreSQL.
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Version: "18"}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("postgres major upgrade must be refused, got %v", err)
	}

	// MySQL
	req2 := phpRequest("Shop My", true)
	req2.Database = &DatabaseRequest{Type: "mysql", Version: "8.0"}
	view2, err := e.m.Create(ctx, req2)
	if err != nil {
		t.Fatal(err)
	}
	my, _ := e.engine.Container("envoryx-shop-my-database")
	if my.Spec.Image != "mysql:8.0" || !strings.Contains(strings.Join(my.Spec.Env, "\n"), "MYSQL_ROOT_PASSWORD=") || my.Spec.Healthcheck.Test[0] != "mysqladmin" {
		t.Fatalf("mysql container: %+v", my.Spec)
	}
	if _, err := e.m.Update(ctx, view2.Project.ID, UpdateRequest{Database: &DatabaseUpdate{Enabled: true, Version: "8.4"}}); err != nil {
		t.Fatalf("mysql in-place upgrade must work: %v", err)
	}
	my, _ = e.engine.Container("envoryx-shop-my-database")
	if my.Spec.Image != "mysql:8.4" {
		t.Fatalf("mysql image after upgrade: %s", my.Spec.Image)
	}
	req3 := phpRequest("Bad", true)
	req3.Database = &DatabaseRequest{Type: "oracle"}
	if _, err := e.m.Create(ctx, req3); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("unknown db type must be rejected, got %v", err)
	}
}

func TestRedisAndMailpitServices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Full", true)
	req.Database = &DatabaseRequest{Type: "mariadb", ExposePort: true}
	req.Redis = &ExtraRequest{Version: "8", ExposePort: true}
	req.Mailpit = &ExtraRequest{}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Status.Services) != 5 {
		t.Fatalf("expected 5 services, got %+v", view.Status.Services)
	}
	redis, _ := e.engine.Container("envoryx-full-redis")
	mail, _ := e.engine.Container("envoryx-full-mailpit")
	if redis.Spec.Image != "redis:8-alpine" || redis.Spec.Mounts[0].Source != "envoryx-full-redis" || redis.Spec.Ports[0].ContainerPort != 6379 {
		t.Fatalf("redis: %+v", redis.Spec)
	}
	if mail.Spec.Image != "axllent/mailpit:v1.31" || mail.Spec.Ports[0].ContainerPort != 8025 || len(mail.Spec.Mounts) != 0 {
		t.Fatalf("mailpit: %+v", mail.Spec)
	}
	// Distinct host ports for web, db, redis, mailpit.
	ports := map[int]bool{view.Project.HTTPPort: true}
	for _, c := range []dockerContainer{redis, mail} {
		if ports[c.Spec.Ports[0].HostPort] {
			t.Fatalf("duplicate host port %d", c.Spec.Ports[0].HostPort)
		}
		ports[c.Spec.Ports[0].HostPort] = true
	}
	if got := strings.Join(e.engine.VolumeNames(), ","); got != "envoryx-full-database,envoryx-full-redis" {
		t.Fatalf("volumes: %s", got)
	}
	php, _ := e.engine.Container("envoryx-full-php")
	env := strings.Join(php.Spec.Env, "\n")
	for _, want := range []string{"REDIS_HOST=redis", "REDIS_URL=redis://redis:6379", "MAIL_HOST=mailpit", "MAIL_PORT=1025", "MAILER_DSN=smtp://mailpit:1025", "DB_HOST=database"} {
		if !strings.Contains(env, want) {
			t.Errorf("php env missing %s", want)
		}
	}
	extras, err := e.m.ExtraServices(ctx, view.Project.ID)
	if err != nil || len(extras) != 2 || extras[0].Kind != store.ServiceRedis || extras[1].WebUIPort == 0 || extras[0].State != "running" {
		t.Fatalf("extras: %+v %v", extras, err)
	}

	// Removing redis needs confirmation, then removes container + volume and PHP env.
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Redis: &ExtraUpdate{Enabled: false}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("redis removal must require removeData, got %v", err)
	}
	view, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Redis: &ExtraUpdate{Enabled: false, RemoveData: true}, Mailpit: &ExtraUpdate{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.engine.VolumeNames(), ","); got != "envoryx-full-database" {
		t.Fatalf("volumes after removal: %s", got)
	}
	if len(view.Status.Services) != 3 {
		t.Fatalf("services after removal: %+v", view.Status.Services)
	}
	php, _ = e.engine.Container("envoryx-full-php")
	if strings.Contains(strings.Join(php.Spec.Env, "\n"), "REDIS_HOST") || strings.Contains(strings.Join(php.Spec.Env, "\n"), "MAIL_HOST") {
		t.Fatal("php env must drop service variables")
	}
	// Add again later without exposing redis.
	view, err = e.m.Update(ctx, view.Project.ID, UpdateRequest{Redis: &ExtraUpdate{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	redis, _ = e.engine.Container("envoryx-full-redis")
	if redis.State != "running" || len(redis.Spec.Ports) != 0 {
		t.Fatalf("redis re-added: %+v", redis)
	}
	// Restart never removes volumes.
	if _, err := e.m.Restart(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.engine.Calls {
		if strings.HasPrefix(c, "volume-remove:") && !strings.HasSuffix(c, "envoryx-full-redis") {
			t.Fatalf("unexpected volume removal: %s", c)
		}
	}
}

type dockerContainer = dockertest.FakeContainer

func TestRedisAndMailpitEnvReachNodeApplication(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := nodeRequest("Shop", true)
	req.Redis = &ExtraRequest{}
	req.Mailpit = &ExtraRequest{}
	if _, err := e.m.Create(ctx, req); err != nil {
		t.Fatal(err)
	}
	node, _ := e.engine.Container("envoryx-shop-node")
	env := strings.Join(node.Spec.Env, "\n")
	for _, want := range []string{"REDIS_URL=redis://redis:6379", "MAIL_HOST=mailpit", "MAILER_DSN=smtp://mailpit:1025", "SMTP_HOST=mailpit", "SMTP_PORT=1025"} {
		if !strings.Contains(env, want) {
			t.Errorf("node env missing %q", want)
		}
	}
	// The web container only serves files and never receives service variables.
	web, _ := e.engine.Container("envoryx-shop-web")
	if strings.Contains(strings.Join(web.Spec.Env, "\n"), "REDIS_URL") {
		t.Fatalf("web env: %v", web.Spec.Env)
	}
}

// RabbitMQ keeps its data under a fixed node name, always publishes the management UI,
// publishes AMQP only on request and gives the application a login of its own.
func TestRabbitMQService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Queue", true)
	req.RabbitMQ = &ExtraRequest{ExposePort: true}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	rq, ok := e.engine.Container("envoryx-queue-rabbitmq")
	if !ok {
		t.Fatal("no rabbitmq container")
	}
	if rq.Spec.Image != "rabbitmq:4.3-management-alpine" || rq.Spec.Mounts[0].Source != "envoryx-queue-rabbitmq" || rq.Spec.Mounts[0].Target != "/var/lib/rabbitmq" {
		t.Fatalf("rabbitmq: %+v", rq.Spec)
	}
	if len(rq.Spec.Ports) != 2 || rq.Spec.Ports[0].ContainerPort != 5672 || rq.Spec.Ports[1].ContainerPort != 15672 || rq.Spec.Ports[0].HostPort == rq.Spec.Ports[1].HostPort || rq.Spec.Ports[1].HostPort == view.Project.HTTPPort {
		t.Fatalf("rabbitmq ports: %+v", rq.Spec.Ports)
	}
	creds, err := e.m.RabbitMQCredentials(ctx, view.Project.ID)
	if err != nil || creds.Username != "envoryx" || len(creds.Password) < 20 {
		t.Fatalf("credentials: %+v %v", creds, err)
	}
	rqEnv := strings.Join(rq.Spec.Env, "\n")
	for _, want := range []string{"RABBITMQ_NODENAME=rabbit@localhost", "RABBITMQ_DEFAULT_USER=envoryx", "RABBITMQ_DEFAULT_PASS=" + creds.Password} {
		if !strings.Contains(rqEnv, want) {
			t.Errorf("rabbitmq env missing %s", want)
		}
	}
	php, _ := e.engine.Container("envoryx-queue-php")
	env := strings.Join(php.Spec.Env, "\n")
	for _, want := range []string{"RABBITMQ_HOST=rabbitmq", "RABBITMQ_PORT=5672", "RABBITMQ_USER=envoryx", "RABBITMQ_PASSWORD=" + creds.Password, "RABBITMQ_VHOST=/", "RABBITMQ_URL=amqp://envoryx:" + creds.Password + "@rabbitmq:5672/%2f"} {
		if !strings.Contains(env, want) {
			t.Errorf("php env missing %s", want)
		}
	}
	extras, err := e.m.ExtraServices(ctx, view.Project.ID)
	if err != nil || len(extras) != 1 || extras[0].WebUIPort != rq.Spec.Ports[1].HostPort || extras[0].HostPort != rq.Spec.Ports[0].HostPort || extras[0].Username != "envoryx" || extras[0].VolumeName == "" {
		t.Fatalf("extras: %+v %v", extras, err)
	}

	// Unpublishing AMQP keeps the management UI.
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{RabbitMQ: &ExtraUpdate{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	rq, _ = e.engine.Container("envoryx-queue-rabbitmq")
	if len(rq.Spec.Ports) != 1 || rq.Spec.Ports[0].ContainerPort != 15672 || rq.Spec.Ports[0].HostPort != extras[0].WebUIPort {
		t.Fatalf("ports after unpublishing AMQP: %+v", rq.Spec.Ports)
	}

	// Removal needs confirmation and takes the volume and the variables with it.
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{RabbitMQ: &ExtraUpdate{Enabled: false}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("rabbitmq removal must require removeData, got %v", err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{RabbitMQ: &ExtraUpdate{Enabled: false, RemoveData: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(e.engine.VolumeNames(), ","), "rabbitmq") {
		t.Fatalf("volumes after removal: %v", e.engine.VolumeNames())
	}
	php, _ = e.engine.Container("envoryx-queue-php")
	if strings.Contains(strings.Join(php.Spec.Env, "\n"), "RABBITMQ_") {
		t.Fatal("php env must drop the RabbitMQ variables")
	}
	if _, err := e.m.RabbitMQCredentials(ctx, view.Project.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("credentials without rabbitmq: %v", err)
	}

	// Added again later: a fresh password and a management UI port, AMQP unpublished.
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{RabbitMQ: &ExtraUpdate{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	rq, _ = e.engine.Container("envoryx-queue-rabbitmq")
	if rq.State != "running" || len(rq.Spec.Ports) != 1 || rq.Spec.Ports[0].ContainerPort != 15672 {
		t.Fatalf("rabbitmq re-added: %+v", rq)
	}
}

// Memcached has no volume and no login: removing it needs no confirmation, and the
// application gets host, port and a DSN.
func TestMemcachedService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Cache", true)
	req.Memcached = &ExtraRequest{ExposePort: true}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	mc, ok := e.engine.Container("envoryx-cache-memcached")
	if !ok {
		t.Fatal("no memcached container")
	}
	if mc.Spec.Image != "memcached:1.6-alpine" || len(mc.Spec.Mounts) != 0 || len(mc.Spec.Ports) != 1 || mc.Spec.Ports[0].ContainerPort != 11211 || mc.Spec.Ports[0].HostPort == view.Project.HTTPPort {
		t.Fatalf("memcached: %+v", mc.Spec)
	}
	if len(e.engine.VolumeNames()) != 0 {
		t.Fatalf("memcached must not create a volume: %v", e.engine.VolumeNames())
	}
	php, _ := e.engine.Container("envoryx-cache-php")
	env := strings.Join(php.Spec.Env, "\n")
	for _, want := range []string{"MEMCACHED_HOST=memcached", "MEMCACHED_PORT=11211", "MEMCACHED_URL=memcached://memcached:11211"} {
		if !strings.Contains(env, want) {
			t.Errorf("php env missing %s", want)
		}
	}
	extras, err := e.m.ExtraServices(ctx, view.Project.ID)
	if err != nil || len(extras) != 1 || extras[0].Kind != store.ServiceMemcached || extras[0].Port != 11211 || extras[0].HostPort != mc.Spec.Ports[0].HostPort || extras[0].VolumeName != "" {
		t.Fatalf("extras: %+v %v", extras, err)
	}
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{Memcached: &ExtraUpdate{Enabled: false}}); err != nil {
		t.Fatalf("removing memcached needs no confirmation: %v", err)
	}
	if _, ok := e.engine.Container("envoryx-cache-memcached"); ok {
		t.Fatal("memcached container left behind")
	}
	php, _ = e.engine.Container("envoryx-cache-php")
	if strings.Contains(strings.Join(php.Spec.Env, "\n"), "MEMCACHED_") {
		t.Fatal("php env must drop the Memcached variables")
	}
}
