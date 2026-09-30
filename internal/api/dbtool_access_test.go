package api_test

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/project"
)

// The database browser is one container with the logins of every project; the proxy
// must let a principal open only the databases of projects it may operate.
func TestDBToolOpensOnlyTheDatabasesOfOwnProjects(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	admin := a.cookie
	create := func(name string) string {
		t.Helper()
		r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": name, "docroot": "public", "php": map[string]any{"version": "8.4", "config": map[string]any{}},
			"database": map[string]any{"type": "mariadb", "version": "11"}, "createStarter": true, "start": true}, true)
		if r.status != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, r.status, r.raw)
		}
		return r.body["project"].(map[string]any)["id"].(string)
	}
	shop, blog := create("Shop"), create("Blog")
	user := func(id string) string {
		t.Helper()
		r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/database/credentials", nil, false)
		if r.status != http.StatusOK {
			t.Fatalf("credentials: %d %s", r.status, r.raw)
		}
		return r.body["credentials"].(map[string]any)["username"].(string)
	}
	shopUser, blogUser := user(shop), user(blog)
	if r := a.do(http.MethodPut, "/api/v1/dbtool", map[string]any{"enabled": true}, true); r.status != http.StatusOK {
		t.Fatalf("enable: %d %s", r.status, r.raw)
	}
	if r := a.do(http.MethodPost, "/api/v1/projects/"+blog+"/dbtool", nil, true); r.status != http.StatusOK {
		t.Fatalf("open Blog as admin: %d %s", r.status, r.raw)
	}

	// Dana is a developer in Shop and only looks at Blog.
	r := a.do(http.MethodPost, "/api/v1/users", map[string]any{"username": "dana", "role": "viewer", "projectRoles": map[string]string{shop: "developer"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", r.status, r.raw)
	}
	invite := r.body["inviteUrl"].(string)
	a.cookie = nil
	r = a.do(http.MethodPost, "/api/v1/invites/"+invite[strings.LastIndex(invite, "/")+1:], map[string]string{"password": "dana-secret-123"}, true)
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("accept: %d %s", r.status, r.raw)
	}
	dana := r.cookie

	send := func(cookie *http.Cookie, bearer, method, query, form string) int {
		t.Helper()
		var body *strings.Reader
		if form != "" {
			body = strings.NewReader(form)
		} else {
			body = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, a.srv.URL+"/dbtool/?"+query, body)
		if form != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	target := func(driver, server, username string) string {
		return url.Values{driver: {server}, "username": {username}}.Encode()
	}
	login := func(driver, server, username string) string {
		return url.Values{"auth[driver]": {driver}, "auth[server]": {server}, "auth[username]": {username}, "auth[password]": {"x"}, "auth[db]": {""}}.Encode()
	}
	// No browser container answers in tests: a request that passes the check fails on the
	// way to it (502/503), one that does not is refused with 403.
	passes := func(code int) bool { return code != http.StatusForbidden && code != http.StatusUnauthorized }

	for _, c := range []struct {
		name         string
		method       string
		query, form  string
		wantAccepted bool
	}{
		{"own project's user", http.MethodGet, target("server", "envoryx-shop-database", shopUser), "", true},
		{"own project's administrator", http.MethodGet, target("server", "envoryx-shop-database", "root"), "", true},
		{"own server's login page", http.MethodGet, "server=envoryx-shop-database", "", true},
		{"the start page", http.MethodGet, "", "", true},
		{"static files", http.MethodGet, "file=default.css&version=5.5.1", "", true},
		{"login post for the own project", http.MethodPost, "", login("server", "envoryx-shop-database", shopUser), true},
		{"other project's user", http.MethodGet, target("server", "envoryx-blog-database", blogUser), "", false},
		{"other project's administrator", http.MethodGet, target("server", "envoryx-blog-database", "root"), "", false},
		{"other project's login page", http.MethodGet, "server=envoryx-blog-database", "", false},
		{"other project's user on the own server", http.MethodGet, target("server", "envoryx-shop-database", blogUser), "", false},
		{"unknown server", http.MethodGet, target("server", "db.example.com:3306", "root"), "", false},
		{"no server", http.MethodGet, "username=root", "", false},
		{"another driver key", http.MethodGet, target("pgsql", "envoryx-shop-database", "root"), "", false},
		{"two driver keys", http.MethodGet, target("server", "envoryx-shop-database", "root") + "&pgsql=envoryx-blog-database", "", false},
		{"repeated server", http.MethodGet, target("server", "envoryx-shop-database", "root") + "&server=envoryx-blog-database", "", false},
		{"array server", http.MethodGet, "server%5B%5D=envoryx-shop-database&username=root", "", false},
		{"login post for another project", http.MethodPost, "", login("server", "envoryx-blog-database", "root"), false},
		{"login post for another project on an own page", http.MethodPost, target("server", "envoryx-shop-database", shopUser), login("server", "envoryx-blog-database", blogUser), false},
	} {
		code := send(dana, "", c.method, c.query, c.form)
		if passes(code) != c.wantAccepted {
			t.Errorf("%s: %d, want accepted=%v", c.name, code, c.wantAccepted)
		}
	}

	// A token follows the same rule, via its owner's roles.
	a.cookie = dana
	r = a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ci", "scope": "operate"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("token: %d %s", r.status, r.raw)
	}
	secret := r.body["secret"].(string)
	if code := send(nil, secret, http.MethodGet, target("server", "envoryx-shop-database", shopUser), ""); !passes(code) {
		t.Errorf("token for Shop: %d", code)
	}
	if code := send(nil, secret, http.MethodGet, target("server", "envoryx-blog-database", blogUser), ""); passes(code) {
		t.Errorf("token for Blog: %d", code)
	}

	// The admin operates every project.
	if code := send(admin, "", http.MethodGet, target("server", "envoryx-blog-database", "root"), ""); !passes(code) {
		t.Errorf("admin opens Blog: %d", code)
	}

	// What reaches the container: the proxy's secret and the checked login, never what the
	// client sent in those headers.
	tool, ok := a.engine.Container(project.DBToolContainer)
	if !ok || len(tool.Spec.Ports) != 1 {
		t.Fatalf("tool container: %+v", tool)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(tool.Spec.Ports[0].HostPort))
	if err != nil {
		t.Skipf("cannot listen on the tool's port: %v", err)
	}
	got := make(chan http.Header, 4)
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
	})}
	go func() { _ = upstream.Serve(ln) }()
	t.Cleanup(func() { _ = upstream.Close() })

	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+"/dbtool/?"+target("server", "envoryx-shop-database", shopUser), nil)
	req.AddCookie(dana)
	req.Header.Set(project.DBToolTokenHeader, "forged")
	req.Header.Set(project.DBToolTargetHeader, "server|envoryx-blog-database|root")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("proxied request: %d", res.StatusCode)
	}
	h := <-got
	if h.Get(project.DBToolTargetHeader) != "server|envoryx-shop-database|"+shopUser {
		t.Errorf("target header: %q", h.Get(project.DBToolTargetHeader))
	}
	if tok := h.Get(project.DBToolTokenHeader); tok == "" || tok == "forged" {
		t.Errorf("token header: %q", tok)
	}

	req, _ = http.NewRequest(http.MethodGet, a.srv.URL+"/dbtool/?file=default.css", nil)
	req.AddCookie(dana)
	req.Header.Set(project.DBToolTargetHeader, "server|envoryx-blog-database|root")
	if res, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if h := <-got; h.Get(project.DBToolTargetHeader) != "" {
		t.Errorf("a request without target must carry none: %q", h.Get(project.DBToolTargetHeader))
	}
}
