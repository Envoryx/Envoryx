package api_test

import (
	"net/http"
	"testing"
)

func TestTestSuitesRoute(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/tests", nil, false)
	if r.status != http.StatusOK || len(r.body["suites"].([]any)) != 0 || len(r.body["runs"].([]any)) != 0 {
		t.Fatalf("tests = %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/test-runs/00000000-0000-4000-8000-000000000000", nil, false); r.status != http.StatusNotFound {
		t.Fatalf("unknown run = %d", r.status)
	}
	// A suite the project does not have is refused before any socket is opened.
	if status := a.wsGet("/api/v1/projects/"+id+"/tests/phpunit/ws", ""); status != http.StatusNotFound {
		t.Fatalf("unknown suite = %d", status)
	}
}

// WebSocket routes start their session (an action, a test run, a shell) before the
// upgrade. A page elsewhere must not get that far: neither by navigating the browser to
// the route (a top-level GET carries the session cookie) nor by opening the socket.
func TestWebSocketRoutesRefuseOtherSites(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Shop", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	for _, route := range []string{"/actions/composer:install/ws", "/tests/phpunit/ws", "/services/php/terminal/ws", "/services/php/logs/ws"} {
		path := "/api/v1/projects/" + id + route
		if r := a.do(http.MethodGet, path, nil, false); r.status != http.StatusBadRequest {
			t.Errorf("%s as a plain GET = %d %s", route, r.status, r.raw)
		}
		if status := a.wsGet(path, "https://evil.example"); status != http.StatusForbidden {
			t.Errorf("%s from another origin = %d", route, status)
		}
	}
}

// wsGet sends a WebSocket upgrade request with the session cookie and returns the status.
func (a *testApp) wsGet(path, origin string) int {
	a.t.Helper()
	req, err := http.NewRequest(http.MethodGet, a.srv.URL+path, nil)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.AddCookie(a.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
