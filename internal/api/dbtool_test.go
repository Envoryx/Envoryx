package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestDBToolEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()

	r := a.do(http.MethodGet, "/api/v1/dbtool", nil, false)
	if r.status != http.StatusOK || r.body["enabled"] != false {
		t.Fatalf("status: %d %s", r.status, r.raw)
	}
	create := map[string]any{"name": "Shop", "docroot": "public", "php": map[string]any{"version": "8.4", "config": map[string]any{}}, "database": map[string]any{"type": "mariadb", "version": "11"}, "createStarter": true, "start": true}
	r = a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/dbtool", nil, true)
	if r.status != http.StatusConflict || errCode(r) != "dbtool_disabled" {
		t.Fatalf("open while disabled: %d %s", r.status, r.raw)
	}
	// The proxy path is session-protected and answers 503 until the tool runs.
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+"/dbtool/", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/dbtool/ without session: %d", res.StatusCode)
	}
	req.AddCookie(a.cookie)
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/dbtool/ before start: %d", res.StatusCode)
	}

	r = a.do(http.MethodPut, "/api/v1/dbtool", map[string]any{"enabled": true}, true)
	if r.status != http.StatusOK || r.body["enabled"] != true {
		t.Fatalf("enable: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/dbtool", nil, true)
	if r.status != http.StatusOK || !strings.Contains(r.body["url"].(string), "server=envoryx-shop-database") {
		t.Fatalf("open: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/dbtool", nil, false)
	if r.body["running"] != true {
		t.Fatalf("status after open: %s", r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/audit?limit=5", nil, false)
	if !strings.Contains(string(r.raw), "project.dbtool_opened") {
		t.Fatalf("audit: %s", r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/dbtool", map[string]any{"enabled": false}, true)
	if r.status != http.StatusOK || r.body["running"] != false {
		t.Fatalf("disable: %d %s", r.status, r.raw)
	}
}
