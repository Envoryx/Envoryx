package project

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/envoryx/envoryx/internal/docker"
)

// Docker hands every new bridge network a /16 or /20 out of a handful of default ranges,
// so a host runs out after about 30 networks - and every Envoryx project keeps one, even
// while stopped. With a network pool (ENVORYX_NETWORK_POOL, 10.213.0.0/16 unless set)
// Envoryx picks a /24 for each new network itself: 256 projects per /16, and existing
// networks keep the range they have.

// networkPrefixBits is the size of one project network: 254 addresses are plenty for the
// handful of containers a project runs.
const networkPrefixBits = 24

// createNetwork creates a network for Envoryx's containers. With a pool it takes the first
// /24 no network on the host uses; one Docker still refuses (another host route, a create
// that picked it at the same moment) is skipped for the next.
func (m *Manager) createNetwork(ctx context.Context, name string, labels map[string]string) error {
	pool := m.cfg.NetworkPool
	if !pool.IsValid() {
		_, err := m.engine.CreateNetwork(ctx, name, labels, "")
		return networkError(err)
	}
	networks, err := m.engine.ListNetworks(ctx, false)
	if err != nil {
		return err
	}
	var used []netip.Prefix
	for _, n := range networks {
		for _, s := range n.Subnets {
			if p, err := netip.ParsePrefix(s); err == nil {
				used = append(used, p)
			}
		}
	}
	for _, subnet := range poolSubnets(pool) {
		if overlapsAny(subnet, used) {
			continue
		}
		_, err := m.engine.CreateNetwork(ctx, name, labels, subnet.String())
		if errors.Is(err, docker.ErrSubnetInUse) {
			used = append(used, subnet)
			continue
		}
		return networkError(err)
	}
	return fmt.Errorf("%w: every /%d of the network pool %s is in use; delete projects you no longer need or give ENVORYX_NETWORK_POOL a larger range", ErrConflict, networkPrefixBits, pool)
}

// networkError turns Docker running out of its own ranges into a message that says what to do.
func networkError(err error) error {
	if errors.Is(err, docker.ErrAddressPoolsExhausted) {
		return fmt.Errorf("%w: Docker has no address range left for another network; set ENVORYX_NETWORK_POOL (e.g. 10.213.0.0/16) so Envoryx gives each project a small one", ErrConflict)
	}
	return err
}

// poolSubnets lists the /24 networks of an IPv4 pool in order.
func poolSubnets(pool netip.Prefix) []netip.Prefix {
	pool = pool.Masked()
	if !pool.Addr().Is4() || pool.Bits() > networkPrefixBits {
		return nil
	}
	base := pool.Addr().As4()
	start := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	count := 1 << (networkPrefixBits - pool.Bits())
	out := make([]netip.Prefix, 0, count)
	for i := 0; i < count; i++ {
		a := start + uint32(i)<<(32-networkPrefixBits)
		out = append(out, netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a)}), networkPrefixBits))
	}
	return out
}

func overlapsAny(p netip.Prefix, others []netip.Prefix) bool {
	for _, o := range others {
		if o.Overlaps(p) {
			return true
		}
	}
	return false
}
