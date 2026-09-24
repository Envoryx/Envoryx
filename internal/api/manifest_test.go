package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	yml := "version: 1\nname: Shop\ndocroot: public\nphp: {version: \"8.4\"}\nredis: true\ndomains: [api.shop.example]\nenv: {APP_ENV: local}\nsecrets: [STRIPE_SECRET]\n"
	r := a.do(http.MethodPost, "/api/v1/projects/from-manifest", map[string]any{"yaml": yml, "secrets": map[string]string{"STRIPE_SECRET": "sk_test"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	p := r.body["project"].(map[string]any)
	id := p["id"].(string)
	if p["slug"] != "shop" {
		t.Fatalf("project: %v", p)
	}

	// The export carries no secret value; the directory has no file yet.
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/manifest", nil, false)
	if r.status != http.StatusOK || strings.Contains(string(r.raw), "sk_test") {
		t.Fatalf("export: %d %s", r.status, r.raw)
	}
	if !strings.Contains(r.body["yaml"].(string), "api.shop.example") || r.body["repository"].(map[string]any)["present"] != false {
		t.Fatalf("export: %s", r.raw)
	}

	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/manifest/file", nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("write: %d %s", r.status, r.raw)
	}
	if _, err := os.Stat(filepath.Join(a.projDir, "shop", "envoryx.yml")); err != nil {
		t.Fatal(err)
	}
	repo := r.body["repository"].(map[string]any)
	if repo["present"] != true || repo["plan"].(map[string]any)["inSync"] != true {
		t.Fatalf("written file must be in sync: %s", r.raw)
	}

	// Plan and apply a changed manifest sent as text.
	next := strings.Replace(yml, "redis: true", "memcached: true", 1)
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/manifest/plan", map[string]any{"yaml": next}, true)
	if r.status != http.StatusOK {
		t.Fatalf("plan: %d %s", r.status, r.raw)
	}
	changes := r.body["plan"].(map[string]any)["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("plan: %s", r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/manifest/apply", map[string]any{"yaml": next, "prune": true}, true)
	if r.status != http.StatusOK {
		t.Fatalf("apply: %d %s", r.status, r.raw)
	}
	var kinds []string
	for _, s := range r.body["project"].(map[string]any)["services"].([]any) {
		kinds = append(kinds, s.(map[string]any)["kind"].(string))
	}
	if strings.Contains(strings.Join(kinds, ","), "redis") || !strings.Contains(strings.Join(kinds, ","), "memcached") {
		t.Fatalf("services after apply: %v", kinds)
	}

	// Without yaml the file in the project directory is used; it still has Redis.
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/manifest/plan", map[string]any{}, true)
	if r.status != http.StatusOK || !strings.Contains(string(r.raw), `"section":"redis"`) {
		t.Fatalf("plan from file: %d %s", r.status, r.raw)
	}

	// Broken input is a 400 with the reason.
	r = a.do(http.MethodPost, "/api/v1/projects/from-manifest", map[string]any{"yaml": "version: 1\nredsi: true\n", "name": "x"}, true)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(string(r.raw), "unknown key redsi") {
		t.Fatalf("broken manifest: %d %s", r.status, r.raw)
	}
}
