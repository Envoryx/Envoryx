package server

import (
	"context"
	"net/http"
	"net/http/httptest"
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
