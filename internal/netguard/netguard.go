// Package netguard keeps requests Envoryx makes to addresses a user typed in (a webhook,
// an addon URL) away from places they have no business: Envoryx itself and the host's
// loopback, link-local addresses such as the cloud metadata service, and, on a hosted
// instance, the hoster's private network. The check runs on the address actually dialled,
// so a name that resolves somewhere else later (DNS rebinding) doesn't get past it.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlocked is returned for a connection to a refused address.
var ErrBlocked = errors.New("address not allowed")

// Allowed reports whether a connection to addr may be made. Private addresses (a NAS, an
// ntfy server on the LAN) are fine unless private is false.
func Allowed(addr netip.Addr, private bool) bool {
	addr = addr.Unmap()
	switch {
	case !addr.IsValid(), addr.IsUnspecified(), addr.IsLoopback(), addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast(), addr.IsInterfaceLocalMulticast(), addr.IsMulticast():
		return false
	case !private && addr.IsPrivate():
		return false
	}
	// 100.64.0.0/10 (carrier-grade NAT) counts as private here: hosters use it inside.
	if !private && netip.MustParsePrefix("100.64.0.0/10").Contains(addr) {
		return false
	}
	return true
}

// Control is a net.Dialer Control function that refuses connections allow says no to.
func Control(allow func(netip.Addr) bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrBlocked, address)
		}
		if !allow(ap.Addr()) {
			return fmt.Errorf("%w: %s", ErrBlocked, ap.Addr())
		}
		return nil
	}
}

// Policy is Allowed with private addresses allowed while private says so (asked for
// each connection, so it follows the instance's plan); nil allows them.
func Policy(private func() bool) func(netip.Addr) bool {
	return func(a netip.Addr) bool { return Allowed(a, private == nil || private()) }
}

// Client returns an HTTP client whose connections pass allow.
func Client(timeout time.Duration, allow func(netip.Addr) bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: Control(allow)}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	tr.Proxy = nil
	return &http.Client{Timeout: timeout, Transport: tr}
}
