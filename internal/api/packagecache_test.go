package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageCache(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	dir := filepath.Join(a.cfgDir, "cache", "npm")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "blob"), make([]byte, 1234), 0o644)

	r := a.do(http.MethodGet, "/api/v1/package-cache", nil, false)
	if r.status != http.StatusOK || r.body["cache"].(map[string]any)["bytes"].(float64) != 1234 {
		t.Fatalf("get = %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/package-cache?tool=nope", nil, true); r.status != http.StatusNotFound {
		t.Fatalf("unknown tool = %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, "/api/v1/package-cache", nil, true)
	if r.status != http.StatusOK || r.body["cache"].(map[string]any)["bytes"].(float64) != 0 {
		t.Fatalf("clear = %d %s", r.status, r.raw)
	}
}
