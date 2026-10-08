package docker

import (
	"errors"
	"strings"
	"testing"
)

func TestParseListeningPorts(t *testing.T) {
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:7530 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0000000000000000 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
   2: 0100007F:7531 0100007F:9C40 01 00000000:00000000 00:00000000 00000000  1000        0 3 1 0000000000000000 20 4 30 10 -1
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:7531 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 4 1 0000000000000000 100 0 0 10 0
`
	got := parseListeningPorts(table)
	// 30000 on 127.0.0.1, 22 on all addresses, 30001 on ::1; 40000 is only the remote
	// end of an established connection and 30001's IPv4 socket is not listening.
	if len(got) != 3 || !got[30000] || !got[22] || !got[30001] {
		t.Fatalf("ports = %v", got)
	}
}

func TestPortError(t *testing.T) {
	for _, msg := range []string{
		"Error response from daemon: driver failed programming external connectivity on endpoint envoryx-shop-web: Bind for 0.0.0.0:30001 failed: port is already allocated",
		"Error response from daemon: failed to set up container networking: listen tcp4 127.0.0.1:30001: bind: address already in use",
		"Error response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint envoryx-porty-web (9394): failed to bind host port 0.0.0.0:30001/tcp: address already in use",
	} {
		err := portError(errors.New(msg))
		if got := err.Error(); !errors.Is(err, ErrPortInUse) || !strings.HasPrefix(got, "host port in use: 30001 is taken") || !strings.Contains(got, msg) {
			t.Errorf("%q -> %q", msg, err)
		}
	}
	if err := errors.New("no such image"); portError(err) != err {
		t.Error("other errors pass through")
	}
}
