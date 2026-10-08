package docker

import (
	"bufio"
	"context"
	"strconv"
	"strings"
)

// HostPortLister is implemented by engines that can see the Docker host's own sockets.
type HostPortLister interface {
	// HostListeningPorts returns the TCP ports something listens on in the Docker host's
	// network namespace, on any address.
	HostListeningPorts(ctx context.Context) (map[int]bool, error)
}

// hostPortProbeImage runs the probe; the addon volume tools use it too.
const hostPortProbeImage = "busybox:1.37"

// HostListeningPorts implements HostPortLister. Envoryx usually runs in a container of
// its own, so its /proc/net/tcp shows only its own sockets, and Docker only knows the
// ports its containers publish. A short-lived container in the host's network namespace
// reads the host's table instead: a program on the host (an IDE backend, a database
// installed there) holding a port is then skipped when Envoryx hands out ports, instead
// of failing the project's start with "address already in use".
func (e *MobyEngine) HostListeningPorts(ctx context.Context) (map[int]bool, error) {
	if err := e.EnsureImage(ctx, hostPortProbeImage, nil); err != nil {
		return nil, err
	}
	labels := ManagedLabels("", "", "port-probe", "")
	labels[LabelSystem] = "port-probe"
	res, err := e.RunOneShot(ctx, ContainerSpec{
		Image:   hostPortProbeImage,
		Labels:  labels,
		Cmd:     []string{"cat", "/proc/net/tcp", "/proc/net/tcp6"},
		Network: "host",
	})
	if err != nil {
		return nil, err
	}
	return parseListeningPorts(res.Stdout), nil
}

// parseListeningPorts reads /proc/net/tcp and /proc/net/tcp6: the local address is
// HEXADDR:HEXPORT, state 0A is LISTEN.
func parseListeningPorts(table string) map[int]bool {
	ports := map[int]bool{}
	sc := bufio.NewScanner(strings.NewReader(table))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		_, hexPort, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		if port, err := strconv.ParseUint(hexPort, 16, 16); err == nil {
			ports[int(port)] = true
		}
	}
	return ports
}
