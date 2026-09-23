package project

import (
	"context"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
)

// Remote forwarding listens where the container can reach Envoryx on the project network.
func TestCallbackAddresses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.Create(ctx, phpRequest("Shop", true)); err != nil {
		t.Fatal(err)
	}
	target, err := e.m.ResolveSSHUser(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.m.CallbackAddresses(ctx, target); err == nil {
		t.Fatal("no addresses known yet must be an error")
	}

	// Bare metal: the network's gateway is the host Envoryx runs on.
	e.engine.NetworkGateways = map[string]string{"envoryx-shop": "172.18.0.1"}
	e.engine.NetworkIPs = map[string]map[string]string{"envoryx-shop": {"envoryx-shop-php": "172.18.0.3"}}
	envoryx, container, err := e.m.CallbackAddresses(ctx, target)
	if err != nil || envoryx != "172.18.0.1" || container != "172.18.0.3" {
		t.Fatalf("bare metal: %q %q %v", envoryx, container, err)
	}

	// In Docker: Envoryx's own container on the network, known by a short id. The fake's ids
	// share their first 12 characters, so the prefix has to reach the sequence number.
	self := e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx"}, "running")
	e.selfID = self[:13]
	e.engine.NetworkIPs["envoryx-shop"]["envoryx"] = "172.18.0.2"
	envoryx, container, err = e.m.CallbackAddresses(ctx, target)
	if err != nil || envoryx != "172.18.0.2" || container != "172.18.0.3" {
		t.Fatalf("in docker: %q %q %v", envoryx, container, err)
	}
}
