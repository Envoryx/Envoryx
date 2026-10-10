package api_test

import (
	"net/http"
	"testing"
)

func TestRestoreBackupIntoNewProjectEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/backups", map[string]any{"files": true}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("backup: %d %s", r.status, r.raw)
	}
	backup := r.body["backup"].(map[string]any)["id"].(string)

	r = a.do(http.MethodPost, "/api/v1/backups/"+id+"/"+backup+"/restore-new", map[string]any{"name": "Shop Again"}, true)
	if r.status != http.StatusCreated || r.body["project"].(map[string]any)["slug"] != "shop-again" {
		t.Fatalf("restore into a new project: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/backups/orphaned", nil, false); r.status != http.StatusOK || len(r.body["backups"].([]any)) != 0 {
		t.Fatalf("no orphans yet: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/projects/"+id, map[string]any{"confirm": "shop", "deleteFiles": true}, true); r.status != http.StatusNoContent && r.status != http.StatusOK {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/backups/orphaned", nil, false)
	if r.status != http.StatusOK || len(r.body["backups"].([]any)) != 1 {
		t.Fatalf("orphans: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/backups/orphaned/"+id+"/"+backup, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("delete orphan: %d %s", r.status, r.raw)
	}
	// A read token reaches none of it.
	tok := a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ro", "scope": "read"}, true)
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+"/api/v1/backups/orphaned", nil)
	req.Header.Set("Authorization", "Bearer "+tok.body["secret"].(string))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("read token: %d", res.StatusCode)
	}
}
