package sshd

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
)

func TestParseForward(t *testing.T) {
	host := "localhost"
	b := make([]byte, 4+len(host)+4)
	binary.BigEndian.PutUint32(b, uint32(len(host)))
	copy(b[4:], host)
	binary.BigEndian.PutUint32(b[4+len(host):], 5990)
	h, p, ok := parseForward(b)
	if !ok || h != "localhost" || p != 5990 {
		t.Fatalf("parse: %q %d %v", h, p, ok)
	}
	if _, _, ok := parseForward(b[:6]); ok {
		t.Fatal("truncated payload must fail")
	}
}

// SetDialForTest overrides the forward dialer (used by the external test package).
func (s *Server) SetDialForTest(f func(ctx context.Context, addr string) (net.Conn, error)) {
	s.dial = f
}

func TestParseListenAddress(t *testing.T) {
	procNet := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1766 00000000:0000 0A 00000000:00000000 00:00000000 00000000    99        0 1 0 0
   1: 0600A8C0:88AF 00000000:0000 0A 00000000:00000000 00:00000000 00000000    99        0 1 0 0
   2: 0600A8C0:88AF 0100A8C0:C350 01 00000000:00000000 00:00000000 00000000    99        0 1 0 0
   3: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 0 0
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000    99        0 1 0 0
   1: 00000000000000000000000001000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000    99        0 1 0 0
`
	cases := map[int]string{
		5990:  "TCP4:127.0.0.1:5990",    // backend on loopback
		34991: "TCP4:192.168.0.6:34991", // worker bound to the container address
		80:    "TCP4:127.0.0.1:80",      // wildcard
		8080:  "TCP4:127.0.0.1:8080",    // IPv6 wildcard (dual stack)
		8081:  "TCP6:[::1]:8081",        // IPv6 loopback
		50000: "",                       // established connection only / nothing
	}
	for port, want := range cases {
		if got := parseListenAddress(procNet, port); got != want {
			t.Errorf("port %d: got %q want %q", port, got, want)
		}
	}
}
