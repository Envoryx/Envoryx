package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGate(t *testing.T) {
	var seenCookie atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie.Store(r.Header.Get("Cookie"))
		_, _ = io.WriteString(w, "phpmyadmin "+r.URL.RequestURI())
	}))
	defer upstream.Close()
	dial := strings.TrimPrefix(upstream.URL, "http://")
	source := func(context.Context) (Table, error) {
		return Table{
			Routes: map[string]Target{
				"shop-pma.test": {ProjectID: "p1", ProjectName: "Shop (phpMyAdmin)", Dial: dial, Running: true, Login: "operate"},
				"blog-pma.test": {ProjectID: "p2", ProjectName: "Blog (phpMyAdmin)", Dial: dial, Running: true, Login: "operate"},
				"shop.test":     {ProjectID: "p1", ProjectName: "Shop", Dial: dial, Running: true},
				"shop-dev.test": {ProjectID: "p1", ProjectName: "Shop (dev server)", Dial: dial, Running: true, Login: "read"},
			},
			EnvoryxURL: "http://envoryx.test",
		}, nil
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	router := NewRouter(source, time.Second, log)
	var allowed atomic.Bool
	allowed.Store(true)
	checks := atomic.Int32{}
	gate := NewGate(func(_ context.Context, sessionID, projectID, need string) bool {
		checks.Add(1)
		return allowed.Load() && sessionID == "s1" && projectID == "p1"
	})
	now := time.Now()
	gate.now = func() time.Time { return now }
	gate.SetRouter(router)
	h := NewHandler(router, http.NotFoundHandler(), false, log)
	h.SetGate(gate)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(host, path string, header map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = host
		for k, v := range header {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(body)
	}
	navigate := map[string]string{"Sec-Fetch-Mode": "navigate"}

	// A browser without a pass goes to Envoryx with the address it asked for.
	res, _ := do("shop-pma.test", "/index.php?db=shop", navigate)
	if res.StatusCode != http.StatusFound {
		t.Fatalf("without a pass: %d", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if loc.Host != "envoryx.test" || loc.Path != "/gate" || loc.Query().Get("to") != "http://shop-pma.test/index.php?db=shop" {
		t.Fatalf("sign-in address: %s", loc)
	}
	// A script gets a refusal, not a redirect.
	if res, _ := do("shop-pma.test", "/api", map[string]string{"Sec-Fetch-Mode": "cors"}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("script without a pass: %d", res.StatusCode)
	}
	// The open host stays open.
	if res, _ := do("shop.test", "/", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("open host: %d", res.StatusCode)
	}

	// Envoryx issues a code; the proxy swaps it for a cookie and sends the browser on.
	u, target, err := gate.Resolve(context.Background(), loc.Query().Get("to"))
	if err != nil || target.ProjectID != "p1" || target.Login != "operate" {
		t.Fatalf("resolve: %v %+v", err, target)
	}
	if _, _, err := gate.Resolve(context.Background(), "http://shop.test/"); err != ErrNotGated {
		t.Fatalf("an open host is not gated: %v", err)
	}
	if _, _, err := gate.Resolve(context.Background(), "javascript:alert(1)"); err != ErrNotGated {
		t.Fatalf("odd address: %v", err)
	}
	back, err := gate.Issue("s1", u, target)
	if err != nil {
		t.Fatal(err)
	}
	backURL, _ := url.Parse(back)
	if backURL.Host != "shop-pma.test" || backURL.Path != GatePath {
		t.Fatalf("back address: %s", back)
	}
	// The code is only good on its own host.
	if res, _ := do("blog-pma.test", GatePath+"?"+backURL.RawQuery, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("code on another host: %d", res.StatusCode)
	}
	back, _ = gate.Issue("s1", u, target)
	backURL, _ = url.Parse(back)
	res, _ = do("shop-pma.test", GatePath+"?"+backURL.RawQuery, nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/index.php?db=shop" {
		t.Fatalf("redeem: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	var pass *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == gateCookie {
			pass = c
		}
	}
	if pass == nil || !pass.HttpOnly {
		t.Fatalf("pass cookie: %+v", pass)
	}
	// Used up.
	if res, _ := do("shop-pma.test", GatePath+"?"+backURL.RawQuery, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("second redeem: %d", res.StatusCode)
	}

	withPass := map[string]string{"Cookie": gateCookie + "=" + pass.Value + "; app=1"}
	res, body := do("shop-pma.test", "/index.php", withPass)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(body, "phpmyadmin") {
		t.Fatalf("with pass: %d %q", res.StatusCode, body)
	}
	if c := seenCookie.Load().(string); c != "app=1" {
		t.Fatalf("the application saw cookie %q", c)
	}
	// The pass belongs to its host.
	if res, _ := do("shop-dev.test", "/", withPass); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("pass on another host: %d", res.StatusCode)
	}
	if checks.Load() != 0 {
		t.Fatalf("fresh pass checked %d times", checks.Load())
	}

	// After a while the session is asked again; signed out, the pass is gone.
	now = now.Add(gateRecheck + time.Second)
	if res, _ := do("shop-pma.test", "/", withPass); res.StatusCode != http.StatusOK || checks.Load() != 1 {
		t.Fatalf("recheck: %d after %d checks", res.StatusCode, checks.Load())
	}
	now = now.Add(gateRecheck + time.Second)
	allowed.Store(false)
	if res, _ := do("shop-pma.test", "/", withPass); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
	allowed.Store(true)
	if res, _ := do("shop-pma.test", "/", withPass); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a dropped pass stays dropped: %d", res.StatusCode)
	}

	// An old code is refused.
	back, _ = gate.Issue("s1", u, target)
	backURL, _ = url.Parse(back)
	now = now.Add(gateCodeTTL + time.Second)
	if res, _ := do("shop-pma.test", GatePath+"?"+backURL.RawQuery, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("old code: %d", res.StatusCode)
	}
}

func TestGateIssueKeepsThePathOnTheHost(t *testing.T) {
	g := NewGate(nil)
	for _, raw := range []string{"http://a.test", "http://a.test//evil.example/x", "http://a.test" + GatePath + "?code=x"} {
		u, _ := url.Parse(raw)
		back, err := g.Issue("s", u, Target{ProjectID: "p", Login: "read"})
		if err != nil {
			t.Fatal(err)
		}
		code, _ := url.Parse(back)
		e := g.codes[code.Query().Get("code")]
		if !strings.HasPrefix(e.path, "/") || strings.HasPrefix(e.path, "//") || strings.HasPrefix(e.path, GatePath) {
			t.Fatalf("%s: path %q", raw, e.path)
		}
	}
}

func TestGateClosedWithoutGate(t *testing.T) {
	source := func(context.Context) (Table, error) {
		return Table{Routes: map[string]Target{"shop-pma.test": {ProjectName: "Shop", Dial: "127.0.0.1:1", Running: true, Login: "operate"}}}, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(NewRouter(source, time.Second, log), http.NotFoundHandler(), false, log)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "shop-pma.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no gate: %d", rec.Code)
	}
}
