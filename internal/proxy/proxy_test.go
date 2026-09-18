package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRoutingByHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Host", r.Host)
		w.Header().Set("X-Forwarded-Proto-Seen", r.Header.Get("X-Forwarded-Proto"))
		_, _ = io.WriteString(w, "hello from "+r.URL.Path)
	}))
	defer upstream.Close()
	dial := strings.TrimPrefix(upstream.URL, "http://")

	source := func(context.Context) (Table, error) {
		return Table{
			Routes: map[string]Target{
				"shop.test":    {ProjectName: "Shop", Slug: "shop", Dial: dial, Running: true},
				"stopped.test": {ProjectName: "Stopped", Slug: "stopped", Dial: dial, Running: false},
			},
			UIHosts:   map[string]bool{"staqio.test": true},
			StaqioURL: "http://staqio.test",
		}, nil
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	router := NewRouter(source, time.Second, log)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "staqio ui") })
	h := NewHandler(router, ui, false, log)
	srv := httptest.NewServer(h)
	defer srv.Close()

	get := func(host, path string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = host
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(body)
	}

	res, body := get("shop.test", "/index.php?x=1")
	if res.StatusCode != 200 || body != "hello from /index.php" || res.Header.Get("X-Seen-Host") != "shop.test" {
		t.Fatalf("proxied: %d %q host=%s", res.StatusCode, body, res.Header.Get("X-Seen-Host"))
	}
	if res, _ := get("SHOP.test:8080", "/"); res.StatusCode != 200 {
		t.Fatalf("host with port/case must route: %d", res.StatusCode)
	}
	res, body = get("stopped.test", "/")
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "is not running") {
		t.Fatalf("stopped: %d %q", res.StatusCode, body)
	}
	res, body = get("unknown.test", "/")
	if res.StatusCode != http.StatusNotFound || !strings.Contains(body, "unknown.test") || strings.Contains(body, "<script") {
		t.Fatalf("unknown: %d %q", res.StatusCode, body)
	}
	rec := httptest.NewRecorder()
	h.errorPage(rec, http.StatusNotFound, "x", "No project for <strong>"+template("<script>alert(1)</script>")+"</strong>.", "")
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("host must be escaped: %q", rec.Body.String())
	}
	res, body = get("staqio.test", "/projects")
	if res.StatusCode != 200 || body != "staqio ui" {
		t.Fatalf("ui host: %d %q", res.StatusCode, body)
	}
	res, body = get("192.168.1.10", "/")
	if body != "staqio ui" {
		t.Fatalf("bare ip must reach the ui: %q", body)
	}
}

func template(s string) string { return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s) }

func TestForceHTTPSRedirect(t *testing.T) {
	source := func(context.Context) (Table, error) {
		return Table{Routes: map[string]Target{"shop.test": {Running: true, Dial: "127.0.0.1:1"}}, UIHosts: map[string]bool{"staqio.test": true}, ForceHTTPS: true, HTTPSPort: 8443}, nil
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := NewHandler(NewRouter(source, time.Second, log), http.NotFoundHandler(), true, log)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://shop.test/path?q=1", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "https://shop.test:8443/path?q=1" {
		t.Fatalf("redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Bare IPs are never redirected (no certificate trust for IPs by default).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://192.168.1.10/", nil))
	if rec.Code == http.StatusTemporaryRedirect {
		t.Fatal("ip requests must not redirect")
	}
}

func TestRouterCachesAndInvalidates(t *testing.T) {
	calls := 0
	source := func(context.Context) (Table, error) {
		calls++
		return Table{}, nil
	}
	r := NewRouter(source, time.Hour, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	r.Table(context.Background())
	r.Table(context.Background())
	if calls != 1 {
		t.Fatalf("expected one source call, got %d", calls)
	}
	r.Invalidate()
	r.Table(context.Background())
	if calls != 2 {
		t.Fatalf("expected refresh after invalidate, got %d", calls)
	}
}
