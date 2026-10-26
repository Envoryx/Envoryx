package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diagnosticsRoute(errors int) map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/system/diagnostics": func(w http.ResponseWriter, r *http.Request) {
			checks := []map[string]any{
				{"id": "docker", "category": "runtime", "status": "ok", "title": "Docker engine reachable", "detail": "29.1"},
				{"id": "dns", "category": "network", "status": "warning", "title": "Wildcard DNS", "detail": "x.shop.test does not resolve", "hint": "Point *.test at this host. The check from this browser above shows whether project domains work."},
			}
			if errors > 0 {
				checks = append(checks, map[string]any{"id": "disk", "category": "runtime", "status": "error", "title": "Disk space", "detail": "2% free"})
			}
			writeJSON(w, map[string]any{"checks": checks, "summary": map[string]int{"ok": 1, "warning": 1, "error": errors}})
		},
	}
}

// Checks that are fine stay out of the way; errors fail the command, warnings only
// with --strict.
func TestDiagnostics(t *testing.T) {
	srv := newFakeServer(t, diagnosticsRoute(0))
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"diagnostics"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "Docker engine reachable") || !strings.Contains(text, "WARNING") || !strings.Contains(text, "→ Point *.test at this host") || !strings.Contains(text, "1 ok, 0 info, 1 warning, 0 errors") || strings.Contains(text, "browser above") {
		t.Fatalf("output:\n%s", text)
	}
	if err := c.run(context.Background(), []string{"diagnostics", "--strict"}); err == nil || err.Error() != "1 check needs attention" {
		t.Fatalf("--strict: %v", err)
	}
	out.Reset()
	if err := c.run(context.Background(), []string{"diagnostics", "--all"}); err != nil || !strings.Contains(out.String(), "Docker engine reachable") {
		t.Fatalf("--all: %v\n%s", err, out.String())
	}

	srv = newFakeServer(t, diagnosticsRoute(1))
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"diagnostics"}); err == nil {
		t.Fatal("an error check must fail the command")
	}
}

func TestAuditFiltersAndExport(t *testing.T) {
	var query string
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/audit": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			writeJSON(w, map[string]any{"next": "cursor", "entries": []map[string]any{
				{"createdAt": "2026-10-12T06:00:00Z", "username": "stefan", "action": "project.started", "targetType": "project",
					"targetId": "11111111-1111-4111-8111-111111111111", "details": map[string]any{"name": "Acme Shop"}, "ip": "10.0.0.2"},
			}})
		},
		"GET /api/v1/audit/export": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			w.Header().Set("Content-Disposition", `attachment; filename="envoryx-audit-1.csv"`)
			_, _ = w.Write([]byte("time,user\n"))
		},
	})
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"audit", "--project", "acme-shop", "--action", "project.", "--action", "backup.", "--since", "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project=11111111-1111-4111-8111-111111111111", "action=project.", "action=backup.", "since=2026-10-01", "limit=50"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query %q lacks %q", query, want)
		}
	}
	for _, want := range []string{"stefan", "project.started", "Acme Shop", "10.0.0.2", "There is more"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "audit.csv")
	if err := c.run(context.Background(), []string{"audit", "--export", "csv", "--user", "stefan", "-o", target}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "time,user\n" || !strings.Contains(query, "format=csv") || !strings.Contains(query, "user=stefan") {
		t.Fatalf("export %q, query %q", b, query)
	}
	if err := c.run(context.Background(), []string{"audit", "--project", "nope"}); err == nil {
		t.Fatal("an unknown project name must fail")
	}
}
