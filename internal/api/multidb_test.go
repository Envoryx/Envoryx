package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdditionalDatabases(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{"name": "Shop", "start": true, "php": map[string]any{"version": "8.4"},
		"database":  map[string]any{"type": "mariadb", "version": "11"},
		"databases": []map[string]any{{"name": "analytics", "type": "postgresql"}}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	// No password of any database is part of the project.
	creds := a.do(http.MethodGet, "/api/v1/projects/"+id+"/database/credentials?db=analytics", nil, false)
	if creds.status != http.StatusOK {
		t.Fatalf("credentials = %d %s", creds.status, creds.raw)
	}
	pw := creds.body["credentials"].(map[string]any)["password"].(string)
	if c := creds.body["credentials"].(map[string]any); c["host"] != "analytics" || c["port"].(float64) != 5432 || pw == "" {
		t.Fatalf("credentials = %v", c)
	}
	if strings.Contains(string(r.raw), pw) || strings.Contains(string(a.do(http.MethodGet, "/api/v1/projects/"+id, nil, false).raw), pw) {
		t.Fatal("the project answer carries the additional database's password")
	}

	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/databases", nil, false)
	list, _ := r.body["databases"].([]any)
	if r.status != http.StatusOK || len(list) != 2 || list[1].(map[string]any)["name"] != "analytics" || list[1].(map[string]any)["service"] != "db-analytics" {
		t.Fatalf("databases = %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/database?db=analytics", nil, false); r.status != http.StatusOK || r.body["database"].(map[string]any)["type"] != "postgresql" {
		t.Fatalf("database?db = %d %s", r.status, r.raw)
	}
	for _, bad := range []string{"nope", "Not%20a%20name"} {
		if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/database?db="+bad, nil, false); r.status != http.StatusNotFound {
			t.Fatalf("database?db=%s = %d", bad, r.status)
		}
	}
	if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/db-analytics/logs?tail=5", nil, false); r.status != http.StatusOK {
		t.Fatalf("logs of the additional database = %d %s", r.status, r.raw)
	}

	// Add one, then remove it again.
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"databases": map[string]any{"legacy": map[string]any{"enabled": true, "type": "mysql"}}}, true)
	if r.status != http.StatusOK || !strings.Contains(string(r.raw), `"db-legacy"`) {
		t.Fatalf("add = %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"databases": map[string]any{"legacy": map[string]any{"enabled": false, "removeData": true}}}, true)
	if r.status != http.StatusOK || strings.Contains(string(r.raw), `"db-legacy"`) {
		t.Fatalf("remove = %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"databases": map[string]any{"web": map[string]any{"enabled": true}}}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("reserved name = %d %s", r.status, r.raw)
	}
}
