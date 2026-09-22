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
