//go:build integration

// Integration tests against a real Docker Engine. Run with:
//
//	go test -tags integration ./internal/docker/
//
// They create and remove small alpine containers, one network and one volume, all
// labelled envoryx.managed=true with the project id "envoryx-integration-test".
package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const testProject = "envoryx-integration-test"

func integrationEngine(t *testing.T) *MobyEngine {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	e, err := Connect(Options{}, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.Ping(ctx); err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestIntegrationLifecycleAndGuards(t *testing.T) {
	e := integrationEngine(t)
	ctx := context.Background()
	labels := ManagedLabels(testProject, "integration", "php", "test")
	netName := "envoryx-integration-test-net"
	volName := "envoryx-integration-test-vol"
	ctName := "envoryx-integration-test-php"

	// Clean up leftovers from previous runs.
	_ = e.RemoveContainer(ctx, ctName)
	_ = e.RemoveVolume(ctx, volName)
	_ = e.RemoveNetwork(ctx, netName)

	if err := e.EnsureImage(ctx, "alpine:3.20", func(msg string) { t.Log(msg) }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateNetwork(ctx, netName, ManagedLabels(testProject, "integration", "", "test")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveNetwork(ctx, netName) })
	if err := e.CreateVolume(ctx, volName, labels); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveVolume(ctx, volName) })

	id, err := e.CreateContainer(ctx, ContainerSpec{
		Name:          ctName,
		Image:         "alpine:3.20",
		Labels:        labels,
		Cmd:           []string{"sleep", "60"},
		Network:       netName,
		NetworkAlias:  []string{"php"},
		Mounts:        []MountSpec{{Type: "volume", Source: volName, Target: "/data"}},
		RestartPolicy: "no",
		StopTimeout:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })

	if err := e.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	d, err := e.InspectContainer(ctx, id)
	if err != nil || !d.Running || d.Name != ctName {
		t.Fatalf("inspect: %+v %v", d, err)
	}
	list, err := e.ListContainers(ctx, true, testProject)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list managed: %+v %v", list, err)
	}
	st, err := e.ContainerStats(ctx, id)
	if err != nil || st.ContainerID != id {
		t.Fatalf("stats: %+v %v", st, err)
	}
	eps, err := e.NetworkEndpoints(ctx, netName)
	if err != nil || len(eps) != 1 || eps[0].ContainerID != id || eps[0].Name != ctName {
		t.Fatalf("network endpoints: %+v %v", eps, err)
	}
	if err := e.RemoveNetwork(ctx, netName); err == nil {
		t.Fatal("removing a network with an active endpoint must fail")
	}
	// ExecStream must return once the process exits even when the caller's stdin stays
	// open (SSH clients keep it open until they see the exit status).
	pr, pw := io.Pipe()
	defer pw.Close()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		code, err := e.ExecStream(ctx, id, ExecStreamOptions{Cmd: []string{"sh", "-c", "echo hi; exit 3"}, Stdin: pr, Stdout: &out})
		if err != nil || code != 3 || out.String() != "hi\n" {
			t.Errorf("exec stream with open stdin: code=%d err=%v out=%q", code, err, out.String())
		}
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("ExecStream blocked on open stdin after the process exited")
	}
	if err := e.StopContainer(ctx, id, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	d, _ = e.InspectContainer(ctx, id)
	if d.Running {
		t.Fatal("container should be stopped")
	}
	if err := e.RemoveContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveVolume(ctx, volName); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveNetwork(ctx, netName); err != nil {
		t.Fatal(err)
	}
	if eps, err := e.NetworkEndpoints(ctx, netName); err != nil || len(eps) != 0 {
		t.Fatalf("endpoints of a removed network: %+v %v", eps, err)
	}
}

