package api_test

import (
	"net/http"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
)

func TestMetricsEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()}}, true)
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/metrics?range=6h", nil, false)
	m := r.body["metrics"].(map[string]any)
	if r.status != http.StatusOK || m["res"] != float64(60) || m["containers"] == nil {
		t.Fatalf("project metrics: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/metrics?range=2y", nil, false); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad range: %d", r.status)
	}
	r = a.do(http.MethodGet, "/api/v1/metrics/overview?range=7d", nil, false)
	if r.status != http.StatusOK || r.body["overview"].(map[string]any)["res"] != float64(3600) {
		t.Fatalf("overview: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"metricsRetentionDays": 30}, true)
	if r.status != http.StatusOK || r.body["metrics"].(map[string]any)["retentionDays"] != float64(30) {
		t.Fatalf("retention: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"metricsRetentionDays": 1000}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("too long: %d", r.status)
	}
	if r := a.do(http.MethodDelete, "/api/v1/settings/metrics", nil, true); r.status != http.StatusOK {
		t.Fatalf("clear: %d %s", r.status, r.raw)
	}
}
