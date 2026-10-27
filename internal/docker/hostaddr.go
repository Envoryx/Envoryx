package docker

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// HostAddressFinder is implemented by engines that can ask the Docker host for its own
// LAN address.
type HostAddressFinder interface {
	// HostLANAddress returns the IPv4 address the Docker host uses toward its default
	// route, its address in the LAN.
	HostLANAddress(ctx context.Context) (string, error)
}

// HostLANAddress implements HostAddressFinder. Envoryx with an IP of its own (Unraid
// br0, macvlan) can't see the host's address: Docker only knows the host's name, which
// often doesn't resolve without mDNS. A short-lived container in the host's network
// namespace asks the routing table which source address the host uses, which needs no
// connectivity, only a default route.
func (e *MobyEngine) HostLANAddress(ctx context.Context) (string, error) {
	if err := e.EnsureImage(ctx, hostPortProbeImage, nil); err != nil {
		return "", err
	}
	labels := ManagedLabels("", "", "address-probe", "")
	labels[LabelSystem] = "address-probe"
	res, err := e.RunOneShot(ctx, ContainerSpec{
		Image:   hostPortProbeImage,
		Labels:  labels,
		Cmd:     []string{"ip", "-4", "route", "get", "1.1.1.1"},
		Network: "host",
	})
	if err != nil {
		return "", err
	}
	if ip := parseRouteSource(res.Stdout); ip != "" {
		return ip, nil
	}
	return "", fmt.Errorf("no source address in %q", strings.TrimSpace(res.Stdout+res.Stderr))
}

// parseRouteSource reads the address after "src" in the output of `ip route get`
// ("1.1.1.1 via 192.168.1.1 dev br0 src 192.168.1.10 uid 0").
func parseRouteSource(out string) string {
	f := strings.Fields(out)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "src" {
			if ip := net.ParseIP(f[i+1]); ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	return ""
}
