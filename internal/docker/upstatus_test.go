package docker

import "testing"

func TestUpUnderAMinute(t *testing.T) {
	for status, want := range map[string]bool{
		"Up Less than a second": true, "Up 1 second": true, "Up 42 seconds (healthy)": true,
		"Up About a minute": false, "Up 3 minutes": false, "Up 2 hours (unhealthy)": false, "Restarting (1) 3 seconds ago": false,
	} {
		if got := upUnderAMinute(status); got != want {
			t.Errorf("%q: %v", status, got)
		}
	}
}
