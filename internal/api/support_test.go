package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/auth"
)

// supportLogin opens a support login link like the hoster's browser and returns the
// session cookie, or nil with the redirect target.
func (a *testApp) supportLogin(token string) (*http.Cookie, string) {
	a.t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(a.srv.URL + "/api/v1/auth/support/" + token)
	if err != nil {
		a.t.Fatal(err)
	}
	res.Body.Close()
	for _, ck := range res.Cookies() {
		if ck.Name == auth.CookieName && ck.Value != "" {
			return ck, res.Header.Get("Location")
		}
	}
	return nil, res.Header.Get("Location")
}

func TestSupportLoginNeedsTheAdminsConsent(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	ctx := context.Background()

	if _, err := a.auth.IssueSupportLogin(ctx, "hoster"); !errors.Is(err, auth.ErrSupportNotAllowed) {
		t.Fatalf("without consent: %v", err)
	}
	r := a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true}, true)
	if r.status != http.StatusOK || r.body["access"].(map[string]any)["allowed"] != true {
		t.Fatalf("allow: %d %s", r.status, r.raw)
	}
	token, err := a.auth.IssueSupportLogin(ctx, "hoster")
	if err != nil {
		t.Fatal(err)
	}
	cookie, to := a.supportLogin(token)
	if cookie == nil || to != "/" {
		t.Fatalf("support login: %v %q", cookie, to)
	}
	// The link works once.
	if again, to := a.supportLogin(token); again != nil || !strings.HasPrefix(to, "/login?sso_error=") {
		t.Fatalf("second use: %v %q", again, to)
	}

	admin := a.cookie
	a.cookie = cookie
	r = a.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	if r.status != http.StatusOK || r.body["support"] != true || r.body["admin"] != true {
		t.Fatalf("me as support: %d %s", r.status, r.raw)
	}
	if r = a.do(http.MethodGet, "/api/v1/projects", nil, false); r.status != http.StatusOK {
		t.Fatalf("projects as support: %d %s", r.status, r.raw)
	}
	// Nothing that outlasts the consent: no tokens, no own consent.
	if r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "keep", "scope": "admin"}, true); r.status != http.StatusForbidden {
		t.Fatalf("token as support: %d %s", r.status, r.raw)
	}
	if r = a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true}, true); r.status != http.StatusForbidden {
		t.Fatalf("consent as support: %d %s", r.status, r.raw)
	}

	// The support user is no user of the instance.
	a.cookie = admin
	r = a.do(http.MethodGet, "/api/v1/users", nil, false)
	if strings.Contains(string(r.raw), "Hoster support") {
		t.Fatalf("support user listed: %s", r.raw)
	}
	// Taking the consent back ends the support's session.
	if r = a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": false}, true); r.status != http.StatusOK {
		t.Fatalf("revoke: %d %s", r.status, r.raw)
	}
	a.cookie = cookie
	if r = a.do(http.MethodGet, "/api/v1/auth/me", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("support after revoking: %d %s", r.status, r.raw)
	}
	// A consent with an end in the past is refused.
	a.cookie = admin
	if r = a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true, "until": "2020-01-01T00:00:00Z"}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("past end: %d %s", r.status, r.raw)
	}
}
