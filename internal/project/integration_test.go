//go:build integration

// Integration tests for the project lifecycle against a real Docker Engine. Run with:
//
//	go test -tags integration ./internal/project/
//
// Unit tests drive the planner through a fake engine, which proves what Envoryx asks
// Docker for – not whether the images accept it. Every stateful service (database, Redis,
// Mailpit) was therefore never actually started by any test, and that is where
// PostgreSQL 18 could ship broken: the image changed the directory it keeps its cluster
// in and refused to start on the old mount, while the unit test pinned version 17 and saw
// nothing. These tests use the catalogue's *default* versions on purpose, so upstream
// drift in what a new project gets fails here first.
//
// Everything created is labelled envoryx.managed=true and removed again, including the
// volumes. Object storage is left out: internal/s3 already provisions a real RustFS.
package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// integrationManager wires a Manager to the real engine. Containers run as the test user
// so the files they leave behind in the temporary project directory can be cleaned up.
func integrationManager(t *testing.T) *Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	engine, err := docker.Connect(docker.Options{}, log)
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := engine.Ping(ctx); err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)

	// The test runs on the host, so the paths Envoryx hands to Docker are its own.
	cfgDir, projDir := t.TempDir(), t.TempDir()
	paths := func() (Paths, error) {
		return Paths{
			ConfigDir: cfgDir, ConfigHostDir: cfgDir,
			ProjectsDir: projDir, ProjectsHostDir: projDir,
			PUID: os.Getuid(), PGID: os.Getgid(),
			EnvoryxVersion: "integration", BaseDomain: "test",
			PublishInterface: "127.0.0.1",
		}, nil
	}
	return NewManager(st, engine, runtime.Default(), paths, audit.New(st.Audit, log), Config{PortRangeStart: 21000, PortRangeEnd: 21050}, log)
}

// errTerminal marks a state that will not improve, so the wait reports it at once
// instead of sitting out its timeout – a container the restart policy keeps restarting is
// crash-looping, not starting up, and on a slow CI runner that difference is minutes.
var errTerminal = errors.New("terminal state")

// waitFor polls until fn succeeds. Pulling the images on a cold host is the slow part, so
// the timeouts are generous; errTerminal cuts the wait short.
func waitFor(t *testing.T, what string, timeout time.Duration, fn func() error) {
	t.Helper()
	deadline, last := time.Now().Add(timeout), error(nil)
	for time.Now().Before(deadline) {
		if last = fn(); last == nil {
			return
		}
		if errors.Is(last, errTerminal) {
			t.Fatalf("%s: %v", what, last)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s: %v", what, last)
}

// crashLooping reports a container the restart policy keeps restarting – no wait will
// outlive that. One sample is not enough: between the exit and the next restart Docker
// reports the container as running, so a crash-looping service can look healthy for a
// moment. seen counts the observations across polls.
func crashLooping(t *testing.T, m *Manager, id string, seen map[store.ServiceKind]int) error {
	t.Helper()
	v, err := m.Get(context.Background(), id)
	if err != nil {
		return nil // the caller's own poll reports it
	}
	for _, s := range v.Status.Services {
		if s.State != "restarting" {
			continue
		}
		if seen[s.Kind]++; seen[s.Kind] > 1 {
			return fmt.Errorf("%w: %s crash-loops on %s\n%s", errTerminal, s.Kind, s.Image, containerTail(t, m, id, s.Kind))
		}
	}
	return nil
}

// containerTail returns the last log lines of a service, so a failure says why.
func containerTail(t *testing.T, m *Manager, id string, kind store.ServiceKind) string {
	t.Helper()
	lines, err := m.TailLogs(context.Background(), id, kind, 15)
	if err != nil {
		return "(no logs: " + err.Error() + ")"
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return strings.Join(out, "\n")
}

// TestIntegrationStatefulServices starts a project with the services no other test ever
// starts and checks the two things that only a real engine can answer: do the images
// accept the way Envoryx mounts and configures them, and does the data survive a
// container that is thrown away and rebuilt from the plan?
//
// Every engine's tag floats (mariadb:11 is rolling, the others move with patch releases),
// so all four are worth the check – but pulling them is gigabytes. PostgreSQL runs on
// every push, the rest when ENVORYX_TEST_ALL_DATABASES is set, which the scheduled run
// does.
func TestIntegrationStatefulServices(t *testing.T) {
	engines := []string{"postgresql"}
	if os.Getenv("ENVORYX_TEST_ALL_DATABASES") != "" {
		engines = append(engines, "mariadb", "mysql", "mongodb")
	}
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) { statefulServices(t, engine) })
	}
}

