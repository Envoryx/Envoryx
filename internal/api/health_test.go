package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
)

func TestHealthCheckEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "createStarter": true, "php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()}}, true)
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/health-check", map[string]any{"path": "/health", "failures": 5}, true)
	if r.status != http.StatusOK {
		t.Fatalf("set: %d %s", r.status, r.raw)
	}
	hc := r.body["project"].(map[string]any)["healthCheck"].(map[string]any)
	if hc["path"] != "/health" || hc["status"] != float64(200) || hc["intervalSec"] != float64(30) || hc["timeoutSec"] != float64(5) || hc["failures"] != float64(5) {
		t.Fatalf("check: %v", hc)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/health-check", map[string]any{"path": "health"}, true)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(string(r.raw), "must start with /") {
		t.Fatalf("bad path: %d %s", r.status, r.raw)
	}
	// The project is stopped: nothing to ask.
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/health-check/test", map[string]any{"path": "/health"}, true)
	if r.status != http.StatusConflict {
		t.Fatalf("test on a stopped project: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/health-check", map[string]any{"path": ""}, true)
	if r.status != http.StatusOK || r.body["project"].(map[string]any)["healthCheck"] != nil {
		t.Fatalf("off: %d %s", r.status, r.raw)
	}
}
