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
	back, err := gate.Issue(context.Background(), "s1", u, target)
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
	back, _ = gate.Issue(context.Background(), "s1", u, target)
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
	back, _ = gate.Issue(context.Background(), "s1", u, target)
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
		back, err := g.Issue(context.Background(), "s", u, Target{ProjectID: "p", Login: "read"})
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

// A page of the application loads its scripts from the dev server: one sign-in gives
// both hosts a pass, the dev server's on the way.
func TestGatePassesTheDevServerOnTheWay(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer upstream.Close()
	dial := strings.TrimPrefix(upstream.URL, "http://")
	source := func(context.Context) (Table, error) {
		return Table{Routes: map[string]Target{
			"shop.test":     {ProjectID: "p1", ProjectName: "Shop", Dial: dial, Running: true, Login: "read", LoginAlso: []string{"shop-dev.test", "blog-dev.test"}},
			"shop-dev.test": {ProjectID: "p1", ProjectName: "Shop (dev server)", Dial: dial, Running: true, Login: "read"},
			"blog-dev.test": {ProjectID: "p2", ProjectName: "Blog (dev server)", Dial: dial, Running: true, Login: "read"},
		}, EnvoryxURL: "http://envoryx.test"}, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(source, time.Second, log)
	gate := NewGate(func(context.Context, string, string, string) bool { return true })
	gate.SetRouter(router)
	h := NewHandler(router, http.NotFoundHandler(), false, log)
	h.SetGate(gate)
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(u *url.URL, cookie string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+u.RequestURI(), nil)
		req.Host = u.Host
		if cookie != "" {
			req.Header.Set("Cookie", gateCookie+"="+cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	pass := func(res *http.Response) string {
		for _, c := range res.Cookies() {
			if c.Name == gateCookie {
				return c.Value
			}
		}
		return ""
	}

	u, target, err := gate.Resolve(context.Background(), "http://shop.test:8080/cart")
	if err != nil {
		t.Fatal(err)
	}
	back, _ := gate.Issue(context.Background(), "s1", u, target)
	first, _ := url.Parse(back)
	if first.Host != "shop-dev.test:8080" {
		t.Fatalf("first hop: %s", back)
	}
	res := get(first, "")
	devPass := pass(res)
	second, _ := url.Parse(res.Header.Get("Location"))
	if devPass == "" || second.Path != GatePath {
		t.Fatalf("dev hop: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if second.Host != "shop.test:8080" {
		t.Fatalf("another project's dev server must not be on the way: %s", second)
	}
	res = get(second, "")
	appPass := pass(res)
	if appPass == "" || res.Header.Get("Location") != "/cart" {
		t.Fatalf("last hop: %s", res.Header.Get("Location"))
	}
	if res := get(&url.URL{Host: "shop-dev.test", Path: "/@vite/client"}, devPass); res.StatusCode != http.StatusOK {
		t.Fatalf("dev server with its pass: %d", res.StatusCode)
	}
	if res := get(&url.URL{Host: "shop.test", Path: "/"}, appPass); res.StatusCode != http.StatusOK {
		t.Fatalf("app with its pass: %d", res.StatusCode)
	}
}

func TestGateOpenPathsAndTokens(t *testing.T) {
	var seen atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get(TokenHeader))
		_, _ = io.WriteString(w, "app "+r.URL.Path)
	}))
	defer upstream.Close()
	dial := strings.TrimPrefix(upstream.URL, "http://")
	source := func(context.Context) (Table, error) {
		return Table{Routes: map[string]Target{
			"shop.test": {ProjectID: "p1", ProjectName: "Shop", Dial: dial, Running: true, Login: "read", LoginExcept: []string{"/webhooks/*", "/up"}},
		}, EnvoryxURL: "http://envoryx.test"}, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(source, time.Second, log)
	gate := NewGate(func(context.Context, string, string, string) bool { return false })
	checks := 0
	gate.SetTokenCheck(func(_ context.Context, token, projectID, need string) bool {
		checks++
		return token == "envx_good" && projectID == "p1" && need == "read"
	})
	h := NewHandler(router, http.NotFoundHandler(), false, log)
	h.SetGate(gate)
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(rawPath string, header map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+rawPath, nil)
		req.Host = "shop.test"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for path, want := range map[string]int{
		"/webhooks/stripe":         200,
		"/up":                      200,
		"/up/more":                 401,
		"/admin":                   401,
		"/webhooks/../admin":       401,
		"/webhooks/%2e%2e/admin":   401,
		"/webhooks/x%2F..%2Fadmin": 401,
		"/webhooks//admin":         401,
		"/webhooks\\..\\admin":     401,
	} {
		if got := get(path, nil); got != want {
			t.Errorf("%s: %d, want %d", path, got, want)
		}
	}
	if got := get("/admin", map[string]string{TokenHeader: "envx_bad"}); got != 401 {
		t.Fatalf("bad token: %d", got)
	}
	if got := get("/admin", map[string]string{TokenHeader: "envx_good"}); got != 200 || seen.Load().(string) != "" {
		t.Fatalf("good token: %d, the app saw %q", got, seen.Load())
	}
	before := checks
	get("/admin", map[string]string{TokenHeader: "envx_good"})
	if checks != before {
		t.Fatal("a fresh token check must be remembered")
	}
}

func TestOpenPath(t *testing.T) {
	open := []string{"/webhooks/*", "/up"}
	for raw, want := range map[string]bool{
		"/webhooks/stripe": true, "/up": true, "/upload": false, "/": false,
		"/webhooks/../admin": false, "/webhooks/%2e%2e/admin": false, "/webhooks/a%2Fb": false,
		"/webhooks//x": false, "/webhooks/a\\b": false, "/webhooks/./x": false,
	} {
		r := httptest.NewRequest(http.MethodGet, "http://shop.test"+raw, nil)
		if got := openPath(r, open); got != want {
			t.Errorf("%s: %v, want %v", raw, got, want)
		}
	}
}

func TestGuestLink(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer upstream.Close()
	dial := strings.TrimPrefix(upstream.URL, "http://")
	source := func(context.Context) (Table, error) {
		return Table{Routes: map[string]Target{
			"shop.test":     {ProjectID: "p1", ProjectName: "Shop", Dial: dial, Running: true, Login: "read", LoginAlso: []string{"shop-dev.test"}},
			"shop-dev.test": {ProjectID: "p1", ProjectName: "Shop (dev server)", Dial: dial, Running: true, Login: "read"},
			"shop-pma.test": {ProjectID: "p1", ProjectName: "Shop (phpMyAdmin)", Dial: dial, Running: true, Login: "operate"},
			"blog.test":     {ProjectID: "p2", ProjectName: "Blog", Dial: dial, Running: true, Login: "read"},
		}, EnvoryxURL: "http://envoryx.test"}, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(source, time.Second, log)
	gate := NewGate(func(context.Context, string, string, string) bool {
		t.Error("a guest pass must not ask about a session")
		return false
	})
	var valid atomic.Bool
	valid.Store(true)
	gate.SetGuestCheck(func(_ context.Context, token string) (string, bool) {
		return "p1", valid.Load() && token == "envg_ok"
	})
	now := time.Now()
	gate.now = func() time.Time { return now }
	gate.SetRouter(router)
	h := NewHandler(router, http.NotFoundHandler(), false, log)
	h.SetGate(gate)
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(host, path, cookie string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = host
		if cookie != "" {
			req.Header.Set("Cookie", gateCookie+"="+cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	pass := func(res *http.Response) string {
		for _, c := range res.Cookies() {
			if c.Name == gateCookie {
				return c.Value
			}
		}
		return ""
	}
	for host, token := range map[string]string{"shop.test": "envg_bad", "blog.test": "envg_ok", "shop-pma.test": "envg_ok"} {
		if res := get(host, GuestPath+"?t="+token, ""); res.StatusCode != http.StatusForbidden {
			t.Errorf("%s with %s: %d", host, token, res.StatusCode)
		}
	}
	res := get("shop.test", GuestPath+"?t=envg_ok", "")
	appPass := pass(res)
	hop, _ := url.Parse(res.Header.Get("Location"))
	if appPass == "" || hop.Host != "shop-dev.test" || hop.Path != GatePath || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("guest redeem: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res = get("shop-dev.test", hop.RequestURI(), "")
	devPass := pass(res)
	back, _ := url.Parse(res.Header.Get("Location"))
	if devPass == "" || back.Host != "shop.test" {
		t.Fatalf("dev hop: %s", res.Header.Get("Location"))
	}
	if res := get("shop.test", back.RequestURI(), ""); res.Header.Get("Location") != "/" {
		t.Fatalf("back: %s", res.Header.Get("Location"))
	}
	if res := get("shop.test", "/", appPass); res.StatusCode != http.StatusOK {
		t.Fatalf("app with the guest pass: %d", res.StatusCode)
	}
	if res := get("shop-dev.test", "/@vite/client", devPass); res.StatusCode != http.StatusOK {
		t.Fatalf("dev server with the guest pass: %d", res.StatusCode)
	}
	// The cookie is the link itself: an Envoryx restart (no passes in memory) keeps the
	// guest in.
	if appPass != "envg_ok" {
		t.Fatalf("guest cookie: %q", appPass)
	}
	gate.mu.Lock()
	clear(gate.passes)
	gate.mu.Unlock()
	if res := get("shop.test", "/", appPass); res.StatusCode != http.StatusOK {
		t.Fatalf("after a restart: %d", res.StatusCode)
	}
	// Revoked, the browser is out at the next look.
	valid.Store(false)
	now = now.Add(gateRecheck + time.Second)
	if res := get("shop.test", "/", appPass); res.StatusCode == http.StatusOK {
		t.Fatal("a revoked guest link must close the project")
	}
}
