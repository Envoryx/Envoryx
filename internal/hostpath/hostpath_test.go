package hostpath

import (
	"context"
	"errors"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/docker/dockertest"
)

func TestResolveWithOverrides(t *testing.T) {
	r := New(nil, map[string]string{"/projects": "/mnt/user/development", "/config": "/mnt/user/appdata/envoryx"})
	got, err := r.Resolve("/projects/shop")
	if err != nil || got != "/mnt/user/development/shop" {
		t.Fatalf("got %q %v", got, err)
	}
	got, err = r.Resolve("/config/projects/abc/php/zz.ini")
	if err != nil || got != "/mnt/user/appdata/envoryx/projects/abc/php/zz.ini" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := r.Resolve("/other"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("expected unresolved, got %v", err)
	}
	if _, err := r.Resolve("relative"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("expected unresolved for relative path, got %v", err)
	}
}

func TestDetectFromOwnContainer(t *testing.T) {
	engine := dockertest.New()
	engine.AddManagedContainer(docker.ContainerSpec{
		Name:   "envoryx",
		Labels: map[string]string{"x": "y"},
		Mounts: []docker.MountSpec{
			{Type: "bind", Source: "/mnt/user/development", Target: "/projects"},
			{Type: "bind", Source: "/mnt/user/appdata/envoryx", Target: "/config"},
			{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		},
	}, "running")
	r := New(engine, nil)
	r.selfIDs = func() []string { return []string{"unknown", "envoryx"} }
	if err := r.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve("/projects/shop")
	if err != nil || got != "/mnt/user/development/shop" {
		t.Fatalf("got %q %v", got, err)
	}
	st := r.Status()
	if st.SelfContainerID != "envoryx" || st.Detected["/config"] != "/mnt/user/appdata/envoryx" || st.Error != "" {
		t.Fatalf("status: %+v", st)
	}

	// Overrides win over detection.
	r2 := New(engine, map[string]string{"/projects": "/srv/dev"})
	r2.selfIDs = func() []string { return []string{"envoryx"} }
	_ = r2.Detect(context.Background())
	if got, _ := r2.Resolve("/projects/x"); got != "/srv/dev/x" {
		t.Fatalf("override ignored: %q", got)
	}
}

func TestBareMetalUsesIdentityMapping(t *testing.T) {
	r := New(dockertest.New(), nil)
	r.selfIDs = func() []string { return nil }
	r.inContainer = func() bool { return false }
	if err := r.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Resolve("/srv/projects/x"); err != nil || got != "/srv/projects/x" {
		t.Fatalf("identity mapping expected, got %q %v", got, err)
	}
	if !r.Status().BareMetal {
		t.Fatal("status must report bare metal mode")
	}
}

func TestDetectFailureIsReported(t *testing.T) {
	engine := dockertest.New()
	r := New(engine, nil)
	r.selfIDs = func() []string { return []string{"nope"} }
	r.inContainer = func() bool { return true }
	if err := r.Detect(context.Background()); err == nil {
		t.Fatal("expected detection error")
	}
	if _, err := r.Resolve("/projects"); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("expected unresolved, got %v", err)
	}
	if r.Status().Error == "" {
		t.Fatal("status must carry the error")
	}
}
