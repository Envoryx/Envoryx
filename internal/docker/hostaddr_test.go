package docker

import "testing"

func TestParseRouteSource(t *testing.T) {
	cases := map[string]string{
		"1.1.1.1 via 192.168.1.1 dev br0 src 192.168.1.10 uid 0\n    cache \n": "192.168.1.10",
		"1.1.1.1 via 10.0.0.1 dev eth0  src 10.0.0.7 \n":                       "10.0.0.7",
		"ip: RTNETLINK answers: Network unreachable":                           "",
		"1.1.1.1 dev lo src 127.0.0.1":                                         "",
	}
	for in, want := range cases {
		if got := parseRouteSource(in); got != want {
			t.Errorf("parseRouteSource(%q) = %q, want %q", in, got, want)
		}
	}
}
