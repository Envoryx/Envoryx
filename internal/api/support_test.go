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

// The hoster's support manages no accounts and opens no other way in: whatever it did
// would outlast the admins' consent.
func TestSupportCantGrantAccess(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodGet, "/api/v1/users", nil, false)
	users, _ := r.body["users"].([]any)
	if r.status != http.StatusOK || len(users) == 0 {
		t.Fatalf("users: %d %s", r.status, r.raw)
	}
	adminID := users[0].(map[string]any)["id"].(string)
	if r = a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true}, true); r.status != http.StatusOK {
		t.Fatalf("allow: %d %s", r.status, r.raw)
	}
	token, err := a.auth.IssueSupportLogin(context.Background(), "hoster")
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := a.supportLogin(token)
	if cookie == nil {
		t.Fatal("no support session")
	}
	a.cookie = cookie

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/users", map[string]any{"username": "helper", "role": "admin"}},
		{http.MethodPatch, "/api/v1/users/" + adminID, map[string]any{"role": "viewer"}},
		{http.MethodDelete, "/api/v1/users/" + adminID, nil},
		{http.MethodPost, "/api/v1/users/" + adminID + "/invite", map[string]any{}},
		{http.MethodPut, "/api/v1/users/" + adminID + "/projects/shop", map[string]any{"role": "admin"}},
		{http.MethodPut, "/api/v1/settings/oidc", map[string]any{"enabled": true}},
		{http.MethodPost, "/api/v1/settings/oidc/test", map[string]any{}},
		{http.MethodPatch, "/api/v1/settings", map[string]any{"sshAuthorizedKeys": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGv1t9rJ8o3b7h2y0aZ3kq3Yl0b1vXcXgk7oQyM5c4Zt support"}},
		{http.MethodPost, "/api/v1/instance/backups/x/restore", map[string]any{"confirm": "restore"}},
		{http.MethodPost, "/api/v1/tokens", map[string]any{"name": "keep", "scope": "admin"}},
		{http.MethodPut, "/api/v1/auth/ssh-keys", map[string]any{"keys": ""}},
		{http.MethodPost, "/api/v1/auth/password", map[string]any{"current": "x", "new": "y"}},
		{http.MethodPost, "/api/v1/settings/secret-key/reveal", nil},
		{http.MethodPost, "/api/v1/settings/secret-key/rotate", nil},
		// No code of its own choosing that stays, and the record of its visit stays too.
		{http.MethodPost, "/api/v1/addons", map[string]any{"url": "https://example.com/addon.yml"}},
		{http.MethodPut, "/api/v1/settings/registries", map[string]any{"registries": []any{}}},
		{http.MethodPut, "/api/v1/audit/settings", map[string]any{"retentionDays": 1}},
		{http.MethodDelete, "/api/v1/settings/log-history", nil},
		// Where the backups go: a target of its own would outlast the consent.
		{http.MethodPost, "/api/v1/offsite/targets", map[string]any{"name": "mine", "type": "s3", "endpoint": "https://s3.example.com", "bucket": "b"}},
		{http.MethodPut, "/api/v1/offsite/targets/x", map[string]any{"name": "mine"}},
		{http.MethodDelete, "/api/v1/offsite/targets/x", nil},
		{http.MethodPatch, "/api/v1/settings", map[string]any{"logHistory": map[string]any{"enabled": false}}},
		{http.MethodPost, "/api/v1/projects/from-manifest", map[string]any{"name": "Custom", "yaml": "version: 1\nphp:\n  version: \"8.4\"\n  image: example/php:1\n"}},
	} {
		r := a.do(c.method, c.path, c.body, true)
		if r.status != http.StatusForbidden || !strings.Contains(string(r.raw), "support") {
			t.Errorf("%s %s as support: %d %s", c.method, c.path, r.status, r.raw)
		}
	}
	// Other settings stay open to the support.
	if r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"xdebugClientHost": "192.168.1.20"}, true); r.status != http.StatusOK {
		t.Fatalf("settings as support: %d %s", r.status, r.raw)
	}
}

