package project

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
)

// Envoryx joins every project network so its proxy reaches the web containers, which
// puts its own interface on those networks too. The server refuses requests from them
// (FromProjectNetwork): a project's code is no client of Envoryx.

// projectSubnetsTTL is how long the list of project subnets is used before Docker is
// asked again; a project network created since is covered within this time.
const projectSubnetsTTL = 30 * time.Second

type projectSubnets struct {
	mu       sync.Mutex
	prefixes []netip.Prefix
	at       time.Time
}

// FromProjectNetwork reports whether addr belongs to a project's container: an address
// in the subnet of a network Envoryx made for a project. The first address of a subnet
// is Docker's gateway, the host's side of the network, and doesn't count.
func (m *Manager) FromProjectNetwork(addr netip.Addr) bool {
	for _, p := range m.projectSubnetPrefixes() {
		if p.Contains(addr) && addr != p.Masked().Addr().Next() {
			return true
		}
	}
	return false
}

// forgetProjectSubnets makes the next check ask Docker again: a network was just created,
// and its containers must not wait out the cache.
func (m *Manager) forgetProjectSubnets() {
	m.subnets.mu.Lock()
	m.subnets.at = time.Time{}
	m.subnets.mu.Unlock()
}

func (m *Manager) projectSubnetPrefixes() []netip.Prefix {
	s := &m.subnets
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < projectSubnetsTTL {
		return s.prefixes
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		// Keep the last list; Docker answers again soon.
		return s.prefixes
	}
	var out []netip.Prefix
	for _, n := range networks {
		if n.Labels[docker.LabelProjectID] == "" {
			continue
		}
		for _, sub := range n.Subnets {
			if p, err := netip.ParsePrefix(sub); err == nil {
				out = append(out, p)
			}
		}
	}
	s.prefixes, s.at = out, time.Now()
	return out
}
