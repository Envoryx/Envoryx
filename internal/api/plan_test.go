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
	// The hoster's target keeps working without the feature: fetching from it, and
	// sending a backup offsite (the syncer then only uses the hoster's target).
	key := map[string]any{"key": "instance/x.tar.gz"}
	if r := a.do(http.MethodPost, "/api/v1/offsite/targets/nas/instance/fetch", key, true); errCode(r) != "plan_limit" {
		t.Fatalf("fetch from an own target: %d %s", r.status, r.raw)
	}
	for _, path := range []string{"/api/v1/offsite/targets/hoster/instance/fetch", "/api/v1/offsite/targets/hoster/projects/fetch", "/api/v1/instance/backups/x/offsite"} {
		if r := a.do(http.MethodPost, path, key, true); errCode(r) == "plan_limit" {
			t.Fatalf("%s: %d %s", path, r.status, r.raw)
		}
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

// The plan sets its fixed settings at every start and push; only a real change is in
// the audit log.
func TestPlanSettingsAuditOnlyChanges(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	changes := func() int {
		r := a.do(http.MethodGet, "/api/v1/audit?action=settings.changed", nil, false)
		entries, _ := r.body["entries"].([]any)
		return len(entries)
	}
	a.setPlan(`{"name":"Starter","settings":{"metricsRetentionDays":7,"forceHttps":true,"publicHost":"c1.example.net"}}`)
	first := changes()
	if first == 0 {
		t.Fatal("the plan's settings weren't logged")
	}
	a.setPlan(`{"name":"Starter","limits":{"projects":3},"settings":{"metricsRetentionDays":7,"forceHttps":true,"publicHost":"c1.example.net"}}`)
	if n := changes(); n != first {
		t.Fatalf("%d settings.changed entries after a plan push that changed no setting, want %d", n, first)
	}
	a.setPlan(`{"name":"Starter","settings":{"metricsRetentionDays":14,"forceHttps":true,"publicHost":"c1.example.net"}}`)
	if n := changes(); n != first+1 {
		t.Fatalf("%d settings.changed entries after one change, want %d", n, first+1)
	}
}

func TestSuspendedInstanceAnswersOnlyWhoAndWhy(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "CI", "scope": "admin"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("token: %d %s", r.status, r.raw)
	}
	secret := r.body["secret"].(string)

	if err := os.WriteFile(filepath.Join(a.cfgDir, plan.SuspensionFile), []byte(`{"since":"2026-10-11T08:00:00Z","message":"Invoice 1234 is open"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.plans.Reload()

	r = a.do(http.MethodGet, "/api/v1/plan", nil, false)
	s, _ := r.body["suspension"].(map[string]any)
	if r.status != http.StatusOK || s["message"] != "Invoice 1234 is open" {
		t.Fatalf("plan: %d %s", r.status, r.raw)
	}
	if r = a.do(http.MethodGet, "/api/v1/auth/me", nil, false); r.status != http.StatusOK {
		t.Fatalf("me: %d %s", r.status, r.raw)
	}
	for _, path := range []string{"/api/v1/projects", "/api/v1/settings", "/api/v1/dashboard"} {
		if r = a.do(http.MethodGet, path, nil, false); r.status != http.StatusForbidden || errCode(r) != "suspended" {
			t.Errorf("%s: %d %s", path, r.status, r.raw)
		}
	}
	for _, path := range []string{"/api/v1/projects", "/mcp"} {
		req, _ := http.NewRequest(http.MethodGet, a.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("token on %s: %d", path, res.StatusCode)
		}
	}

	if err := os.Remove(filepath.Join(a.cfgDir, plan.SuspensionFile)); err != nil {
		t.Fatal(err)
	}
	a.plans.Reload()
	if r = a.do(http.MethodGet, "/api/v1/projects", nil, false); r.status != http.StatusOK {
		t.Fatalf("after lifting: %d %s", r.status, r.raw)
	}
	if r = a.do(http.MethodGet, "/api/v1/plan", nil, false); r.body["suspension"] != nil {
		t.Fatalf("plan after lifting: %s", r.raw)
	}
}