func statefulServices(t *testing.T, engine string) {
	m := integrationManager(t)
	ctx := context.Background()

	// A static project: the point is the services, not a language runtime, and this keeps
	// the images small. The default database version is what a new project gets.
	req := CreateRequest{
		Name:          "Envoryx Integration " + engine,
		CreateStarter: true,
		Start:         true,
		Database:      &DatabaseRequest{Type: engine},
		Redis:         &ExtraRequest{},
		Mailpit:       &ExtraRequest{},
	}
	view, err := m.Create(ctx, req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, slug := view.Project.ID, view.Project.Slug
	t.Cleanup(func() {
		if err := m.Delete(context.Background(), id, DeleteOptions{Confirm: slug, DeleteFiles: true}); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	dbSvc := view.Project.Service(store.ServiceDatabase)
	if dbSvc == nil {
		t.Fatal("no database service")
	}
	t.Logf("database image %s (catalogue default %s)", dbSvc.Image, dbSvc.Version)

	// Every enabled service has to reach running – a container that crash-loops on its
	// volume (PostgreSQL 18) never gets there.
	// Shared across every wait below: a service that crash-loops fails them all, and the
	// count has to survive the individual waits to be meaningful.
	restarting := map[store.ServiceKind]int{}
	waitFor(t, "all services healthy", 5*time.Minute, func() error {
		if err := crashLooping(t, m, id, restarting); err != nil {
			return err
		}
		v, err := m.Get(ctx, id)
		if err != nil {
			return err
		}
		for _, s := range v.Status.Services {
			if s.State != "running" {
				return fmt.Errorf("%s is %s", s.Kind, s.State)
			}
			// Running is not ready: the database images start a setup server first and
			// restart the real one behind it, so connections are refused in between.
			// Where the image defines a check, that is the readiness signal.
			if s.Health != "" && s.Health != "healthy" {
				return fmt.Errorf("%s is %s (health %s)", s.Kind, s.State, s.Health)
			}
		}
		if v.Status.State != StateRunning {
			return fmt.Errorf("project is %s", v.Status.State)
		}
		return nil
	})

	// The server accepts connections and Envoryx's own statements work against it. A
	// container that exits right after start passes the check above in the moment it is
	// up, so this wait needs the same guard.
	waitFor(t, "database answers", 2*time.Minute, func() error {
		if err := crashLooping(t, m, id, restarting); err != nil {
			return err
		}
		_, err := m.ListDatabases(ctx, id)
		return err
	})
	if err := m.CreateDatabase(ctx, id, "persisted"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	// Throw the container away and let the plan rebuild it: the volume carries the data.
	containers, err := m.engine.ListContainers(ctx, true, id)
	if err != nil {
		t.Fatal(err)
	}
	var removed string
	for _, c := range containers {
		if c.Service() == string(store.ServiceDatabase) {
			removed = c.ID
			if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
				t.Fatalf("remove database container: %v", err)
			}
		}
	}
	if removed == "" {
		t.Fatal("no database container to remove")
	}
	if _, err := m.Start(ctx, id); err != nil {
		t.Fatalf("restart after removal: %v", err)
	}
	waitFor(t, "database answers after the rebuild", 2*time.Minute, func() error {
		if err := crashLooping(t, m, id, restarting); err != nil {
			return err
		}
		names, err := m.ListDatabases(ctx, id)
		if err != nil {
			return err
		}
		if !slices.Contains(names, "persisted") {
			return fmt.Errorf("database gone after the container was rebuilt: %v", names)
		}
		return nil
	})

	// It really is a different container, so the data came from the volume.
	containers, err = m.engine.ListContainers(ctx, true, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range containers {
		if c.Service() == string(store.ServiceDatabase) && c.ID == removed {
			t.Fatal("container was not recreated")
		}
	}
}

// sqlIn pipes statements into a project's primary database through the flavour's client –
// the same argv an import uses – and returns what it printed. It is how this test writes
// and reads rows without a driver, and it works for every SQL engine in the catalogue.
func sqlIn(t *testing.T, m *Manager, id, sql string) string {
	t.Helper()
	ctx := context.Background()
	p, err := m.loadProject(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	svc, cfg, err := databaseConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	dialect, err := dialectOf(svc)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.ServiceContainer(ctx, id, store.ServiceDatabase)
	if err != nil {
		t.Fatal(err)
	}
	argv, env := dialect.Restore(cfg)
	var out, errOut strings.Builder
	code, err := m.engine.ExecStream(ctx, c.ID, docker.ExecStreamOptions{
		Cmd: argv, Env: env, Stdin: strings.NewReader(sql), Stdout: &out, Stderr: &errOut,
	})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	if code != 0 {
		t.Fatalf("%s: exit %d: %s", sql, code, errOut.String())
	}
	return out.String()
}

// TestIntegrationDatabaseSnapshotAndClone drives a snapshot and a clone against real
// database containers: the unit tests fake the client, so only this says whether the
// flavour's own dump really lands in another project's database – one with a different
// database name and login than the dump was taken from – and whether a snapshot puts the
// state before it back.
func TestIntegrationDatabaseSnapshotAndClone(t *testing.T) {
	engines := []string{"postgresql"}
	if os.Getenv("ENVORYX_TEST_ALL_DATABASES") != "" {
		engines = append(engines, "mariadb", "mysql")
	}
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) { snapshotAndClone(t, engine) })
	}
}

func snapshotAndClone(t *testing.T, engine string) {
	m := integrationManager(t)
	ctx := context.Background()

	// Two static projects with a database each: source and target of the clone. Their
	// database names and logins differ, which is exactly what a dump has to survive.
	create := func(name string) store.Project {
		t.Helper()
		view, err := m.Create(ctx, CreateRequest{Name: name, CreateStarter: true, Start: true, Database: &DatabaseRequest{Type: engine}})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		id, slug := view.Project.ID, view.Project.Slug
		t.Cleanup(func() {
			if err := m.Delete(context.Background(), id, DeleteOptions{Confirm: slug, DeleteFiles: true}); err != nil {
				t.Errorf("cleanup %s: %v", slug, err)
			}
		})
		restarting := map[store.ServiceKind]int{}
		waitFor(t, slug+" answers", 5*time.Minute, func() error {
			if err := crashLooping(t, m, id, restarting); err != nil {
				return err
			}
			_, err := m.ListDatabases(ctx, id)
			return err
		})
		return view.Project
	}
	source := create("Envoryx Clone Source " + engine)
	target := create("Envoryx Clone Target " + engine)

	sqlIn(t, m, source.ID, "CREATE TABLE orders (note varchar(32)); INSERT INTO orders VALUES ('from-source');")
	sqlIn(t, m, target.ID, "CREATE TABLE orders (note varchar(32)); INSERT INTO orders VALUES ('from-target');")

	snapshot, err := m.CreateSnapshot(ctx, target.ID, "before the clone")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Kind != "database" || snapshot.Meta.Database == nil || snapshot.Meta.Database.Bytes == 0 {
		t.Fatalf("snapshot: %+v", snapshot)
	}

	res, err := m.CloneDatabase(ctx, target.ID, CloneDatabaseRequest{Source: source.ID, Snapshot: true, Confirm: target.Slug})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if res.Snapshot == nil || res.Source != source.Slug {
		t.Fatalf("clone result: %+v", res)
	}
	rows := sqlIn(t, m, target.ID, "SELECT note FROM orders;")
	if !strings.Contains(rows, "from-source") || strings.Contains(rows, "from-target") {
		t.Fatalf("the clone must replace the target's rows, got %q", rows)
	}

	if _, err := m.RestoreSnapshot(ctx, target.ID, snapshot.ID, target.Slug); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	rows = sqlIn(t, m, target.ID, "SELECT note FROM orders;")
	if !strings.Contains(rows, "from-target") || strings.Contains(rows, "from-source") {
		t.Fatalf("the snapshot must bring the state before the clone back, got %q", rows)
	}

	// Two snapshots of the target (one taken by hand, one by the clone), none of the
	// source: it is only ever read.
	if list, err := m.ListSnapshots(ctx, target.ID); err != nil || len(list) != 2 {
		t.Fatalf("snapshots of the target: %+v %v", list, err)
	}
	if list, err := m.ListSnapshots(ctx, source.ID); err != nil || len(list) != 0 {
		t.Fatalf("the source must not be snapshotted: %+v %v", list, err)
	}
}

// TestIntegrationRabbitMQ starts the catalogue's default RabbitMQ, checks the generated
// login and that a durable queue survives a rebuilt container. The image names its data
// directory after the node, whose default contains the container's random host name, so
// without a fixed node name every recreate would start from an empty broker.
func TestIntegrationRabbitMQ(t *testing.T) {
	m := integrationManager(t)
	ctx := context.Background()
	view, err := m.Create(ctx, CreateRequest{Name: "Envoryx Integration RabbitMQ", CreateStarter: true, Start: true, RabbitMQ: &ExtraRequest{}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, slug := view.Project.ID, view.Project.Slug
	t.Cleanup(func() {
		if err := m.Delete(context.Background(), id, DeleteOptions{Confirm: slug, DeleteFiles: true}); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	restarting := map[store.ServiceKind]int{}
	healthy := func() error {
		if err := crashLooping(t, m, id, restarting); err != nil {
			return err
		}
		v, err := m.Get(ctx, id)
		if err != nil {
			return err
		}
		for _, s := range v.Status.Services {
			// Not every engine reports health in its container list (see statefulServices);
			// the broker answering below is the readiness signal that always works.
			if s.Kind == store.ServiceRabbitMQ && (s.State != "running" || (s.Health != "" && s.Health != "healthy")) {
				return fmt.Errorf("rabbitmq is %s (health %s)", s.State, s.Health)
			}
		}
		return nil
	}
	ctl := func(args ...string) (string, error) {
		c, err := m.ServiceContainer(ctx, id, store.ServiceRabbitMQ)
		if err != nil {
			return "", err
		}
		res, err := m.engine.Exec(ctx, c.ID, args, nil)
		if err != nil {
			return "", err
		}
		if res.ExitCode != 0 {
			return "", fmt.Errorf("%v: exit %d: %s%s", args, res.ExitCode, res.Stdout, res.Stderr)
		}
		return res.Stdout, nil
	}
	creds, err := m.RabbitMQCredentials(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// The generated login works once the broker has initialised the volume.
	answers := func() error {
		if err := healthy(); err != nil {
			return err
		}
		_, err := ctl("rabbitmqctl", "authenticate_user", creds.Username, creds.Password)
		return err
	}
	waitFor(t, "rabbitmq accepts the generated login", 3*time.Minute, answers)
	// Through the management API with the generated login, which also proves the UI's
	// plugin is on. The plugin comes up a moment after the broker.
	waitFor(t, "queue declared through the management API", time.Minute, func() error {
		_, err := ctl("rabbitmqadmin", "--username", creds.Username, "--password", creds.Password, "declare", "queue", "--name", "persisted", "--durable", "true")
		return err
	})

	c, err := m.ServiceContainer(ctx, id, store.ServiceRabbitMQ)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
		t.Fatalf("remove rabbitmq container: %v", err)
	}
	if _, err := m.Start(ctx, id); err != nil {
		t.Fatalf("restart after removal: %v", err)
	}
	waitFor(t, "rabbitmq answers after the rebuild", 3*time.Minute, answers)
	out, err := ctl("rabbitmqctl", "-q", "list_queues", "name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "persisted") {
		t.Fatalf("queue gone after the container was rebuilt: %q", out)
	}
}
