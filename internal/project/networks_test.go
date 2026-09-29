package project

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestPoolSubnets(t *testing.T) {
	subs := poolSubnets(netip.MustParsePrefix("10.213.0.0/16"))
	if len(subs) != 256 || subs[0].String() != "10.213.0.0/24" || subs[255].String() != "10.213.255.0/24" {
		t.Fatalf("/16: %d %v %v", len(subs), subs[0], subs[len(subs)-1])
	}
	if subs := poolSubnets(netip.MustParsePrefix("192.168.7.0/24")); len(subs) != 1 || subs[0].String() != "192.168.7.0/24" {
		t.Fatalf("/24: %v", subs)
	}
	if subs := poolSubnets(netip.MustParsePrefix("10.0.0.0/25")); len(subs) != 0 {
		t.Fatalf("a range smaller than a /24 holds none: %v", subs)
	}
}

// Each project network gets the next free /24 of the pool, around ranges other networks
// on the host hold, and a full pool says what to do instead of Docker's raw error.
func TestProjectNetworksComeFromThePool(t *testing.T) {
	e := newEnv(t)
	e.m.cfg.NetworkPool = netip.MustParsePrefix("10.213.0.0/22")
	e.engine.AddNetworkWithSubnet("someone-elses", nil, "10.213.0.0/24")
	ctx := context.Background()
	for i, want := range []string{"10.213.1.0/24", "10.213.2.0/24", "10.213.3.0/24"} {
		v, err := e.m.Create(ctx, phpRequest("Net "+string(rune('A'+i)), false))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.engine.NetworkSubnets(NetworkName(v.Project.Slug)); len(got) != 1 || got[0] != want {
			t.Fatalf("project %d: %v, want %s", i, got, want)
		}
	}
	_, err := e.m.Create(ctx, phpRequest("Net D", false))
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "ENVORYX_NETWORK_POOL") {
		t.Fatalf("full pool: %v", err)
	}
}
