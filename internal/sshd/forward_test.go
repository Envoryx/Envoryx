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
