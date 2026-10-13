package api_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/plan"
)

// accessFile is the access matrix: one line per route with who may call it.
const accessFile = "testdata/access.txt"

// accessColumns are the columns every line of accessFile has, with their values.
var accessColumns = map[string][]string{
	"scope":     {"public", "read", "operate", "admin", "token"},
	"tokens":    {"yes", "no", "admin", "body", "own"},
	"support":   {"yes", "no", "body"},
	"confined":  {"project", "any", "yes", "users", "no"},
	"suspended": {"yes", "no"},
	"plan":      {"offsite", "none"},
}

// sensitiveBodies are what a route with tokens=body or support=body refuses: the body
// carrying the way in.
var sensitiveBodies = map[string]string{
	"PATCH /api/v1/settings":                    `{"sshAuthorizedKeys":""}`,
	"POST /api/v1/projects/from-manifest":       `{"name":"Custom","yaml":"version: 1\nphp:\n  version: \"8.4\"\n  image: example/php:1\n"}`,
	"POST /api/v1/projects/{id}/manifest/apply": `{"yaml":"version: 1\nphp:\n  version: \"8.4\"\n  image: example/php:1\n"}`,
}

// accessLine is one route of the access matrix.
type accessLine struct {
	line         int
	method, path string
	col          map[string]string
}

// pattern is the route's pattern as the mux has it.
func (l accessLine) pattern() string {
	if l.method == "ANY" {
		return l.path
	}
	return l.method + " " + l.path
}

