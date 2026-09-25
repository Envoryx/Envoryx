package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// rulesServer proxies shop.test, with rules, to an upstream that echoes what it got;
// without share it sends plain http to https first.
func rulesServer(t *testing.T, rules *Rules, share bool) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Powered-By", "PHP")
		w.Header().Set("X-Auth-Seen", r.Header.Get("Authorization"))
		w.Header().Set("X-Proto-Seen", r.Header.Get("X-Forwarded-Proto"))
		_, _ = io.WriteString(w, "app "+r.URL.RequestURI())
	}))
	t.Cleanup(upstream.Close)
	dial := strings.TrimPrefix(upstream.URL, "http://")
	source := func(context.Context) (Table, error) {
		return Table{Routes: map[string]Target{
			"shop.test":       {ProjectName: "Shop", Dial: dial, Running: true, Rules: rules},
			"www.shop.test":   {ProjectName: "Shop", Dial: dial, Running: true, Rules: rules},
			"x.trycloudflare": {ProjectName: "Shop", Dial: dial, Running: true, Rules: rules, Share: share},
		}, ForceHTTPS: !share}, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(NewRouter(source, time.Second, log), http.NotFoundHandler(), true, log)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func send(t *testing.T, srv *httptest.Server, method, host, path string, header map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, nil)
	req.Host = host
	for k, v := range header {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestAllowlistAdmitsOnlyItsNetworks(t *testing.T) {
	srv := rulesServer(t, &Rules{Allow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, false)
	if resp := send(t, srv, "GET", "shop.test", "/", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("127.0.0.1 got %d", resp.StatusCode)
	}
	srv = rulesServer(t, &Rules{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}, false)
	// Admitted, then sent to https like any request.
	if resp := send(t, srv, "GET", "shop.test", "/", nil); resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("127.0.0.1 got %d", resp.StatusCode)
	}
}

func TestBasicAuth(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	srv := rulesServer(t, &Rules{BasicAuth: &BasicAuth{User: "client", Hash: string(hash), Realm: "Shop"}}, true)
	resp := send(t, srv, "GET", "x.trycloudflare", "/", nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `realm="Shop"`) {
		t.Fatalf("no credentials: %d %v", resp.StatusCode, resp.Header)
	}
	for _, pw := range []string{"wrong", ""} {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		req.Host = "x.trycloudflare"
		req.SetBasicAuth("client", pw)
		r, _ := http.DefaultClient.Do(req)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("password %q: %d", pw, r.StatusCode)
		}
	}
	for range 2 { // the second answer comes from the cache
		req, _ := http.NewRequest("GET", srv.URL+"/a", nil)
		req.Host = "x.trycloudflare"
		req.SetBasicAuth("client", "s3cret")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		// A share is https already and the credentials stay with the proxy.
		if r.StatusCode != 200 || string(body) != "app /a" || r.Header.Get("X-Auth-Seen") != "" || r.Header.Get("X-Proto-Seen") != "https" {
			t.Fatalf("signed in: %d %q %v", r.StatusCode, body, r.Header)
		}
	}
}

func TestRedirectsAndHeaders(t *testing.T) {
	srv := rulesServer(t, &Rules{
		Redirects: []Redirect{
			{Host: "www.shop.test", From: "/*", To: "https://shop.test/*", Status: 301},
			{From: "/old/*", To: "/new/*"},
			{From: "/about", To: "/team", Status: 308},
		},
		Headers: []Header{{Name: "X-Frame-Options", Value: "DENY"}, {Name: "X-Powered-By"}},
	}, true)
	cases := []struct {
		host, path, loc string
		status          int
	}{
		{"www.shop.test", "/a/b?x=1", "https://shop.test/a/b?x=1", 301},
		{"x.trycloudflare", "/old/page?q=2", "/new/page?q=2", 302},
		{"x.trycloudflare", "/about", "/team", 308},
	}
	for _, c := range cases {
		resp := send(t, srv, "GET", c.host, c.path, nil)
		if resp.StatusCode != c.status || resp.Header.Get("Location") != c.loc {
			t.Errorf("%s%s: %d %q", c.host, c.path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	resp := send(t, srv, "GET", "x.trycloudflare", "/about/us", nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("X-Powered-By") != "" {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestCORS(t *testing.T) {
	srv := rulesServer(t, &Rules{CORS: &CORS{Origins: []string{"https://app.test", "https://*.shop.test"}, Credentials: true, MaxAgeSec: 600}}, true)
	resp := send(t, srv, "OPTIONS", "x.trycloudflare", "/api", map[string]string{"Origin": "https://admin.shop.test", "Access-Control-Request-Method": "PUT", "Access-Control-Request-Headers": "content-type"})
	h := resp.Header
	if resp.StatusCode != 204 || h.Get("Access-Control-Allow-Origin") != "https://admin.shop.test" || h.Get("Access-Control-Allow-Credentials") != "true" ||
		h.Get("Access-Control-Allow-Headers") != "content-type" || h.Get("Access-Control-Max-Age") != "600" || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("preflight: %d %v", resp.StatusCode, h)
	}
	resp = send(t, srv, "GET", "x.trycloudflare", "/api", map[string]string{"Origin": "https://app.test"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.test" || resp.Header.Get("Vary") != "Origin" {
		t.Fatalf("response: %v", resp.Header)
	}
	resp = send(t, srv, "GET", "x.trycloudflare", "/api", map[string]string{"Origin": "https://evil.test"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a foreign origin: %v", resp.Header)
	}
	// A preflight from elsewhere is the application's business.
	resp = send(t, srv, "OPTIONS", "x.trycloudflare", "/api", map[string]string{"Origin": "https://evil.test", "Access-Control-Request-Method": "PUT"})
	if resp.StatusCode != 200 {
		t.Fatalf("foreign preflight: %d", resp.StatusCode)
	}
}
