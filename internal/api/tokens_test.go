package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAPITokensAndMCPEndpoint(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()

	r := a.do(http.MethodGet, "/api/v1/tokens", nil, false)
	if r.status != http.StatusOK || len(r.body["tokens"].([]any)) != 0 || !strings.HasSuffix(r.body["mcpUrl"].(string), "/mcp") {
		t.Fatalf("list: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "   "}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("empty name: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "Claude Code", "scope": "admin"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	if got := r.body["token"].(map[string]any)["scope"]; got != "admin" {
		t.Fatalf("scope in response = %v", got)
	}
	secret := r.body["secret"].(string)
	id := r.body["token"].(map[string]any)["id"].(string)
	if !strings.HasPrefix(secret, "stq_") {
		t.Fatalf("secret format: %s", secret)
	}
	r = a.do(http.MethodGet, "/api/v1/tokens", nil, false)
	if strings.Contains(string(r.raw), secret) {
		t.Fatal("listing must never contain the secret")
	}

	// The MCP endpoint refuses session cookies and accepts the bearer token.
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/mcp", strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.AddCookie(a.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie auth must be rejected on /mcp: %d", res.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, a.srv.URL+"/mcp", strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bearer auth on /mcp: %d", res.StatusCode)
	}

	// The REST API accepts the same bearer token: no cookie, no CSRF header needed.
	bearer := func(method, path string, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, a.srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := bearer(http.MethodGet, "/api/v1/auth/me", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("bearer GET on REST: %d", res.StatusCode)
	}
	if res := bearer(http.MethodPost, "/api/v1/projects/preview", `{"name":"Bearer"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("bearer POST on REST without X-Requested-With: %d", res.StatusCode)
	}
	if res := bearer(http.MethodPatch, "/api/v1/settings", `{"xdebugClientHost":"10.0.0.7"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("bearer PATCH on REST: %d", res.StatusCode)
	}
	// Account and token management stay reserved for browser sessions.
	if res := bearer(http.MethodPost, "/api/v1/auth/password", `{"currentPassword":"supersecret123","newPassword":"anothersecret123"}`); res.StatusCode != http.StatusForbidden {
		t.Fatalf("bearer must not change the password: %d", res.StatusCode)
	}
	if res := bearer(http.MethodPost, "/api/v1/tokens", `{"name":"chained"}`); res.StatusCode != http.StatusForbidden {
		t.Fatalf("bearer must not create tokens: %d", res.StatusCode)
	}
	if res := bearer(http.MethodDelete, "/api/v1/tokens/"+id, ""); res.StatusCode != http.StatusForbidden {
		t.Fatalf("bearer must not revoke tokens: %d", res.StatusCode)
	}
	// A wrong bearer token does not fall back to the cookie.
	req, _ = http.NewRequest(http.MethodGet, a.srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer stq_wrongwrongwrongwrongwrongwrong")
	req.AddCookie(a.cookie)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid bearer with cookie: %d", res.StatusCode)
	}
	r = a.do(http.MethodGet, "/api/v1/audit?limit=10", nil, false)
	if !strings.Contains(string(r.raw), "(token: Claude Code)") {
		t.Fatalf("audit must attribute bearer requests to the token: %s", r.raw)
	}

	r = a.do(http.MethodDelete, "/api/v1/tokens/"+id, nil, true)
	if r.status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, "/api/v1/tokens/"+id, nil, true)
	if r.status != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", r.status)
	}
	r = a.do(http.MethodGet, "/api/v1/audit?limit=10", nil, false)
	if !strings.Contains(string(r.raw), "token.created") || !strings.Contains(string(r.raw), "token.revoked") {
		t.Fatalf("audit must record token changes: %s", r.raw)
	}
}

func TestTokenScopesAndProjectRestriction(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	// Two projects; the confined token may only touch the first.
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"}, "database": map[string]any{"type": "mariadb", "version": "11.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create shop: %d %s", r.status, r.raw)
	}
	shop := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Blog", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create blog: %d %s", r.status, r.raw)
	}
	blog := r.body["project"].(map[string]any)["id"].(string)

	mint := func(name, scope string, projects ...string) string {
		t.Helper()
		body := map[string]any{"name": name, "scope": scope}
		if projects != nil {
			body["projects"] = projects
		}
		r := a.do(http.MethodPost, "/api/v1/tokens", body, true)
		if r.status != http.StatusCreated {
			t.Fatalf("mint %s: %d %s", name, r.status, r.raw)
		}
		return r.body["secret"].(string)
	}
	call := func(secret, method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, a.srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	expect := func(secret, method, path, body string, want int) {
		t.Helper()
		if got, raw := call(secret, method, path, body); got != want {
			t.Fatalf("%s %s with this token: %d, want %d (%s)", method, path, got, want, raw)
		}
	}

	// Validation: unknown scope, unknown project; the default scope is operate.
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "x", "scope": "root"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown scope: %d", r.status)
	}
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "x", "projects": []string{"00000000-0000-0000-0000-000000000000"}}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown project: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "default"}, true)
	if r.status != http.StatusCreated || r.body["token"].(map[string]any)["scope"] != "operate" {
		t.Fatalf("default scope: %d %s", r.status, r.raw)
	}

	// read: looks, never touches, sees no secrets.
	rd := mint("monitor", "read")
	expect(rd, http.MethodGet, "/api/v1/projects", "", http.StatusOK)
	expect(rd, http.MethodGet, "/api/v1/projects/"+shop+"/services", "", http.StatusOK)
	expect(rd, http.MethodGet, "/api/v1/projects/"+shop+"/backups", "", http.StatusOK)
	expect(rd, http.MethodGet, "/api/v1/projects/"+shop+"/database/credentials", "", http.StatusForbidden)
	expect(rd, http.MethodPost, "/api/v1/projects/"+shop+"/restart", "", http.StatusForbidden)
	expect(rd, http.MethodGet, "/api/v1/settings", "", http.StatusForbidden)
	expect(rd, http.MethodGet, "/api/v1/dashboard", "", http.StatusForbidden)
	if _, raw := call(rd, http.MethodPost, "/api/v1/projects/"+shop+"/restart", ""); !strings.Contains(raw, "read scope") || !strings.Contains(raw, "needs operate") {
		t.Fatalf("forbidden message must explain: %s", raw)
	}
	if _, raw := call(rd, http.MethodGet, "/api/v1/auth/me", ""); !strings.Contains(raw, `"scope":"read"`) {
		t.Fatalf("me must show the token scope: %s", raw)
	}

	// operate: works with existing projects, cannot create/delete or change settings.
	op := mint("assistant", "operate")
	expect(op, http.MethodPost, "/api/v1/projects/"+shop+"/restart", "", http.StatusOK)
	expect(op, http.MethodGet, "/api/v1/projects/"+shop+"/database/credentials", "", http.StatusOK)
	expect(op, http.MethodPost, "/api/v1/projects/"+shop+"/database/databases", `{"name":"extra"}`, http.StatusCreated)
	expect(op, http.MethodDelete, "/api/v1/projects/"+shop+"/database/databases/extra", "", http.StatusForbidden)
	expect(op, http.MethodPost, "/api/v1/projects", `{"name":"Nope"}`, http.StatusForbidden)
	expect(op, http.MethodDelete, "/api/v1/projects/"+blog, `{"confirm":"blog"}`, http.StatusForbidden)
	expect(op, http.MethodPatch, "/api/v1/settings", `{"xdebugClientHost":"10.0.0.7"}`, http.StatusForbidden)
	expect(op, http.MethodPost, "/api/v1/docker/images/prune", "", http.StatusForbidden)

	// operate, confined to shop: blog is invisible, instance-wide routes are closed.
	confined := mint("shop only", "operate", shop)
	expect(confined, http.MethodGet, "/api/v1/projects/"+shop, "", http.StatusOK)
	expect(confined, http.MethodPost, "/api/v1/projects/"+shop+"/restart", "", http.StatusOK)
	expect(confined, http.MethodGet, "/api/v1/projects/"+blog, "", http.StatusForbidden)
	expect(confined, http.MethodPost, "/api/v1/projects/"+blog+"/restart", "", http.StatusForbidden)
	expect(confined, http.MethodGet, "/api/v1/runtimes", "", http.StatusOK)
	expect(confined, http.MethodGet, "/api/v1/docker", "", http.StatusForbidden)
	expect(confined, http.MethodPost, "/api/v1/projects/preview", `{"name":"x"}`, http.StatusForbidden)
	if _, raw := call(confined, http.MethodGet, "/api/v1/projects", ""); !strings.Contains(raw, shop) || strings.Contains(raw, blog) {
		t.Fatalf("project list must be filtered to the token's projects: %s", raw)
	}
	// Admin scope does not lift a project restriction.
	confinedAdmin := mint("shop admin", "admin", shop)
	expect(confinedAdmin, http.MethodPost, "/api/v1/projects", `{"name":"Nope"}`, http.StatusForbidden)
	expect(confinedAdmin, http.MethodPatch, "/api/v1/projects/"+blog, `{"name":"Renamed"}`, http.StatusForbidden)
	// A copy is a new project, which a confined token may not create either.
	expect(confinedAdmin, http.MethodPost, "/api/v1/projects/"+shop+"/duplicate", `{"name":"Shop Copy"}`, http.StatusForbidden)
	expect(confinedAdmin, http.MethodGet, "/api/v1/projects/"+shop+"/plan", "", http.StatusOK)

	// Tokens carry scope and projects in the listing; audit records them.
	r = a.do(http.MethodGet, "/api/v1/tokens", nil, false)
	if !strings.Contains(string(r.raw), `"scope":"read"`) || !strings.Contains(string(r.raw), `"projects":["`+shop+`"]`) {
		t.Fatalf("listing: %s", r.raw)
	}
}
