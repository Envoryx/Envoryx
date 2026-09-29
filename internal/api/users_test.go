package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestUsersRolesAndInvitations(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	admin := a.cookie
	create := func(name string) string {
		t.Helper()
		r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": name, "php": map[string]any{"version": "8.4"}}, true)
		if r.status != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, r.status, r.raw)
		}
		return r.body["project"].(map[string]any)["id"].(string)
	}
	shop, blog := create("Shop"), create("Blog")

	// The admin invites a viewer who is a developer in Shop.
	r := a.do(http.MethodPost, "/api/v1/users", map[string]any{"username": "dana", "role": "viewer", "projectRoles": map[string]string{shop: "developer"}}, true)
	if r.status != http.StatusCreated || !strings.Contains(r.body["inviteUrl"].(string), "/invite/") {
		t.Fatalf("invite: %d %s", r.status, r.raw)
	}
	url := r.body["inviteUrl"].(string)
	token := url[strings.LastIndex(url, "/")+1:]
	danaID := r.body["user"].(map[string]any)["id"].(string)

	// The invitation page is public; the link sets the password once and signs in.
	a.cookie = nil
	if r := a.do(http.MethodGet, "/api/v1/invites/"+token, nil, false); r.status != http.StatusOK || r.body["username"] != "dana" {
		t.Fatalf("invitation: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodPost, "/api/v1/invites/"+token, map[string]string{"password": "short"}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("weak password: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/invites/"+token, map[string]string{"password": "dana-secret-123"}, true)
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("accept: %d %s", r.status, r.raw)
	}
	dana := r.cookie
	if r := a.do(http.MethodPost, "/api/v1/invites/"+token, map[string]string{"password": "dana-secret-456"}, true); r.status != http.StatusGone {
		t.Fatalf("a used invitation: %d %s", r.status, r.raw)
	}

	// Dana sees both projects, works in Shop, only looks at Blog, and has no admin rights.
	a.cookie = dana
	r = a.do(http.MethodGet, "/api/v1/projects", nil, false)
	if n := len(r.body["projects"].([]any)); r.status != http.StatusOK || n != 2 {
		t.Fatalf("viewer list: %d %d", r.status, n)
	}
	for _, p := range r.body["projects"].([]any) {
		pm := p.(map[string]any)
		want := map[string]string{shop: "operate", blog: "read"}[pm["id"].(string)]
		if pm["access"] != want {
			t.Fatalf("access of %s: %v, want %s", pm["name"], pm["access"], want)
		}
	}
	if r := a.do(http.MethodPost, "/api/v1/projects/"+shop+"/start", nil, true); r.status != http.StatusOK {
		t.Fatalf("developer starts Shop: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodPost, "/api/v1/projects/"+blog+"/start", nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer starts Blog: %d %s", r.status, r.raw)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/projects"}, {http.MethodGet, "/api/v1/users"}, {http.MethodGet, "/api/v1/docker"},
		{http.MethodDelete, "/api/v1/projects/" + shop}, {http.MethodPost, "/api/v1/projects/" + shop + "/duplicate"},
	} {
		if r := a.do(c.method, c.path, map[string]any{"name": "X", "confirm": "shop"}, true); r.status != http.StatusForbidden {
			t.Fatalf("%s %s as developer/viewer: %d %s", c.method, c.path, r.status, r.raw)
		}
	}
	r = a.do(http.MethodGet, "/api/v1/settings", nil, false)
	if r.status != http.StatusOK || r.body["baseDomain"] == nil || r.body["configDir"] != nil {
		t.Fatalf("trimmed settings: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	if r.body["admin"] != false || r.body["projectRoles"].(map[string]any)[shop] != "developer" {
		t.Fatalf("me: %s", r.raw)
	}

	// Dana's tokens never exceed Dana: no admin token, and an operate token reads Blog only.
	if r := a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ci", "scope": "admin"}, true); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("admin token for a developer: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ci", "scope": "operate"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("operate token: %d %s", r.status, r.raw)
	}
	secret := r.body["secret"].(string)
	bearer := func(path string) int {
		req, _ := http.NewRequest(http.MethodPost, a.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := bearer("/api/v1/projects/" + blog + "/stop"); code != http.StatusForbidden {
		t.Fatalf("Dana's token stops Blog: %d", code)
	}
	if code := bearer("/api/v1/projects/" + shop + "/stop"); code != http.StatusOK {
		t.Fatalf("Dana's token stops Shop: %d", code)
	}

	// The admin lists users, sees Dana's token, and cannot lose the last admin.
	a.cookie = admin
	r = a.do(http.MethodGet, "/api/v1/users", nil, false)
	if r.status != http.StatusOK || len(r.body["users"].([]any)) != 2 {
		t.Fatalf("users: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/tokens", nil, false)
	if toks := r.body["tokens"].([]any); len(toks) != 1 || toks[0].(map[string]any)["owner"] != "dana" {
		t.Fatalf("admin sees the tokens with owners: %s", r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	adminID := r.body["user"].(map[string]any)["id"].(string)
	if r := a.do(http.MethodPatch, "/api/v1/users/"+adminID, map[string]any{"role": "viewer"}, true); r.status != http.StatusConflict {
		t.Fatalf("demoting the last admin: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodDelete, "/api/v1/users/"+adminID, nil, true); r.status != http.StatusConflict {
		t.Fatalf("deleting yourself: %d %s", r.status, r.raw)
	}

	// A user limited to Blog only sees Blog and nothing instance-wide.
	if r := a.do(http.MethodPut, "/api/v1/users/"+danaID+"/projects/"+shop, map[string]any{"role": ""}, true); r.status != http.StatusOK {
		t.Fatalf("remove project role: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodPatch, "/api/v1/users/"+danaID, map[string]any{"role": "none"}, true); r.status != http.StatusOK {
		t.Fatalf("role none: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodPut, "/api/v1/users/"+danaID+"/projects/"+blog, map[string]any{"role": "viewer"}, true); r.status != http.StatusOK {
		t.Fatalf("project role: %d %s", r.status, r.raw)
	}
	a.cookie = dana
	r = a.do(http.MethodGet, "/api/v1/projects", nil, false)
	if ps := r.body["projects"].([]any); len(ps) != 1 || ps[0].(map[string]any)["id"] != blog {
		t.Fatalf("confined list: %s", r.raw)
	}
	if r := a.do(http.MethodGet, "/api/v1/projects/"+shop, nil, false); r.status != http.StatusForbidden {
		t.Fatalf("a project without access: %d", r.status)
	}
	if r := a.do(http.MethodGet, "/api/v1/runtimes", nil, false); r.status != http.StatusOK {
		t.Fatalf("runtimes for a confined user: %d", r.status)
	}
	// The resource comparison lists only Blog as well, not the other projects' names.
	r = a.do(http.MethodGet, "/api/v1/metrics/overview?range=24h", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("metrics overview: %d %s", r.status, r.raw)
	}
	for _, u := range r.body["overview"].(map[string]any)["projects"].([]any) {
		if u.(map[string]any)["id"] != blog {
			t.Fatalf("metrics overview shows another project: %s", r.raw)
		}
	}

	// Disabling signs Dana out and stops the token.
	a.cookie = admin
	if r := a.do(http.MethodPatch, "/api/v1/users/"+danaID, map[string]any{"disabled": true}, true); r.status != http.StatusOK {
		t.Fatalf("disable: %d %s", r.status, r.raw)
	}
	a.cookie = dana
	if r := a.do(http.MethodGet, "/api/v1/projects", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("a disabled user's session: %d", r.status)
	}
	if code := bearer("/api/v1/projects/" + blog + "/stop"); code != http.StatusUnauthorized {
		t.Fatalf("a disabled user's token: %d", code)
	}
	a.cookie = nil
	if r := a.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "dana", "password": "dana-secret-123"}, true); r.status != http.StatusUnauthorized {
		t.Fatalf("a disabled user's login: %d %s", r.status, r.raw)
	}
}
