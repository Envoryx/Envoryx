package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

// serviceConfig returns the config and version of one service of a project answer.
func serviceConfig(t *testing.T, r resp, kind string) (map[string]any, string, bool) {
	t.Helper()
	for _, s := range r.body["project"].(map[string]any)["services"].([]any) {
		m := s.(map[string]any)
		if m["kind"] != kind {
			continue
		}
		cfg := map[string]any{}
		raw, _ := json.Marshal(m["config"])
		_ = json.Unmarshal(raw, &cfg)
		return cfg, m["version"].(string), true
	}
	return nil, "", false
}

// A PATCH changes what it sends and nothing else: a new PHP version keeps the PHP
// settings, one setting keeps the others, a runtime object without "enabled" keeps the
// runtime, and a database object without "exposePort" keeps the published port.
func TestPatchProjectKeepsWhatItLeavesOut(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	cfg := map[string]any{"memoryLimit": "1G", "extensions": []string{"intl", "pdo_mysql", "redis"}, "displayErrors": false}
	create := map[string]any{"name": "Shop", "php": map[string]any{"version": "8.4", "config": cfg},
		"database": map[string]any{"type": "mariadb", "version": "11", "exposePort": true},
		"web":      map[string]any{"type": "apache"}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	_, webVersion, _ := serviceConfig(t, r, "web")

	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"php": map[string]any{"version": "8.3"}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("php version = %d %s", r.status, r.raw)
	}
	got, version, _ := serviceConfig(t, r, "php")
	if version != "8.3" || got["memoryLimit"] != "1G" || got["displayErrors"] != false || !slices.Contains(anyStrings(got["extensions"]), "redis") {
		t.Fatalf("after the version change: %s %v", version, got)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"php": map[string]any{"config": map[string]any{"maxExecutionTime": 300}}}, true)
	got, version, _ = serviceConfig(t, r, "php")
	if r.status != http.StatusOK || version != "8.3" || got["memoryLimit"] != "1G" || got["maxExecutionTime"] != float64(300) || !slices.Contains(anyStrings(got["extensions"]), "redis") {
		t.Fatalf("after one setting: %d %s %v", r.status, version, got)
	}

	// The database keeps its published port, the web server its type and version.
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"database": map[string]any{"version": "11"}, "web": map[string]any{"spaFallback": false}}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("spaFallback on a PHP project = %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"database": map[string]any{"version": "11"}, "web": map[string]any{"version": ""}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("database = %d %s", r.status, r.raw)
	}
	db, _, _ := serviceConfig(t, r, "database")
	if port, _ := db["hostPort"].(float64); port == 0 {
		t.Fatalf("the database port was unpublished: %v", db)
	}
	if _, v, _ := serviceConfig(t, r, "web"); v != webVersion {
		t.Fatalf("web version %s, want %s", v, webVersion)
	}
	for _, s := range r.body["project"].(map[string]any)["services"].([]any) {
		if m := s.(map[string]any); m["kind"] == "web" && m["variant"] != "apache" {
			t.Fatalf("web server changed to %v", m["variant"])
		}
	}

	// Ruby: an object without "enabled" changes the setting and keeps the runtime.
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"ruby": map[string]any{"version": "3.4"}}, true)
	if _, _, ok := serviceConfig(t, r, "ruby"); r.status != http.StatusOK || !ok {
		t.Fatalf("add ruby = %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"ruby": map[string]any{"debug": true}}, true)
	rb, _, ok := serviceConfig(t, r, "ruby")
	if r.status != http.StatusOK || !ok || rb["debug"] != true {
		t.Fatalf("ruby without enabled = %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"ruby": map[string]any{"enabled": false}}, true)
	if _, _, ok := serviceConfig(t, r, "ruby"); r.status != http.StatusOK || ok {
		t.Fatalf("remove ruby = %d %s", r.status, r.raw)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}
