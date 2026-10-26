package project

import (
	"context"
	"net/netip"
	"testing"
)

// An address in a project network's subnet is a project's container; the gateway (the
// host's side) and addresses elsewhere are not.
func TestFromProjectNetwork(t *testing.T) {
	e := newEnv(t)
	e.m.cfg.NetworkPool = netip.MustParsePrefix("10.213.0.0/22")
	if _, err := e.m.Create(context.Background(), phpRequest("Shop", true)); err != nil {
		t.Fatal(err)
	}
	subnets := e.engine.NetworkSubnets("envoryx-shop")
	if len(subnets) == 0 {
		t.Fatal("the project network has no subnet")
	}
	p := netip.MustParsePrefix(subnets[0]).Masked()
	gateway := p.Addr().Next()
	container := gateway.Next().Next()
	if !e.m.FromProjectNetwork(container) {
		t.Fatalf("%s in %s is a project container", container, p)
	}
	if e.m.FromProjectNetwork(gateway) {
		t.Fatal("the gateway is the host's side")
	}
	if e.m.FromProjectNetwork(netip.MustParseAddr("192.0.2.10")) {
		t.Fatal("an address outside the project networks")
	}
	// A project created after the list was read is covered at once, not after the cache.
	if _, err := e.m.Create(context.Background(), phpRequest("Blog", true)); err != nil {
		t.Fatal(err)
	}
	blog := netip.MustParsePrefix(e.engine.NetworkSubnets("envoryx-blog")[0]).Masked()
	if !e.m.FromProjectNetwork(blog.Addr().Next().Next()) {
		t.Fatal("a new project's network is not covered yet")
	}
}
