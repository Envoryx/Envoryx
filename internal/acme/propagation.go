package acme

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"time"
)

// The CA asks the zone's authoritative name servers, so that is where Envoryx looks
// too. Asking a public resolver instead has two problems: a lookup before the record
// exists gets the NXDOMAIN cached for as long as the zone's SOA says (often an hour), and
// providers that are slow to update their name servers (netcup) look done too early or
// never. A public resolver (1.1.1.1) is used only to find the name servers, so local DNS
// rewrites (AdGuard, Pi-hole) cannot get in the way either.

// resolverAt returns a resolver that sends every query to server (host:port).
func resolverAt(server string) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, "udp", server)
	}}
}

var publicResolver = resolverAt("1.1.1.1:53")

// publicTXTLookup asks the public resolver.
func publicTXTLookup(ctx context.Context, fqdn string) ([]string, error) {
	return publicResolver.LookupTXT(ctx, fqdn)
}

// zoneNameServers finds the name servers of the closest enclosing zone that has any.
func zoneNameServers(ctx context.Context, fqdn string) ([]string, error) {
	labels := strings.Split(strings.Trim(fqdn, "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		ns, err := publicResolver.LookupNS(ctx, strings.Join(labels[i:], "."))
		if err != nil || len(ns) == 0 {
			continue
		}
		var hosts []string
		for _, n := range ns {
			hosts = append(hosts, strings.TrimSuffix(n.Host, "."))
		}
		return hosts, nil
	}
	return nil, errors.New("no name servers found")
}

// authoritativeTXTLookup returns the TXT values every name server of the zone serves.
// Without name servers (no network to them, a split horizon) it falls back to the
// public resolver.
func authoritativeTXTLookup(ctx context.Context, fqdn string) ([]string, error) {
	servers, err := zoneNameServers(ctx, fqdn)
	if err != nil {
		return publicTXTLookup(ctx, fqdn)
	}
	var common []string
	asked := 0
	for _, host := range servers {
		addrs, err := publicResolver.LookupHost(ctx, host)
		if err != nil || len(addrs) == 0 {
			continue
		}
		var got []string
		for _, addr := range addrs {
			if got, err = resolverAt(net.JoinHostPort(addr, "53")).LookupTXT(ctx, fqdn); err == nil {
				break
			}
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				got, err = nil, nil
				break
			}
		}
		if err != nil {
			continue // this server does not answer; the others decide
		}
		asked++
		if asked == 1 {
			common = got
			continue
		}
		common = slices.DeleteFunc(common, func(v string) bool { return !slices.Contains(got, v) })
	}
	if asked == 0 {
		return publicTXTLookup(ctx, fqdn)
	}
	return common, nil
}
