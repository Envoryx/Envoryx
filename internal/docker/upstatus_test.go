package docker

import "testing"

func TestUpWithin(t *testing.T) {
	for status, want := range map[string]bool{
		"Up Less than a second": true, "Up 1 second": true, "Up 19 seconds (healthy)": true,
		"Up 20 seconds": false, "Up 42 seconds (healthy)": false,
		"Up About a minute": false, "Up 3 minutes": false, "Up 2 hours (unhealthy)": false, "Restarting (1) 3 seconds ago": false,
	} {
		if got := upWithin(status, CrashLoopWindow); got != want {
			t.Errorf("%q: %v", status, got)
		}
	}
}
