package runtime

import (
	"fmt"
	"strings"
)

// Guards are the shell commands an application container runs before its server starts:
// they block until the project is actually ready for it – the entry file exists, the
// database accepts connections. Waiting beats crash-looping. A container that restarts
// every few seconds buries its real reason in a wall of logs, and Django's runserver does
// not even exit: its autoreload parent survives the failed child, so the container stays
// "running" and serves nothing until someone restarts it by hand.

// Guarded returns argv behind the given guards: a shell that runs them in order and then
// replaces itself with argv. The validated argv arrives as "$@" (after $0), so nothing in
// it is interpolated into the script. Empty guards are dropped, and argv comes back
// unchanged when none is left – a project without a database keeps the exact command it
// had, so its container is not recreated for nothing.
func Guarded(argv []string, name string, guards ...string) []string {
	kept := make([]string, 0, len(guards))
	for _, g := range guards {
		if strings.TrimSpace(g) != "" {
			kept = append(kept, g)
		}
	}
	if len(kept) == 0 {
		return argv
	}
	script := strings.Join(kept, "; ") + `; exec "$@"`
	return append([]string{"sh", "-c", script, name}, argv...)
}

// WaitForTCP is a guard that blocks until host:port accepts a connection. socat ships in
// every Envoryx runtime image; the host is a fixed network alias or an external server's
// address that passed NormalizeExternalDatabase (host-name characters or an IP address),
// so interpolating it into the line is safe. The database images open their port only
// once initialisation is done – they run the setup server on a socket or with networking
// off – so a connection is a real readiness signal.
func WaitForTCP(host string, port int, label string) string {
	addr := host
	if strings.Contains(host, ":") {
		addr = "[" + host + "]" // IPv6, as socat wants it
	}
	return fmt.Sprintf(`until socat -u /dev/null TCP:%s:%d 2>/dev/null; do echo 'envoryx: waiting for %s at %s:%d'; sleep 2; done`, addr, port, label, host, port)
}
