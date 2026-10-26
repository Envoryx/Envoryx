package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// The page's policy lets the diagnostics reach their probe host on the proxy (any port,
// http and https) and nothing else outside the UI.
func TestPolicyAllowsTheDiagnosticsProbe(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	probe := func(context.Context) string { return "envoryx-diagnostics-probe.test" }
	rec := httptest.NewRecorder()
	securityHeaders(ok, probe).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"connect-src 'self' ws: wss: http://envoryx-diagnostics-probe.test:* https://envoryx-diagnostics-probe.test:*;", "default-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy %q lacks %q", csp, want)
		}
	}
	rec = httptest.NewRecorder()
	securityHeaders(ok, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self' ws: wss:;") {
		t.Errorf("without a probe host: %q", csp)
	}
}

// A request from a project's network is refused before it reaches the interface.
func TestProjectNetworkGuard(t *testing.T) {
	inside := func(a netip.Addr) bool { return netip.MustParsePrefix("10.213.0.0/24").Contains(a) }
	h := projectNetworkGuard(inside)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for addr, want := range map[string]int{"10.213.0.5:41234": http.StatusForbidden, "[::ffff:10.213.0.5]:41234": http.StatusForbidden, "172.17.0.1:5000": http.StatusOK, "127.0.0.1:5000": http.StatusOK} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		r.RemoteAddr = addr
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d, want %d", addr, w.Code, want)
		}
	}
}
