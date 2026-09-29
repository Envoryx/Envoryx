package config

import "testing"

func TestNetworkPool(t *testing.T) {
	for in, want := range map[string]string{"": "invalid Prefix", "off": "invalid Prefix", "10.213.0.0/16": "10.213.0.0/16", " 10.213.7.9/20 ": "10.213.0.0/20", "172.30.5.0/24": "172.30.5.0/24"} {
		got, err := networkPool(in)
		if err != nil || got.String() != want {
			t.Errorf("networkPool(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"10.0.0.0/25", "fd00::/48", "nonsense"} {
		if _, err := networkPool(bad); err == nil {
			t.Errorf("networkPool(%q) accepted", bad)
		}
	}
}
