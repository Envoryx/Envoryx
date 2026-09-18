package api_test

import (
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
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "Claude Code"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
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
