package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
)

func TestLimitsEndpoint(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()}}, true)
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/limits", map[string]any{"app": map[string]any{"cpus": 1.5, "memoryMb": 1024}, "services": map[string]any{"memoryMb": 512}, "pids": 2048}, true)
	if r.status != http.StatusOK {
		t.Fatalf("set: %d %s", r.status, r.raw)
	}
	limits := r.body["project"].(map[string]any)["limits"].(map[string]any)
	if limits["app"].(map[string]any)["memoryMb"] != float64(1024) || limits["pids"] != float64(2048) {
		t.Fatalf("limits: %v", limits)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/limits", map[string]any{"app": map[string]any{"memoryMb": 10}}, true)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(string(r.raw), "at least 64 MiB") {
		t.Fatalf("too small: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/stats", nil, false)
	containers := r.body["containers"].([]any)
	if r.status != http.StatusOK || len(containers) == 0 || r.body["host"] == nil {
		t.Fatalf("stats: %d %s", r.status, r.raw)
	}
	for _, c := range containers {
		c := c.(map[string]any)
		if c["service"] == "php" && (c["group"] != "app" || c["cpuLimit"] != 1.5 || c["memLimit"] != float64(1<<30)) {
			t.Fatalf("php stats: %v", c)
		}
	}
}