// The support works with projects, also through a manifest, as long as the manifest
// brings no custom image: that is refused as PUT .../services/{kind}/image is.
func TestSupportManifestWithoutCustomImages(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	if r := a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true}, true); r.status != http.StatusOK {
		t.Fatalf("allow: %d %s", r.status, r.raw)
	}
	token, err := a.auth.IssueSupportLogin(context.Background(), "hoster")
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := a.supportLogin(token)
	if cookie == nil {
		t.Fatal("no support session")
	}
	a.cookie = cookie
	r := a.do(http.MethodPost, "/api/v1/projects/from-manifest", map[string]any{"name": "Plain", "yaml": "version: 1\nphp:\n  version: \"8.4\"\n"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("manifest without an image: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/manifest/apply", map[string]any{"yaml": "version: 1\nphp:\n  version: \"8.4\"\n  image: example/php:1\n"}, true)
	if r.status != http.StatusForbidden || !strings.Contains(string(r.raw), "support") {
		t.Fatalf("manifest with an image: %d %s", r.status, r.raw)
	}
}

// The fleet manager can't fill the memory with support login links: a few may be open at
// once, and the hoster's name they keep for the audit log is short.
func TestSupportLoginLinksAreLimited(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	ctx := context.Background()
	if r := a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true}, true); r.status != http.StatusOK {
		t.Fatalf("allow: %d %s", r.status, r.raw)
	}
	var tokens []string
	for i := range 5 {
		token, err := a.auth.IssueSupportLogin(ctx, strings.Repeat("x", 1<<20))
		if err != nil {
			t.Fatalf("link %d: %v", i+1, err)
		}
		tokens = append(tokens, token)
	}
	if _, err := a.auth.IssueSupportLogin(ctx, "hoster"); !errors.Is(err, auth.ErrSupportTickets) {
		t.Fatalf("sixth link: %v", err)
	}
	// A used link makes room for the next.
	_, _, hoster, err := a.auth.RedeemSupportLogin(ctx, tokens[0], "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(hoster) != 100 {
		t.Errorf("the hoster's name kept %d characters", len(hoster))
	}
	if _, err := a.auth.IssueSupportLogin(ctx, "hoster"); err != nil {
		t.Fatalf("after one was used: %v", err)
	}
}

// The support can't set a custom image, so it can't restore a backup that brings one
// into a new project either; an admin can, and the support restores backups without one.
func TestSupportRestoresNoCustomImage(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	backup := func(name string, image string) (string, string) {
		r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": name, "php": map[string]any{"version": "8.4"}}, true)
		if r.status != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, r.status, r.raw)
		}
		id := r.body["project"].(map[string]any)["id"].(string)
		if image != "" {
			if r := a.do(http.MethodPut, "/api/v1/projects/"+id+"/services/php/image", map[string]any{"image": image}, true); r.status != http.StatusOK {
				t.Fatalf("custom image: %d %s", r.status, r.raw)
			}
		}
		r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/backups", map[string]any{"files": true}, true)
		if r.status != http.StatusCreated {
			t.Fatalf("backup %s: %d %s", name, r.status, r.raw)
		}
		return id, r.body["backup"].(map[string]any)["id"].(string)
	}
	customID, customBackup := backup("Custom", "ghcr.io/acme/php:8.4")
	plainID, plainBackup := backup("Plain", "")
	admin := a.cookie

	if r := a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true}, true); r.status != http.StatusOK {
		t.Fatalf("allow: %d %s", r.status, r.raw)
	}
	token, err := a.auth.IssueSupportLogin(context.Background(), "hoster")
	if err != nil {
		t.Fatal(err)
	}
	support, _ := a.supportLogin(token)
	if support == nil {
		t.Fatal("no support session")
	}
	a.cookie = support
	r := a.do(http.MethodPost, "/api/v1/backups/"+customID+"/"+customBackup+"/restore-new", map[string]any{"name": "Custom Again"}, true)
	if r.status != http.StatusForbidden || !strings.Contains(string(r.raw), "support") || !strings.Contains(string(r.raw), "ghcr.io/acme/php:8.4") {
		t.Fatalf("support restores a custom image: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/projects", nil, false); strings.Contains(string(r.raw), "Custom Again") {
		t.Fatal("the refused restore left a project")
	}
	if r := a.do(http.MethodPost, "/api/v1/backups/"+plainID+"/"+plainBackup+"/restore-new", map[string]any{"name": "Plain Again"}, true); r.status != http.StatusCreated {
		t.Fatalf("support restores a plain backup: %d %s", r.status, r.raw)
	}
	a.cookie = admin
	if r := a.do(http.MethodPost, "/api/v1/backups/"+customID+"/"+customBackup+"/restore-new", map[string]any{"name": "Custom Again"}, true); r.status != http.StatusCreated {
		t.Fatalf("admin restores a custom image: %d %s", r.status, r.raw)
	}
}