// Envoryx gives its own container the proxy's host names as aliases on each project
// network; Docker's DNS must answer them there and keep them until a reconnect. The names
// are under .invalid, so a wildcard for .test in the host's DNS cannot answer them instead.
func TestIntegrationNetworkAliasesResolveOnTheNetwork(t *testing.T) {
	e := integrationEngine(t)
	ctx := context.Background()
	labels := ManagedLabels(testProject, "integration", "", "test")
	netName := "envoryx-integration-test-alias"
	proxyName, clientName := "envoryx-integration-test-proxy", "envoryx-integration-test-client"
	_ = e.RemoveContainer(ctx, proxyName)
	_ = e.RemoveContainer(ctx, clientName)
	_ = e.RemoveNetwork(ctx, netName)

	if err := e.EnsureImage(ctx, "alpine:3.20", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateNetwork(ctx, netName, labels); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveNetwork(ctx, netName) })
	start := func(name, network string) string {
		id, err := e.CreateContainer(ctx, ContainerSpec{Name: name, Image: "alpine:3.20", Labels: labels, Cmd: []string{"sleep", "60"}, Network: network, RestartPolicy: "no", StopTimeout: 1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.RemoveContainer(ctx, id) })
		if err := e.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	proxyID := start(proxyName, "") // default bridge, like Envoryx before it attaches
	clientID := start(clientName, netName)

	if err := e.ConnectNetwork(ctx, netName, proxyID, "shop.alias.invalid", "envoryx.alias.invalid"); err != nil {
		t.Fatal(err)
	}
	aliases, err := e.NetworkAliases(ctx, netName, proxyID)
	if err != nil || len(aliases) != 2 {
		t.Fatalf("aliases: %v %v", aliases, err)
	}
	resolve := func(name string) string {
		res, err := e.Exec(ctx, clientID, []string{"getent", "hosts", name}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Stdout
	}
	if out := resolve("shop.alias.invalid"); !strings.Contains(out, "shop.alias.invalid") {
		t.Fatalf("the alias must resolve on the project network: %q", out)
	}
	// Aliases are fixed until the container is reconnected.
	if err := e.DisconnectNetwork(ctx, netName, proxyID); err != nil {
		t.Fatal(err)
	}
	if err := e.ConnectNetwork(ctx, netName, proxyID, "blog.alias.invalid"); err != nil {
		t.Fatal(err)
	}
	if out := resolve("blog.alias.invalid"); !strings.Contains(out, "blog.alias.invalid") {
		t.Fatalf("a reconnect must bring the new alias: %q", out)
	}
	if out := resolve("shop.alias.invalid"); out != "" {
		t.Fatalf("a reconnect must drop the old alias: %q", out)
	}
	if aliases, _ := e.NetworkAliases(ctx, "bridge", proxyID); len(aliases) != 0 {
		t.Fatalf("no aliases on a network given none: %v", aliases)
	}
}

func TestIntegrationForeignContainersAreUntouchable(t *testing.T) {
	e := integrationEngine(t)
	ctx := context.Background()

	// Create an unlabelled container through the raw client (simulates a foreign container).
	raw, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := e.EnsureImage(ctx, "alpine:3.20", nil); err != nil {
		t.Fatal(err)
	}
	name := "envoryx-integration-foreign"
	_, _ = raw.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	res, err := raw.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   name,
		Config: &container.Config{Image: "alpine:3.20", Cmd: []string{"sleep", "30"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.ContainerRemove(ctx, res.ID, client.ContainerRemoveOptions{Force: true})

	if _, err := e.InspectContainer(ctx, res.ID); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("inspect of foreign container must fail with ErrNotManaged, got %v", err)
	}
	if err := e.StartContainer(ctx, res.ID); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("start must be refused, got %v", err)
	}
	if err := e.StopContainer(ctx, res.ID, time.Second); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("stop must be refused, got %v", err)
	}
	if err := e.RemoveContainer(ctx, res.ID); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("remove must be refused, got %v", err)
	}
	inspect, err := raw.ContainerInspect(ctx, res.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("foreign container vanished: %v", err)
	}
	if inspect.Container.State.Running {
		t.Fatal("foreign container must not have been started")
	}
	managed, err := e.ListContainers(ctx, true, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range managed {
		if c.ID == res.ID {
			t.Fatal("foreign container listed as managed")
		}
	}
	if _, err := e.CreateContainer(ctx, ContainerSpec{Name: "x", Image: "alpine:3.20"}); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("creating an unlabelled container must be refused, got %v", err)
	}
}

// A one-shot must run to completion and report the process's exit code – not return the
// moment the container exists. The wait is registered before the start, so it has to ask
// for the next exit; with the default "not-running" condition a created container already
// qualifies and RunOneShot reported exit code 0 before the command had run (the scaffold
// of a template then "finished" in milliseconds and was killed by the cleanup).
func TestIntegrationRunOneShotWaitsForTheExit(t *testing.T) {
	e := integrationEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := e.EnsureImage(ctx, "alpine:3.20", nil); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	res, err := e.RunOneShot(ctx, ContainerSpec{
		Name:   "envoryx-integration-oneshot",
		Image:  "alpine:3.20",
		Labels: ManagedLabels(testProject, "integration", "oneshot", "test"),
		Cmd:    []string{"sh", "-c", "sleep 2; echo done; exit 3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(begin); took < 2*time.Second {
		t.Fatalf("RunOneShot returned after %v, before the command could finish", took)
	}
	if res.ExitCode != 3 || res.Stdout != "done\n" {
		t.Fatalf("exit=%d stdout=%q, want 3 / done", res.ExitCode, res.Stdout)
	}
}
