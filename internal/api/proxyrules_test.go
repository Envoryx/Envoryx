package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
)

func TestProxyRulesEndpoint(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "createStarter": true, "php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()}}, true)
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/proxy-rules", map[string]any{
		"basicAuth": map[string]any{"user": "client", "password": "s3cret"},
		"headers":   []any{map[string]any{"name": "X-Robots-Tag", "value": "noindex"}},
	}, true)
	if r.status != http.StatusOK {
		t.Fatalf("set: %d %s", r.status, r.raw)
	}
	// The password hash stays on the server.
	if strings.Contains(string(r.raw), "passwordHash") || strings.Contains(string(r.raw), "$2a$") {
		t.Fatalf("hash in the answer: %s", r.raw)
	}
	rules := r.body["project"].(map[string]any)["proxyRules"].(map[string]any)
	if rules["basicAuth"].(map[string]any)["user"] != "client" || len(rules["headers"].([]any)) != 1 {
		t.Fatalf("rules: %v", rules)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/proxy-rules", map[string]any{"redirects": []any{map[string]any{"from": "old", "to": "/new"}}}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad redirect: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/proxy-rules", map[string]any{}, true)
	if r.status != http.StatusOK || r.body["project"].(map[string]any)["proxyRules"] != nil {
		t.Fatalf("off: %d %s", r.status, r.raw)
	}
}
