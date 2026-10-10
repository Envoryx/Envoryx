package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/plan"
)

// setPlan writes the instance's plan file and has it read, as the plan's watcher would.
func (a *testApp) setPlan(content string) {
	a.t.Helper()
	if err := os.WriteFile(filepath.Join(a.cfgDir, plan.File), []byte(content), 0o600); err != nil {
		a.t.Fatal(err)
	}
	a.plans.Reload()
	if err := a.plans.Err(); err != nil {
		a.t.Fatal(err)
	}
}

func TestPlanEndpointAndQuotas(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()

	r := a.do(http.MethodGet, "/api/v1/plan", nil, false)
	if r.status != http.StatusOK || r.body["plan"] != nil {
		t.Fatalf("no plan: %d %s", r.status, r.raw)
	}

	a.setPlan(`{"name":"Starter","limits":{"projects":1,"users":2},"disabled":["offsite"]}`)
	r = a.do(http.MethodGet, "/api/v1/plan", nil, false)
	p, _ := r.body["plan"].(map[string]any)
	u, _ := r.body["usage"].(map[string]any)
	if r.status != http.StatusOK || p["name"] != "Starter" || u["users"] != 1.0 || u["projects"] != 0.0 {
		t.Fatalf("plan: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "One", "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("first project: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Two", "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusForbidden || errCode(r) != "plan_limit" {
		t.Fatalf("second project: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodPost, "/api/v1/users", map[string]any{"username": "dana", "role": "developer"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("second user: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/users", map[string]any{"username": "eli", "role": "developer"}, true)
	if r.status != http.StatusForbidden || errCode(r) != "plan_limit" {
		t.Fatalf("third user: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodPost, "/api/v1/offsite/targets", map[string]any{"name": "NAS", "type": "webdav", "url": "https://nas.example.net/dav"}, true)
	if r.status != http.StatusForbidden || errCode(r) != "plan_limit" {
		t.Fatalf("offsite target: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/offsite", nil, false); r.status != http.StatusOK {
		t.Fatalf("offsite list: %d %s", r.status, r.raw)
	}
}

func TestPlanFixesSettings(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()

	a.setPlan(`{"settings":{"baseDomain":"c1.example.net","forceHttps":true}}`)
	r := a.do(http.MethodGet, "/api/v1/settings", nil, false)
	locked, _ := r.body["lockedSettings"].([]any)
	if r.body["baseDomain"] != "c1.example.net" || r.body["forceHttps"] != true || len(locked) != 2 {
		t.Fatalf("settings: %s", r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"baseDomain": "mine.test"}, true)
	if r.status != http.StatusForbidden || errCode(r) != "plan_limit" {
		t.Fatalf("change a fixed setting: %d %s", r.status, r.raw)
	}
	// The fixed value itself may come along, as a form sends all its fields.
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"baseDomain": "c1.example.net", "forceHttps": true, "xdebugClientHost": "10.0.0.5"}, true)
	if r.status != http.StatusOK || r.body["xdebugClientHost"] != "10.0.0.5" {
		t.Fatalf("change a free setting: %d %s", r.status, r.raw)
	}

	// Without the plan the settings are the admin's again.
	if err := os.Remove(filepath.Join(a.cfgDir, plan.File)); err != nil {
		t.Fatal(err)
	}
	a.plans.Reload()
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"baseDomain": "mine.test"}, true)
	if r.status != http.StatusOK || r.body["baseDomain"] != "mine.test" {
		t.Fatalf("after the plan: %d %s", r.status, r.raw)
	}
}
