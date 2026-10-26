package main

import (
	"testing"

	"github.com/envoryx/envoryx/internal/config"
)

// The proxy also listens on a published host port that differs from its container port,
// but never on a port Envoryx uses for something else.
func TestHostPortListener(t *testing.T) {
	cfg := config.Config{ListenAddr: ":8787", SSHListen: ":2222", ProxyHTTP: ":80", ProxyHTTPS: ":443"}
	for _, c := range []struct {
		listen   string
		hostPort int
		want     string
	}{
		{":443", 18904, ":18904"},
		{":80", 18804, ":18804"},
		{":443", 443, ""},      // the same port
		{":443", 0, ""},        // not published
		{"", 18904, ""},        // HTTPS off
		{":443", 8787, ""},     // the UI's port
		{":443", 2222, ""},     // the SSH server's
		{":443", 80, ""},       // the other proxy listener's
		{":8443", 443, ":443"}, // a container port other than 443
	} {
		cfg := cfg
		if c.listen == ":8443" {
			cfg.ProxyHTTPS = ":8443"
		}
		if got := hostPortListener(cfg, c.listen, c.hostPort); got != c.want {
			t.Errorf("hostPortListener(%q, %d) = %q, want %q", c.listen, c.hostPort, got, c.want)
		}
	}
}
