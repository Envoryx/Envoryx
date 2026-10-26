package netguard

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	for addr, want := range map[string][2]bool{ // [private allowed, private refused]
		"127.0.0.1":        {false, false},
		"::1":              {false, false},
		"169.254.169.254":  {false, false},
		"fe80::1":          {false, false},
		"0.0.0.0":          {false, false},
		"192.168.1.20":     {true, false},
		"10.0.0.5":         {true, false},
		"100.64.1.1":       {true, false},
		"fd00::1":          {true, false},
		"203.0.113.7":      {true, true},
		"::ffff:127.0.0.1": {false, false},
	} {
		a := netip.MustParseAddr(addr)
		if got := Allowed(a, true); got != want[0] {
			t.Errorf("%s with private: %v", addr, got)
		}
		if got := Allowed(a, false); got != want[1] {
			t.Errorf("%s without private: %v", addr, got)
		}
	}
}

// The check runs on the dialled address: a loopback server is refused.
func TestClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := Client(5*time.Second, Policy(nil)).Get(srv.URL)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback: %v", err)
	}
}
