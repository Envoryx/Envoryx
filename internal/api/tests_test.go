package api_test

import (
	"net/http"
	"testing"
)

func TestTestSuitesRoute(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/tests", nil, false)
	if r.status != http.StatusOK || len(r.body["suites"].([]any)) != 0 || len(r.body["runs"].([]any)) != 0 {
		t.Fatalf("tests = %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/test-runs/00000000-0000-4000-8000-000000000000", nil, false); r.status != http.StatusNotFound {
		t.Fatalf("unknown run = %d", r.status)
	}
	// A suite the project does not have is refused before any socket is opened.
	if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/tests/phpunit/ws", nil, false); r.status != http.StatusNotFound {
		t.Fatalf("unknown suite = %d %s", r.status, r.raw)
	}
}