func readAccessTable(t *testing.T) []accessLine {
	t.Helper()
	f, err := os.Open(accessFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []accessLine
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2+len(accessColumns) {
			t.Fatalf("%s:%d: want METHOD PATH and the columns %s, got %q", accessFile, n, columnNames(), text)
		}
		l := accessLine{line: n, method: fields[0], path: fields[1], col: map[string]string{}}
		for _, f := range fields[2:] {
			k, v, _ := strings.Cut(f, "=")
			values, known := accessColumns[k]
			if !known || l.col[k] != "" {
				t.Fatalf("%s:%d: unknown or repeated column %q (the columns are %s)", accessFile, n, k, columnNames())
			}
			if !slices.Contains(values, v) {
				t.Fatalf("%s:%d: %s=%q is not one of %v", accessFile, n, k, v, values)
			}
			l.col[k] = v
		}
		if l.col["scope"] == "public" && (l.col["tokens"] != "yes" || l.col["support"] != "yes" || l.col["confined"] != "yes" || l.col["suspended"] != "yes" || l.col["plan"] != "none") {
			t.Fatalf("%s:%d: a public route checks nobody: tokens, support, confined and suspended are yes, plan is none", accessFile, n)
		}
		if (l.col["tokens"] == "body" || l.col["support"] == "body") && sensitiveBodies[l.pattern()] == "" {
			t.Fatalf("%s:%d: a route that refuses by the body needs that body in sensitiveBodies", accessFile, n)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func columnNames() string {
	names := make([]string, 0, len(accessColumns))
	for k := range accessColumns {
		names = append(names, k)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// Every route on the server's mux has exactly one line in the access matrix, and every
// line is a route: a route without a decision about who may call it fails here.
func TestAccessTableCoversEveryRoute(t *testing.T) {
	a := newApp(t)
	lines := map[string]accessLine{}
	for _, l := range readAccessTable(t) {
		if prev, dup := lines[l.pattern()]; dup {
			t.Errorf("%s:%d: %q has a line already (line %d)", accessFile, l.line, l.pattern(), prev.line)
		}
		lines[l.pattern()] = l
	}
	registered := map[string]bool{}
	for _, r := range a.routes {
		registered[r.Pattern] = true
		l, ok := lines[r.Pattern]
		if !ok {
			t.Errorf("route %q has no line in internal/api/%s: add one and decide each column (scope=%s, see the header there and DEVELOPMENT.md)", r.Pattern, accessFile, r.Kind)
			continue
		}
		if l.col["scope"] != r.Kind {
			t.Errorf("%s:%d: %q says scope=%s, but the route is mounted as %s", accessFile, l.line, r.Pattern, l.col["scope"], r.Kind)
		}
	}
	for p, l := range lines {
		if !registered[p] {
			t.Errorf("%s:%d: %q is no registered route: remove the line or fix its method and path", accessFile, l.line, p)
		}
	}
}

// caller is one kind of principal the access matrix is checked for.
type caller struct {
	name   string
	cookie *http.Cookie
	bearer string
	// level is what the caller may do on the instance; inA in project A, the only
	// project of a confined caller.
	level, inA auth.Scope
	confined   bool
	support    bool
	suspended  bool
	noOffsite  bool
}

func (c caller) anonymous() bool { return c.cookie == nil && c.bearer == "" }
func (c caller) token() bool     { return c.bearer != "" }

// allows is what the line says about the caller, in project A or else B.
func (l accessLine) allows(c caller, inA bool) bool {
	scope := l.col["scope"]
	switch {
	case scope == "public":
		return true
	case c.anonymous(), scope == "token" && !c.token():
		return false
	}
	need := auth.Scope(scope)
	if scope == "token" {
		need = auth.ScopeRead
	}
	if c.confined {
		switch l.col["confined"] {
		case "project":
			if !inA || !c.inA.Covers(need) {
				return false
			}
		case "any":
			if !c.inA.Covers(need) {
				return false
			}
		case "users":
			if c.token() {
				return false
			}
		case "no":
			return false
		}
	} else if !c.level.Covers(need) {
		return false
	}
	if c.token() {
		switch l.col["tokens"] {
		case "no", "body", "own":
			return false
		case "admin":
			if c.confined || !c.level.Covers(auth.ScopeAdmin) {
				return false
			}
		}
	}
	if c.support && l.col["support"] != "yes" {
		return false
	}
	if c.suspended && l.col["suspended"] != "yes" {
		return false
	}
	return !c.noOffsite || l.col["plan"] != "offsite"
}

// accessPath fills the path's wildcards: the project with the given id, a made-up name
// for everything else.
func accessPath(path, project string) string {
	path = strings.ReplaceAll(path, "{id}", project)
	path = strings.ReplaceAll(path, "{project}", project)
	for strings.Contains(path, "{") {
		i := strings.Index(path, "{")
		j := strings.Index(path[i:], "}")
		path = path[:i] + "x" + path[i+j+1:]
	}
	return path
}

// TestAccessMatrix sends every route as every kind of caller and holds the answer to the
// route's line in the access matrix. What the line refuses must be refused (401 without
// a sign-in, 403 otherwise); such requests never reach the handler's work, so they are
// sent for every method. What it allows is checked for GET routes only, which change
// nothing: anything but 401 and 403 passed the gate. Project routes use project B, and
// both A and B for a caller confined to A.
func TestAccessMatrix(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	admin := a.cookie
	lines := readAccessTable(t)

	project := func(name string) string {
		t.Helper()
		r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": name, "php": map[string]any{"version": "8.4"}}, true)
		if r.status != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, r.status, r.raw)
		}
		return r.body["project"].(map[string]any)["id"].(string)
	}
	projA, projB := project("Alpha"), project("Beta")
	user := func(name, role string, projectRoles map[string]string) *http.Cookie {
		t.Helper()
		a.cookie = admin
		r := a.do(http.MethodPost, "/api/v1/users", map[string]any{"username": name, "role": role, "projectRoles": projectRoles}, true)
		if r.status != http.StatusCreated {
			t.Fatalf("invite %s: %d %s", name, r.status, r.raw)
		}
		url := r.body["inviteUrl"].(string)
		a.cookie = nil
		r = a.do(http.MethodPost, "/api/v1/invites/"+url[strings.LastIndex(url, "/")+1:], map[string]string{"password": name + "-secret-123"}, true)
		if r.status != http.StatusOK || r.cookie == nil {
			t.Fatalf("accept %s: %d %s", name, r.status, r.raw)
		}
		return r.cookie
	}
	viewer, developer := user("vera", "viewer", nil), user("dev", "developer", nil)
	confinedUser := user("cora", "none", map[string]string{projA: "developer"})
	a.cookie = admin
	mint := func(name, scope string, projects ...string) string {
		t.Helper()
		body := map[string]any{"name": name, "scope": scope}
		if projects != nil {
			body["projects"] = projects
		}
		r := a.do(http.MethodPost, "/api/v1/tokens", body, true)
		if r.status != http.StatusCreated {
			t.Fatalf("mint %s: %d %s", name, r.status, r.raw)
		}
		return r.body["secret"].(string)
	}
	if r := a.do(http.MethodPut, "/api/v1/settings/support-access", map[string]any{"allowed": true}, true); r.status != http.StatusOK {
		t.Fatalf("allow support: %d %s", r.status, r.raw)
	}
	ticket, err := a.auth.IssueSupportLogin(context.Background(), "hoster")
	if err != nil {
		t.Fatal(err)
	}
	support, _ := a.supportLogin(ticket)
	if support == nil {
		t.Fatal("no support session")
	}
	adminToken := mint("admin", "admin")
	// A tokens=own route is sent what a token may not touch: an uploaded backup.
	_, uploaded := a.ownAndUploadedInstanceBackups()

	callers := []caller{
		{name: "anonymous"},
		{name: "viewer", cookie: viewer, level: auth.ScopeRead},
		{name: "developer", cookie: developer, level: auth.ScopeOperate},
		{name: "admin", cookie: admin, level: auth.ScopeAdmin},
		{name: "confined user", cookie: confinedUser, inA: auth.ScopeOperate, confined: true},
		{name: "read token", bearer: mint("read", "read"), level: auth.ScopeRead},
		{name: "operate token", bearer: mint("operate", "operate"), level: auth.ScopeOperate},
		{name: "admin token", bearer: adminToken, level: auth.ScopeAdmin},
		{name: "confined admin token", bearer: mint("confined", "admin", projA), inA: auth.ScopeAdmin, confined: true},
		{name: "support", cookie: support, level: auth.ScopeAdmin, support: true},
	}
	// Redirects are answers too (the support login, the single sign-on); WebSocket
	// routes answer a plain GET at once, the timeout only guards against a hang.
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(l accessLine, c caller, project string) (int, string) {
		method := l.method
		if method == "ANY" {
			method = http.MethodGet
		}
		var body string
		if method != http.MethodGet {
			body = "{}"
			if b := sensitiveBodies[l.pattern()]; b != "" {
				body = b
			}
		}
		path := l.path
		if l.col["tokens"] == "own" {
			path = strings.ReplaceAll(path, "{id}", uploaded)
		}
		req, err := http.NewRequest(method, a.srv.URL+accessPath(path, project), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "Envoryx")
		if c.cookie != nil {
			req.AddCookie(c.cookie)
		}
		if c.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearer)
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		raw := make([]byte, 300)
		n, _ := res.Body.Read(raw)
		return res.StatusCode, string(raw[:n])
	}
	check := func(c caller) {
		for _, l := range lines {
			projects := []string{projB}
			if c.confined && l.col["confined"] == "project" {
				projects = []string{projA, projB}
			}
			for _, project := range projects {
				inA := project == projA
				name := fmt.Sprintf("%s %s as %s", l.method, l.path, c.name)
				if c.confined && l.col["confined"] == "project" {
					name += map[bool]string{true: " in A", false: " in B"}[inA]
				}
				allowed := l.allows(c, inA)
				if allowed && l.method != http.MethodGet {
					continue
				}
				t.Run(name, func(t *testing.T) {
					status, raw := send(l, c, project)
					switch {
					case allowed && (status == http.StatusUnauthorized || status == http.StatusForbidden || status == 0):
						t.Errorf("%s:%d allows this, got %d %s", accessFile, l.line, status, raw)
					case !allowed:
						want := http.StatusForbidden
						if c.anonymous() || l.col["scope"] == "token" && !c.token() {
							want = http.StatusUnauthorized
						}
						if status != want {
							t.Errorf("%s:%d refuses this with %d, got %d %s", accessFile, l.line, want, status, raw)
						}
					}
				})
			}
		}
	}
	for _, c := range callers {
		check(c)
	}

	// A suspended instance, to a session and a token of an admin.
	susp := filepath.Join(a.cfgDir, plan.SuspensionFile)
	if err := os.WriteFile(susp, []byte(`{"since":"2026-10-11T08:00:00Z","message":"Invoice 1234 is open"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.plans.Reload()
	check(caller{name: "admin while suspended", cookie: admin, level: auth.ScopeAdmin, suspended: true})
	check(caller{name: "admin token while suspended", bearer: adminToken, level: auth.ScopeAdmin, suspended: true})
	if err := os.Remove(susp); err != nil {
		t.Fatal(err)
	}
	a.plans.Reload()

	// A plan without offsite targets.
	a.setPlan(`{"name":"Test","disabled":["offsite"]}`)
	check(caller{name: "admin without offsite in the plan", cookie: admin, level: auth.ScopeAdmin, noOffsite: true})
}
