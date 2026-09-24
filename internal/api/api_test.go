package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/envoryx/envoryx/internal/acme"
	"github.com/envoryx/envoryx/internal/api"
	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/config"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/docker/dockertest"
	"github.com/envoryx/envoryx/internal/hostpath"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/mcpserver"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/s3"
	"github.com/envoryx/envoryx/internal/server"
	"github.com/envoryx/envoryx/internal/stats"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/tlsca"
	"github.com/envoryx/envoryx/internal/update"
)

type dockerExecResult = docker.ExecResult
type dockerSpec = docker.ContainerSpec

type testApp struct {
	t        *testing.T
	srv      *httptest.Server
	engine   *dockertest.Fake
	proxy    *api.ProxyInfo
	cookie   *http.Cookie
	projDir  string
	cfgDir   string
	restarts int
}

func newApp(t *testing.T) *testApp {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sqlDB, err := db.Open(context.Background(), ":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	engine := dockertest.New()
	cfgDir, projDir := t.TempDir(), t.TempDir()
	cfg := config.Config{ConfigDir: cfgDir, ProjectsDir: projDir, PortRangeStart: 20000, PortRangeEnd: 20010, PUID: 1000, PGID: 1000,
		SessionIdleTimeout: time.Hour, SessionAbsoluteTimeout: 24 * time.Hour}
	sessions := auth.NewService(st, auth.Options{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour}, log)
	auditLog := audit.New(st.Audit, log)
	resolver := hostpath.New(engine, map[string]string{cfgDir: "/host/config", projDir: "/host/projects"})
	paths := func() (project.Paths, error) {
		return project.Paths{ConfigDir: cfgDir, ConfigHostDir: "/host/config", ProjectsDir: projDir, ProjectsHostDir: "/host/projects", PUID: 1000, PGID: 1000}, nil
	}
	manager := project.NewManager(st, engine, runtime.Default(), paths, auditLog, project.Config{PortRangeStart: 20000, PortRangeEnd: 20010}, log)
	manager.SetProvisioner(s3.Noop{})
	certs, err := tlsca.Open(filepath.Join(cfgDir, "ca"))
	if err != nil {
		t.Fatal(err)
	}
	acmeMgr, err := acme.New(filepath.Join(cfgDir, "ca"), certs, log)
	if err != nil {
		t.Fatal(err)
	}
	notifier, err := notify.New(cfgDir, log)
	if err != nil {
		t.Fatal(err)
	}
	invalidations := 0
	proxyInfo := &api.ProxyInfo{Enabled: true, HTTPPort: 80, HTTPSPort: 443, InDocker: true, Invalidate: func() { invalidations++ }}
	mcpSrv := mcpserver.New(mcpserver.Deps{Projects: manager, Catalog: runtime.Default(), Auth: sessions, Version: "test", Log: log})
	app := &testApp{t: t, engine: engine, proxy: proxyInfo, projDir: projDir, cfgDir: cfgDir}
	backups := &instance.Store{ConfigDir: cfgDir, DBPath: filepath.Join(cfgDir, "envoryx.db"), Dir: filepath.Join(t.TempDir(), "_instance"), Version: "test", LatestSchema: db.LatestVersion(), Log: log}
	a := api.New(api.Deps{Config: cfg, Version: "test", Store: st, Auth: sessions, Audit: auditLog, Engine: engine, Projects: manager, Updates: update.Disabled("test"),
		Catalog: runtime.Default(), Stats: stats.New(engine, time.Second, log), HostPath: resolver, Certs: certs, ACME: acmeMgr, Notify: notifier, Proxy: proxyInfo, MCP: mcpSrv.Handler(), Log: log, StartedAt: time.Now(),
		Instance: backups, DB: sqlDB, Restart: func() { app.restarts++ }})
	s := server.New(server.Options{Addr: ":0", Log: log, MCP: mcpSrv.Handler()}, a, sessions, nil)
	handler := serverHandler(s)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	app.srv = srv
	return app
}

// serverHandler extracts the http.Handler from the server for httptest.
func serverHandler(s *server.Server) http.Handler { return s.Handler() }

type resp struct {
	status int
	body   map[string]any
	raw    []byte
	cookie *http.Cookie
}

func (a *testApp) do(method, path string, body any, withCSRF bool) resp {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.srv.URL+path, rdr)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withCSRF {
		req.Header.Set("X-Requested-With", "Envoryx")
	}
	if a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.body)
	}
	for _, c := range res.Cookies() {
		if c.Name == auth.CookieName {
			out.cookie = c
		}
	}
	return out
}

func (a *testApp) setupAndLogin() {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "admin", "password": "supersecret123"}, true)
	if r.status != http.StatusCreated || r.cookie == nil {
		a.t.Fatalf("setup failed: %d %s", r.status, r.raw)
	}
	a.cookie = r.cookie
}

func errCode(r resp) string {
	if e, ok := r.body["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

func TestHealthIsPublic(t *testing.T) {
	a := newApp(t)
	r := a.do(http.MethodGet, "/api/v1/health", nil, false)
	if r.status != http.StatusOK || r.body["status"] != "ok" || r.body["docker"] != true {
		t.Fatalf("health: %d %s", r.status, r.raw)
	}
}

func TestUnauthorizedRequests(t *testing.T) {
	a := newApp(t)
	for _, p := range []string{"/api/v1/projects", "/api/v1/dashboard", "/api/v1/docker", "/api/v1/runtimes", "/api/v1/auth/me"} {
		r := a.do(http.MethodGet, p, nil, false)
		if r.status != http.StatusUnauthorized || errCode(r) != "unauthenticated" {
			t.Errorf("%s: expected 401 unauthenticated, got %d %s", p, r.status, r.raw)
		}
	}
	r := a.do(http.MethodPost, "/api/v1/projects/00000000-0000-4000-8000-000000000000/start", nil, true)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", r.status)
	}
	// A forged cookie is rejected.
	a.cookie = &http.Cookie{Name: auth.CookieName, Value: "forged-token-value"}
	r = a.do(http.MethodGet, "/api/v1/projects", nil, false)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("forged cookie accepted: %d", r.status)
	}
}

func TestSetupAndLoginFlow(t *testing.T) {
	a := newApp(t)
	r := a.do(http.MethodGet, "/api/v1/setup", nil, false)
	if r.body["needsSetup"] != true {
		t.Fatalf("expected needsSetup, got %s", r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "admin", "password": "short"}, true)
	if r.status != http.StatusUnprocessableEntity || errCode(r) != "validation_failed" {
		t.Fatalf("weak password must be rejected: %d %s", r.status, r.raw)
	}
	a.setupAndLogin()
	r = a.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "second", "password": "supersecret123"}, true)
	if r.status != http.StatusConflict {
		t.Fatalf("second setup must conflict: %d", r.status)
	}
	r = a.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	if r.status != http.StatusOK || r.body["user"].(map[string]any)["username"] != "admin" {
		t.Fatalf("me: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/auth/logout", nil, true)
	if r.status != http.StatusNoContent || r.cookie == nil || r.cookie.MaxAge >= 0 {
		t.Fatalf("logout should clear cookie: %d %+v", r.status, r.cookie)
	}
	r = a.do(http.MethodGet, "/api/v1/auth/me", nil, false)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("session must be invalid after logout: %d", r.status)
	}
	a.cookie = nil
	r = a.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong-password"}, true)
	if r.status != http.StatusUnauthorized || errCode(r) != "invalid_credentials" {
		t.Fatalf("wrong password: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "supersecret123"}, true)
	if r.status != http.StatusOK || r.cookie == nil {
		t.Fatalf("login: %d %s", r.status, r.raw)
	}
	a.cookie = r.cookie
	r = a.do(http.MethodGet, "/api/v1/audit", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("audit: %d", r.status)
	}
	entries := r.body["entries"].([]any)
	actions := map[string]bool{}
	for _, e := range entries {
		actions[e.(map[string]any)["action"].(string)] = true
	}
	for _, want := range []string{"auth.setup", "auth.login", "auth.login_failed", "auth.logout"} {
		if !actions[want] {
			t.Errorf("audit action %s missing in %v", want, actions)
		}
	}
}

func TestCSRFProtection(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	body := map[string]any{"name": "Csrf", "php": map[string]any{"version": "8.4"}}
	r := a.do(http.MethodPost, "/api/v1/projects/preview", body, false)
	if r.status != http.StatusForbidden {
		t.Fatalf("missing X-Requested-With must be rejected: %d %s", r.status, r.raw)
	}
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/projects/preview", bytes.NewReader([]byte(`{"name":"Csrf"}`)))
	req.Header.Set("X-Requested-With", "Envoryx")
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(a.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin request must be rejected: %d", res.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/projects/preview", bytes.NewReader([]byte(`{"name":"Csrf"}`)))
	req.Header.Set("X-Requested-With", "Envoryx")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(a.cookie)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site fetch must be rejected: %d", res.StatusCode)
	}
}

func TestProjectLifecycleOverHTTP(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	a.engine.AddForeignContainer("plex", "plexinc/pms-docker", "running")

	create := map[string]any{
		"name": "Acme Shop", "docroot": "public", "createStarter": true, "start": true,
		"php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()},
		"env": []map[string]any{{"key": "APP_ENV", "value": "local", "isSecret": false}},
	}
	r := a.do(http.MethodPost, "/api/v1/projects/preview", create, true)
	if r.status != http.StatusOK {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	pv := r.body["preview"].(map[string]any)
	if pv["network"] != "envoryx-acme-shop" || len(pv["containers"].([]any)) != 2 {
		t.Fatalf("preview content: %v", pv)
	}

	r = a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	p := r.body["project"].(map[string]any)
	id := p["id"].(string)
	if p["status"].(map[string]any)["state"] != "running" || p["httpPort"].(float64) != 20000 {
		t.Fatalf("created project: %v", p)
	}

	r = a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusConflict {
		t.Fatalf("duplicate must conflict: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Evil", "path": "../../etc"}, true)
	if r.status != http.StatusUnprocessableEntity || errCode(r) != "validation_failed" {
		t.Fatalf("invalid path: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Unknown", "privileged": true}, true)
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown fields must be rejected: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodGet, "/api/v1/projects", nil, false)
	if r.status != http.StatusOK || len(r.body["projects"].([]any)) != 1 {
		t.Fatalf("list: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/stop", nil, true)
	if r.status != http.StatusOK || r.body["project"].(map[string]any)["status"].(map[string]any)["state"] != "stopped" {
		t.Fatalf("stop: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/start", nil, true)
	if r.status != http.StatusOK || r.body["project"].(map[string]any)["status"].(map[string]any)["state"] != "running" {
		t.Fatalf("start: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/restart", nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("restart: %d %s", r.status, r.raw)
	}

	// Image rollback: nothing to roll back to until the tag was rebuilt and restarted.
	phpImage := "ghcr.io/envoryx/envoryx-php:8.4"
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/images", map[string]any{"image": phpImage, "use": "previous"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("rollback without history: %d %s", r.status, r.raw)
	}
	a.engine.Remote[phpImage] = phpImage + "@v2"
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/restart", nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("restart after rebuild: %d %s", r.status, r.raw)
	}
	phpStatus := func() map[string]any {
		for _, s := range r.body["project"].(map[string]any)["status"].(map[string]any)["services"].([]any) {
			if s.(map[string]any)["kind"] == "php" {
				return s.(map[string]any)
			}
		}
		t.Fatalf("no php service in %s", r.raw)
		return nil
	}
	if s := phpStatus(); s["imagePrevious"] != true || s["imagePinned"] != false || s["imageChangedAt"] == nil {
		t.Fatalf("history after rebuild: %v", s)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/images", map[string]any{"image": phpImage, "use": "previous"}, true)
	if r.status != http.StatusOK || phpStatus()["imagePinned"] != true {
		t.Fatalf("rollback: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/images", map[string]any{"image": phpImage, "use": "sideways"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad choice: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/images", map[string]any{"image": phpImage, "use": "latest"}, true)
	if r.status != http.StatusOK || phpStatus()["imagePinned"] != false {
		t.Fatalf("latest: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services", nil, false)
	if r.status != http.StatusOK || len(r.body["services"].([]any)) != 2 {
		t.Fatalf("services: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/stats", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("stats: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/dashboard", nil, false)
	if r.status != http.StatusOK || r.body["projects"].(map[string]any)["running"].(float64) != 1 {
		t.Fatalf("dashboard: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/docker", nil, false)
	if r.status != http.StatusOK || len(r.body["containers"].([]any)) != 2 || len(r.body["foreign"].([]any)) != 1 {
		t.Fatalf("docker overview: %d %s", r.status, r.raw)
	}
	foreign := r.body["foreign"].([]any)[0].(map[string]any)
	if foreign["labels"] != nil {
		t.Fatalf("foreign container labels must not be exposed: %v", foreign)
	}

	// Invalid / foreign container ids never map to a project.
	for _, bad := range []string{"not-a-uuid", "00000000-0000-4000-8000-000000000000", "f000000000001"} {
		r = a.do(http.MethodPost, "/api/v1/projects/"+bad+"/stop", nil, true)
		if r.status != http.StatusNotFound {
			t.Errorf("id %q: expected 404, got %d", bad, r.status)
		}
	}

	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"name": "Renamed API"}, true)
	if r.status != http.StatusOK || r.body["project"].(map[string]any)["name"] != "Renamed API" {
		t.Fatalf("update: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodDelete, "/api/v1/projects/"+id, map[string]any{"confirm": "nope"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("delete without confirmation: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id, map[string]any{"confirm": "acme-shop"}, true)
	if r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
	if names := a.engine.ContainerNames(); len(names) != 1 || names[0] != "plex" {
		t.Fatalf("foreign container must survive: %v", names)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id, nil, false)
	if r.status != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", r.status)
	}
}

func TestDuplicateProjectEndpoint(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"name": "Shop", "docroot": "public", "createStarter": true, "start": true,
		"php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()},
	}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	_ = os.WriteFile(filepath.Join(a.projDir, "shop", "public", "index.php"), []byte("shop"), 0o644)

	// The parts default to everything the original has.
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/duplicate", map[string]any{"name": "Shop Test"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("duplicate: %d %s", r.status, r.raw)
	}
	copied := r.body["project"].(map[string]any)
	if copied["slug"] != "shop-test" || copied["id"] == id || copied["httpPort"].(float64) == 20000 {
		t.Fatalf("copy: %v", copied)
	}
	if copied["status"].(map[string]any)["state"] != "stopped" {
		t.Fatalf("a copy is not started unless asked: %v", copied["status"])
	}
	if b, err := os.ReadFile(filepath.Join(a.projDir, "shop-test", "public", "index.php")); err != nil || string(b) != "shop" {
		t.Fatalf("files must be copied by default: %v %q", err, b)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/duplicate", map[string]any{"name": "Shop Test"}, true)
	if r.status != http.StatusConflict {
		t.Fatalf("an existing name must conflict: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+store.NewID()+"/duplicate", map[string]any{"name": "Ghost"}, true)
	if r.status != http.StatusNotFound {
		t.Fatalf("unknown source: %d %s", r.status, r.raw)
	}
}

func TestRenameProjectEndpoint(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"name": "Shop", "docroot": "public", "createStarter": true, "start": true,
		"php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()},
	}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/rename", map[string]any{"name": "Acme Blog"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("a rename without the identifier must be refused: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/rename", map[string]any{"name": "Acme Blog", "confirm": "shop"}, true)
	if r.status != http.StatusOK {
		t.Fatalf("rename: %d %s", r.status, r.raw)
	}
	p := r.body["project"].(map[string]any)
	if p["id"] != id || p["slug"] != "acme-blog" || p["path"] != "acme-blog" || p["name"] != "Acme Blog" {
		t.Fatalf("renamed project: %v", p)
	}
	if p["status"].(map[string]any)["state"] != "running" {
		t.Fatalf("a running project must run again: %v", p["status"])
	}
	renamed := r.body["renamed"].(map[string]any)
	if renamed["from"] != "shop" || renamed["to"] != "acme-blog" {
		t.Fatalf("renamed: %v", renamed)
	}
	if _, err := os.Stat(filepath.Join(a.projDir, "acme-blog", "public")); err != nil {
		t.Fatalf("the directory must have moved: %v", err)
	}
	if hosts := p["hostnames"].([]any); len(hosts) == 0 || !strings.HasPrefix(hosts[0].(string), "acme-blog.") {
		t.Fatalf("host names must follow: %v", hosts)
	}
}

func TestDockerUnavailableIsReported(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	a.engine.Unavailable = true
	r := a.do(http.MethodGet, "/api/v1/health", nil, false)
	if r.status != http.StatusOK || r.body["docker"] != false {
		t.Fatalf("health with docker down: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Offline", "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusServiceUnavailable || errCode(r) != "docker_unavailable" {
		t.Fatalf("create with docker down: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("list must still work with docker down: %d %s", r.status, r.raw)
	}
}

func TestDatabaseEndpointsRedactSecrets(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	a.engine.ExecHandler = func(_ string, cmd []string, _ []string) (dockerExecResult, error) {
		if cmd[len(cmd)-1] == "SHOW DATABASES" {
			return dockerExecResult{Stdout: "shop\nmysql\n"}, nil
		}
		return dockerExecResult{}, nil
	}
	create := map[string]any{"name": "Shop", "start": true, "php": map[string]any{"version": "8.4"}, "database": map[string]any{"type": "mariadb", "version": "11", "exposePort": true}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	// Project payload must not contain any password.
	if bytes.Contains(r.raw, []byte("assword")) {
		t.Fatalf("project response leaks credentials: %s", r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/database", nil, false)
	if r.status != http.StatusOK || bytes.Contains(r.raw, []byte("assword")) {
		t.Fatalf("database info: %d %s", r.status, r.raw)
	}
	info := r.body["database"].(map[string]any)
	if info["database"] != "shop" || info["hostPort"].(float64) != 20001 || info["state"] != "running" {
		t.Fatalf("database info content: %v", info)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/database/credentials", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("credentials: %d %s", r.status, r.raw)
	}
	creds := r.body["credentials"].(map[string]any)
	pw, _ := creds["password"].(string)
	if len(pw) != 24 || creds["url"] == "" {
		t.Fatalf("credentials content: %v", creds)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/database/databases", nil, false)
	if r.status != http.StatusOK || len(r.body["databases"].([]any)) != 1 {
		t.Fatalf("list databases: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/database/databases", map[string]any{"name": "Bad Name"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid db name: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/database/databases", map[string]any{"name": "reports"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create db: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id+"/database/databases/reports", map[string]any{"confirm": "reports"}, true)
	if r.status != http.StatusNoContent {
		t.Fatalf("drop db: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/database/rotate", nil, true)
	if r.status != http.StatusOK || bytes.Contains(r.raw, []byte("assword")) {
		t.Fatalf("rotate: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/database/expose", map[string]any{"exposed": false}, true)
	if r.status != http.StatusOK {
		t.Fatalf("unexpose: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/database", nil, false)
	if r.body["database"].(map[string]any)["hostPort"].(float64) != 0 {
		t.Fatalf("port should be unpublished: %s", r.raw)
	}
	// Audit log must mention the access without the secret.
	r = a.do(http.MethodGet, "/api/v1/audit", nil, false)
	if !bytes.Contains(r.raw, []byte("database.credentials_viewed")) || bytes.Contains(r.raw, []byte(pw)) {
		t.Fatalf("audit: %s", r.raw)
	}
}

func TestServiceLogsRESTAndWebSocket(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Logs", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	a.engine.Logs["envoryx-logs-php"] = []docker.LogLine{
		{Time: time.Now(), Stream: "stderr", Text: "NOTICE: fpm is running"},
		{Time: time.Now(), Stream: "stderr", Text: "NOTICE: ready to handle connections"},
	}

	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/php/logs?tail=1", nil, false)
	if r.status != http.StatusOK || len(r.body["lines"].([]any)) != 1 {
		t.Fatalf("tail: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/redis/logs", nil, false)
	if r.status != http.StatusNotFound {
		t.Fatalf("unknown service must be 404: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/php/logs/ws", nil, false)
	if r.status != http.StatusUpgradeRequired && r.status != http.StatusBadRequest {
		t.Fatalf("plain GET on ws endpoint: %d %s", r.status, r.raw)
	}

	wsURL := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/v1/projects/" + id + "/services/php/logs/ws?tail=5"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Without a session the upgrade must be refused.
	if _, res, err := websocket.Dial(ctx, wsURL, nil); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("unauthenticated websocket must be rejected with 401, got err=%v status=%d", err, status)
	}
	// Cross-origin upgrades must be refused even with a valid session.
	hdr := http.Header{"Cookie": {auth.CookieName + "=" + a.cookie.Value}, "Origin": {"https://evil.example"}}
	if _, res, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr}); err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin websocket must be rejected with 403, got %v", err)
	}

	hdr = http.Header{"Cookie": {auth.CookieName + "=" + a.cookie.Value}}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	var texts []string
	for len(texts) < 2 {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(msg, &m); err != nil {
			t.Fatal(err)
		}
		if m["type"] == "line" {
			texts = append(texts, m["text"].(string))
		}
	}
	if texts[0] != "NOTICE: fpm is running" || texts[1] != "NOTICE: ready to handle connections" {
		t.Fatalf("lines: %v", texts)
	}
}

func TestTerminalWebSocket(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Term", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/v1/projects/" + id + "/services/"

	if _, res, err := websocket.Dial(ctx, base+"php/terminal/ws", nil); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated terminal must be rejected with 401, got %v", err)
	}
	hdr := http.Header{"Cookie": {auth.CookieName + "=" + a.cookie.Value}}
	if _, res, err := websocket.Dial(ctx, base+"database/terminal/ws", &websocket.DialOptions{HTTPHeader: hdr}); err == nil || res == nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("terminal for a service the project lacks must be 404, got %v", err)
	}

	conn, _, err := websocket.Dial(ctx, base+"php/terminal/ws?cols=100&rows=30", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	if len(a.engine.Terminals()) != 1 {
		t.Fatalf("expected one terminal session, got %d", len(a.engine.Terminals()))
	}
	rec := a.engine.Terminals()[0]
	if rec.Container != "envoryx-term-php" || rec.Opts.User != "1000:1000" || rec.Opts.WorkingDir != "/var/www/html" || rec.Opts.Cols != 100 || rec.Opts.Rows != 30 {
		t.Fatalf("terminal options: %+v", rec)
	}
	// Keystrokes reach the container and output comes back (the fake echoes input).
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(data) != "ls\r" {
		t.Fatalf("echo: %v %v %q", err, typ, data)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":80,"rows":24}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(a.engine.LastTerminal().Resizes()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := a.engine.LastTerminal().Resizes(); len(got) != 1 || got[0] != "80x24" {
		t.Fatalf("resize: %v", got)
	}
}

func TestProjectActions(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Act", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	path := r.body["project"].(map[string]any)["path"].(string)

	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/actions", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("list: %d %s", r.status, r.raw)
	}
	avail := map[string]bool{}
	for _, x := range r.body["actions"].([]any) {
		act := x.(map[string]any)
		avail[act["id"].(string)] = act["available"].(bool)
	}
	if !avail["php:version"] || avail["composer:install"] || avail["artisan:migrate"] || avail["npm:install"] {
		t.Fatalf("availability without project files: %v", avail)
	}
	// Add composer.json → composer actions become available.
	if err := os.WriteFile(filepath.Join(a.projDir, path, "composer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/actions", nil, false)
	for _, x := range r.body["actions"].([]any) {
		act := x.(map[string]any)
		if act["id"] == "composer:install" && act["available"] != true {
			t.Fatalf("composer:install should be available: %v", act)
		}
	}

	base := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/v1/projects/" + id + "/actions/"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{"Cookie": {auth.CookieName + "=" + a.cookie.Value}}
	if _, res, err := websocket.Dial(ctx, base+"artisan:migrate/ws", &websocket.DialOptions{HTTPHeader: hdr}); err == nil || res == nil || res.StatusCode != http.StatusConflict {
		t.Fatalf("unavailable action must be 409, got %v", err)
	}
	if _, res, err := websocket.Dial(ctx, base+"rm:-rf/ws", &websocket.DialOptions{HTTPHeader: hdr}); err == nil || res == nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown action must be 404, got %v", err)
	}
	conn, _, err := websocket.Dial(ctx, base+"composer:install/ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	recs := a.engine.Terminals()
	if len(recs) != 1 || strings.Join(recs[0].Opts.Cmd, " ") != "composer install --no-interaction --prefer-dist" || recs[0].Opts.User != "1000:1000" {
		t.Fatalf("exec options: %+v", recs)
	}
	a.engine.LastTerminal().Finish("Installing dependencies\r\n", 0)
	var sawStart, sawOutput, sawExit bool
	for !sawExit {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		switch typ {
		case websocket.MessageBinary:
			sawOutput = sawOutput || strings.Contains(string(data), "Installing")
		case websocket.MessageText:
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			switch m["type"] {
			case "start":
				sawStart = true
			case "exit":
				sawExit = true
				if m["code"].(float64) != 0 {
					t.Fatalf("exit code: %v", m)
				}
			}
		}
	}
	if !sawStart || !sawOutput {
		t.Fatalf("start=%v output=%v", sawStart, sawOutput)
	}
}

func TestGitEndpointsNeverExposeToken(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	a.engine.OneShotHandler = func(spec dockerSpec) (dockerExecResult, error) {
		if spec.Cmd[3] == "clone" {
			_ = os.MkdirAll(filepath.Join(a.projDir, "repo", ".git"), 0o755)
		}
		return dockerExecResult{Stdout: "ok\n"}, nil
	}
	create := map[string]any{"name": "Repo", "start": true, "php": map[string]any{"version": "8.4"},
		"git": map[string]any{"url": "https://github.com/seramos/example.git", "branch": "main", "token": "ghp_TOPSECRET"}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	if bytes.Contains(r.raw, []byte("ghp_TOPSECRET")) {
		t.Fatalf("token leaked: %s", r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	git := r.body["project"].(map[string]any)["git"].(map[string]any)
	if git["url"] != "https://github.com/seramos/example.git" || git["hasToken"] != true {
		t.Fatalf("git dto: %v", git)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/git", nil, false)
	if r.status != http.StatusOK || bytes.Contains(r.raw, []byte("ghp_TOPSECRET")) || r.body["git"].(map[string]any)["isRepo"] != true {
		t.Fatalf("status: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/git/pull", nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("pull: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/git/checkout", map[string]any{"branch": "--evil"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("checkout injection: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/git", map[string]any{"url": "https://github.com/seramos/example.git", "branch": "dev"}, true)
	if r.status != http.StatusOK || r.body["git"].(map[string]any)["hasToken"] != true {
		t.Fatalf("token must be kept when omitted: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/settings/deploy-key", nil, false)
	if r.status != http.StatusOK || !strings.HasPrefix(r.body["publicKey"].(string), "ssh-ed25519 ") {
		t.Fatalf("deploy key: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/audit", nil, false)
	if bytes.Contains(r.raw, []byte("ghp_TOPSECRET")) {
		t.Fatal("token in audit log")
	}
}

func TestBackupEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	a.engine.StreamHandler = func(_ string, cmd []string, _ []string, _ []byte) (string, int, error) {
		if cmd[0] == "mariadb-dump" {
			return "-- dump\n", 0, nil
		}
		return "", 0, nil
	}
	create := map[string]any{"name": "Bak", "start": true, "php": map[string]any{"version": "8.4"}, "database": map[string]any{"type": "mariadb"}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/backups", map[string]any{"database": true, "files": true, "note": "test"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("backup: %d %s", r.status, r.raw)
	}
	bid := r.body["backup"].(map[string]any)["id"].(string)
	if bytes.Contains(r.raw, []byte("assword")) {
		t.Fatalf("backup response leaks credentials: %s", r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/backups", nil, false)
	if r.status != http.StatusOK || len(r.body["backups"].([]any)) != 1 {
		t.Fatalf("list: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/backups/"+bid+"/restore", map[string]any{"database": true, "confirm": "nope"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("restore without confirmation: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/backups/"+bid+"/restore", map[string]any{"database": true, "files": true, "confirm": "bak"}, true)
	if r.status != http.StatusOK {
		t.Fatalf("restore: %d %s", r.status, r.raw)
	}
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+"/api/v1/projects/"+id+"/backups/"+bid+"/download", nil)
	req.AddCookie(a.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/x-tar" || len(body) == 0 {
		t.Fatalf("download: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id+"/backups/"+bid, nil, true)
	if r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id+"/backups/"+bid, nil, true)
	if r.status != http.StatusNotFound {
		t.Fatalf("delete twice: %d", r.status)
	}
}

// Snapshots and clones over HTTP: a snapshot is the database alone, a clone takes another
// project's data and both destructive calls insist on the project's identifier.
func TestDatabaseSnapshotAndCloneEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	var imported []string
	a.engine.StreamHandler = func(container string, cmd []string, _ []string, stdin []byte) (string, int, error) {
		switch cmd[0] {
		case "mariadb-dump":
			return "-- dump of " + container + "\n", 0, nil
		case "mariadb":
			imported = append(imported, container+": "+strings.TrimSpace(string(stdin)))
		}
		return "", 0, nil
	}
	newProject := func(name string) string {
		t.Helper()
		body := map[string]any{"name": name, "start": true, "php": map[string]any{"version": "8.4"}, "database": map[string]any{"type": "mariadb"}}
		r := a.do(http.MethodPost, "/api/v1/projects", body, true)
		if r.status != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, r.status, r.raw)
		}
		return r.body["project"].(map[string]any)["id"].(string)
	}
	local, staging := newProject("Local"), newProject("Staging")

	r := a.do(http.MethodPost, "/api/v1/projects/"+local+"/database/snapshots", map[string]any{"note": "before the migration"}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("snapshot: %d %s", r.status, r.raw)
	}
	snapshot := r.body["snapshot"].(map[string]any)
	if snapshot["kind"] != "database" || bytes.Contains(r.raw, []byte("assword")) {
		t.Fatalf("snapshot: %s", r.raw)
	}
	sid := snapshot["id"].(string)

	r = a.do(http.MethodGet, "/api/v1/projects/"+local+"/database/snapshots", nil, false)
	if r.status != http.StatusOK || len(r.body["snapshots"].([]any)) != 1 {
		t.Fatalf("list snapshots: %d %s", r.status, r.raw)
	}

	r = a.do(http.MethodPost, "/api/v1/projects/"+local+"/database/snapshots/"+sid+"/restore", map[string]any{"confirm": "nope"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("restore without confirmation: %d %s", r.status, r.raw)
	}
	imported = nil
	r = a.do(http.MethodPost, "/api/v1/projects/"+local+"/database/snapshots/"+sid+"/restore", map[string]any{"confirm": "local"}, true)
	if r.status != http.StatusOK {
		t.Fatalf("restore: %d %s", r.status, r.raw)
	}
	if len(imported) != 1 || !strings.Contains(imported[0], "envoryx-local-database: -- dump of envoryx-local-database") {
		t.Fatalf("restored dump: %v", imported)
	}

	r = a.do(http.MethodPost, "/api/v1/projects/"+local+"/database/clone", map[string]any{"source": staging, "confirm": "nope"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("clone without confirmation: %d %s", r.status, r.raw)
	}
	imported = nil
	r = a.do(http.MethodPost, "/api/v1/projects/"+local+"/database/clone", map[string]any{"source": staging, "confirm": "local"}, true)
	if r.status != http.StatusOK {
		t.Fatalf("clone: %d %s", r.status, r.raw)
	}
	clone := r.body["clone"].(map[string]any)
	if clone["source"] != "staging" || clone["database"] != "local" || clone["snapshot"] == nil {
		t.Fatalf("clone: %s", r.raw)
	}
	// Staging's dump is what arrived in local's database, and nothing else was imported.
	if len(imported) != 1 || imported[0] != "envoryx-local-database: -- dump of envoryx-staging-database" {
		t.Fatalf("clone imports: %v", imported)
	}
}

func TestBackupScheduleEndpoint(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Sched", "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	if sch := r.body["project"].(map[string]any)["backupSchedule"].(map[string]any); sch["schedule"] != "" || sch["keep"].(float64) != 7 {
		t.Fatalf("default schedule: %v", sch)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/backups/schedule", map[string]any{"schedule": "weekly", "hour": 25, "keep": 4}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid hour: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/backups/schedule", map[string]any{"schedule": "weekly", "hour": 2, "weekday": 6, "keep": 4, "includeDependencies": true}, true)
	if r.status != http.StatusOK || r.body["schedule"].(map[string]any)["weekday"].(float64) != 6 {
		t.Fatalf("set: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id, nil, false)
	if sch := r.body["project"].(map[string]any)["backupSchedule"].(map[string]any); sch["schedule"] != "weekly" || sch["includeDependencies"] != true {
		t.Fatalf("stored schedule: %v", sch)
	}
}

func TestWorkerEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Work", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/workers", nil, false)
	if r.status != http.StatusOK || len(r.body["workers"].([]any)) != 0 || len(r.body["presets"].([]any)) == 0 {
		t.Fatalf("list: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/workers", map[string]any{"name": "queue", "preset": "laravel:queue", "arg": "bad arg", "enabled": true}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid arg: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects/"+id+"/workers", map[string]any{"name": "queue", "preset": "laravel:queue", "enabled": true}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("add: %d %s", r.status, r.raw)
	}
	wk := r.body["worker"].(map[string]any)
	wid := wk["id"].(string)
	if cmd := wk["command"].([]any); cmd[2] != "queue:work" {
		t.Fatalf("command: %v", cmd)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id, nil, false)
	var sawWorker bool
	for _, s := range r.body["project"].(map[string]any)["status"].(map[string]any)["services"].([]any) {
		if s.(map[string]any)["kind"] == "worker" && s.(map[string]any)["running"] == true {
			sawWorker = true
		}
	}
	if !sawWorker {
		t.Fatalf("status must list the worker: %s", r.raw)
	}
	a.engine.Logs["envoryx-work-worker-queue"] = []docker.LogLine{{Time: time.Now(), Stream: "stdout", Text: "Processing job"}}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/worker:"+wid+"/logs?tail=10", nil, false)
	if r.status != http.StatusOK || !strings.Contains(string(r.raw), "Processing job") {
		t.Fatalf("worker logs: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/workers/"+wid, map[string]any{"name": "queue", "preset": "laravel:queue", "arg": "high", "enabled": false}, true)
	if r.status != http.StatusOK || r.body["worker"].(map[string]any)["enabled"] != false {
		t.Fatalf("update: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id+"/workers/"+wid, nil, true)
	if r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
}

func TestPublicHostSetting(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodGet, "/api/v1/settings", nil, false)
	if r.status != http.StatusOK || r.body["publicHost"] != "" {
		t.Fatalf("default public host must be empty: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"publicHost": "http://nas:8787"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("scheme/port must be rejected: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"publicHost": "192.168.1.10"}, true)
	if r.status != http.StatusOK || r.body["publicHost"] != "192.168.1.10" {
		t.Fatalf("set public host: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/dashboard", nil, false)
	if r.body["publicHost"] != "192.168.1.10" {
		t.Fatalf("dashboard must expose public host: %s", r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"publicHost": ""}, true)
	if r.status != http.StatusOK || r.body["publicHost"] != "" {
		t.Fatalf("clearing must work: %d %s", r.status, r.raw)
	}
}

func TestRuntimesEndpoint(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodGet, "/api/v1/runtimes", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("runtimes: %d", r.status)
	}
	runtimes := r.body["runtimes"].([]any)
	if len(runtimes) < 2 || runtimes[0].(map[string]any)["key"] != "php" {
		t.Fatalf("runtimes content: %v", runtimes)
	}
	presets := r.body["nodePresets"].([]any)
	if len(presets) != 4 || presets[0].(map[string]any)["key"] != "vite" || presets[2].(map[string]any)["key"] != "nuxt" || presets[2].(map[string]any)["port"].(float64) != 3000 {
		t.Fatalf("nodePresets: %v", presets)
	}
	pyPresets := r.body["pythonPresets"].([]any)
	if len(pyPresets) != 5 || pyPresets[0].(map[string]any)["key"] != "django" || pyPresets[2].(map[string]any)["key"] != "asgi" || pyPresets[1].(map[string]any)["port"].(float64) != 5000 {
		t.Fatalf("pythonPresets: %v", pyPresets)
	}
	for _, x := range r.body["templates"].([]any) {
		tpl := x.(map[string]any)
		if rt := tpl["runtime"]; rt != "php" && rt != "node" && rt != "python" {
			t.Fatalf("template %v must name its runtime", tpl["id"])
		}
		if tpl["runtime"] == "node" && tpl["node"] == nil {
			t.Fatalf("node template %v must carry dev-server defaults", tpl["id"])
		}
		if tpl["runtime"] == "python" && tpl["python"] == nil {
			t.Fatalf("python template %v must carry server defaults", tpl["id"])
		}
	}
}

func TestProjectWithoutPHPOverHTTP(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{
		"name": "Shop", "createStarter": true, "start": true,
		"node": map[string]any{"version": "24", "devServer": true, "preset": "vite"},
	}
	r := a.do(http.MethodPost, "/api/v1/projects/preview", create, true)
	if r.status != http.StatusOK {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	pv := r.body["preview"].(map[string]any)
	kinds := []string{}
	for _, c := range pv["containers"].([]any) {
		kinds = append(kinds, c.(map[string]any)["service"].(string))
	}
	if strings.Join(kinds, ",") != "node,web" || pv["serves"] != "node" || pv["appService"] != "node" || pv["devHostname"] != "shop-dev.test" {
		t.Fatalf("preview: %v", pv)
	}

	r = a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	p := r.body["project"].(map[string]any)
	id := p["id"].(string)
	path := p["path"].(string)
	if p["serves"] != "node" || p["appService"] != "node" || p["devHostname"] != "shop-dev.test" || p["status"].(map[string]any)["state"] != "running" {
		t.Fatalf("created project: %v", p)
	}
	for _, svc := range p["services"].([]any) {
		if svc.(map[string]any)["kind"] == "php" {
			t.Fatalf("project must have no PHP service: %v", p["services"])
		}
	}

	// Without PHP there are no PHP logs.
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/php/logs", nil, false)
	if r.status != http.StatusNotFound {
		t.Fatalf("php logs without PHP: %d %s", r.status, r.raw)
	}
	// PHP can be added later: it becomes the application (routing, published HTTP port),
	// and removed again ("enabled": false), which hands the project back to the dev server.
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("add php: %d %s", r.status, r.raw)
	}
	p = r.body["project"].(map[string]any)
	if p["serves"] != "php" || p["appService"] != "php" {
		t.Fatalf("after adding PHP: serves=%v app=%v", p["serves"], p["appService"])
	}
	webPublished := false
	for _, svc := range p["status"].(map[string]any)["services"].([]any) {
		m := svc.(map[string]any)
		if m["kind"] == "web" && len(m["ports"].([]any)) > 0 {
			webPublished = true
		}
	}
	if !webPublished {
		t.Fatalf("the web port must be published again once PHP serves the project: %s", r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"php": map[string]any{"enabled": false}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("remove php: %d %s", r.status, r.raw)
	}
	p = r.body["project"].(map[string]any)
	if p["serves"] != "node" {
		t.Fatalf("after removing PHP the dev server serves again: %v", p["serves"])
	}
	for _, svc := range p["services"].([]any) {
		if svc.(map[string]any)["kind"] == "php" {
			t.Fatalf("PHP service must be gone: %v", p["services"])
		}
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"php": map[string]any{"enabled": false}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("removing PHP twice must be a no-op: %d %s", r.status, r.raw)
	}
	// The SPA fallback belongs to projects without PHP; a PHP project rejects it.
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Blog", "php": map[string]any{"version": "8.4"}, "web": map[string]any{"type": "caddy", "spaFallback": true}}, true)
	if r.status != http.StatusUnprocessableEntity || errCode(r) != "validation_failed" {
		t.Fatalf("spaFallback with PHP: %d %s", r.status, r.raw)
	}

	// The Node terminal is the project's shell.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{"Cookie": {auth.CookieName + "=" + a.cookie.Value}}
	base := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/v1/projects/" + id + "/"
	conn, _, err := websocket.Dial(ctx, base+"services/node/terminal/ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("node terminal: %v", err)
	}
	conn.CloseNow()
	rec := a.engine.Terminals()[0]
	if rec.Container != "envoryx-shop-node" || rec.Opts.User != "1000:1000" || rec.Opts.WorkingDir != "/var/www/html" {
		t.Fatalf("terminal options: %+v", rec)
	}

	// Actions: only the services the project has, npm once package.json exists.
	actions := func() map[string]bool {
		r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/actions", nil, false)
		if r.status != http.StatusOK {
			t.Fatalf("actions: %d %s", r.status, r.raw)
		}
		out := map[string]bool{}
		for _, x := range r.body["actions"].([]any) {
			act := x.(map[string]any)
			out[act["id"].(string)] = act["available"].(bool)
		}
		return out
	}
	avail := actions()
	for id := range avail {
		if strings.HasPrefix(id, "composer:") || strings.HasPrefix(id, "artisan:") || strings.HasPrefix(id, "php:") {
			t.Fatalf("PHP action %s listed on a Node-only project: %v", id, avail)
		}
	}
	if on, ok := avail["node:version"]; !ok || !on || avail["npm:install"] {
		t.Fatalf("node actions: %v", avail)
	}
	if err := os.WriteFile(filepath.Join(a.projDir, path, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if avail = actions(); !avail["npm:install"] {
		t.Fatalf("npm:install after package.json: %v", avail)
	}

	// A static project (no runtime at all) is valid too and takes the SPA fallback.
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Docs", "start": true, "createStarter": true, "web": map[string]any{"type": "caddy", "spaFallback": true}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("static create: %d %s", r.status, r.raw)
	}
	docs := r.body["project"].(map[string]any)
	if docs["serves"] != "static" || docs["appService"] != nil || docs["devHostname"] != nil {
		t.Fatalf("static project: %v", docs)
	}
	web := docs["services"].([]any)[0].(map[string]any)
	if web["kind"] != "web" || web["config"].(map[string]any)["spaFallback"] != true {
		t.Fatalf("static web service: %v", web)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+docs["id"].(string), map[string]any{"web": map[string]any{"type": "nginx", "spaFallback": false}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("static update: %d %s", r.status, r.raw)
	}
	web = r.body["project"].(map[string]any)["services"].([]any)[0].(map[string]any)
	if web["variant"] != "nginx" || web["config"].(map[string]any)["spaFallback"] != nil {
		t.Fatalf("static web service after update: %v", web)
	}
}

func TestObjectStorageEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{"name": "Shop", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"}, "storage": map[string]any{}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)

	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/storage", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("info: %d %s", r.status, r.raw)
	}
	info := r.body["storage"].(map[string]any)
	if info["bucket"] != "shop" || info["publicRead"] != true || info["hostname"] != "shop-s3.test" || info["secretKey"] != nil || info["consolePath"] != "/rustfs/console/" {
		t.Fatalf("info: %v", info)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/storage/credentials", nil, false)
	creds := r.body["storage"].(map[string]any)
	if r.status != http.StatusOK || creds["accessKey"] == nil || creds["secretKey"] == nil {
		t.Fatalf("credentials: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPut, "/api/v1/projects/"+id+"/storage/public", map[string]any{"publicRead": false}, true)
	if r.status != http.StatusOK || r.body["storage"].(map[string]any)["publicRead"] != false {
		t.Fatalf("public off: %d %s", r.status, r.raw)
	}
	// Logs of the storage container are readable like any other service's.
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/storage/logs?tail=5", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("logs: %d %s", r.status, r.raw)
	}
	// Removing needs confirmation, then the info endpoint reports 404.
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"storage": map[string]any{"enabled": false}}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("remove without confirmation: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"storage": map[string]any{"enabled": false, "removeData": true}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("remove: %d %s", r.status, r.raw)
	}
	if r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/storage", nil, false); r.status != http.StatusNotFound {
		t.Fatalf("info after removal: %d", r.status)
	}
	// Projects without storage: a project with none gets 404, not 500.
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Plain", "createStarter": true, "php": map[string]any{"version": "8.4"}}, true)
	plain := r.body["project"].(map[string]any)["id"].(string)
	if r = a.do(http.MethodGet, "/api/v1/projects/"+plain+"/storage", nil, false); r.status != http.StatusNotFound {
		t.Fatalf("plain project storage: %d", r.status)
	}
}

// Envoryx with an IP of its own (Unraid br0) cannot serve published project ports under
// the browser's address: the settings then flag the missing public host and suggest the
// Docker host. Plain bridge networking needs nothing.
func TestSettingsAdvisePublicHostWhenEnvoryxHasOwnIP(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodGet, "/api/v1/settings", nil, false)
	if r.status != http.StatusOK || r.body["publicHostNeeded"] != false {
		t.Fatalf("bridge networking must not need a public host: %d %v", r.status, r.body["publicHostNeeded"])
	}
	a.proxy.Address = "192.168.1.5"
	r = a.do(http.MethodGet, "/api/v1/settings", nil, false)
	if r.body["publicHostNeeded"] != true {
		t.Fatalf("own IP without public host must be flagged: %v", r.body["publicHostNeeded"])
	}
	suggestion, _ := r.body["publicHostSuggestion"].(map[string]any)
	if suggestion["hostname"] != "fakehost" {
		t.Fatalf("suggestion must carry the daemon's host name: %v", r.body["publicHostSuggestion"])
	}
	r = a.do(http.MethodPatch, "/api/v1/settings", map[string]any{"publicHost": "192.168.1.10"}, true)
	if r.status != http.StatusOK {
		t.Fatalf("set public host: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/settings", nil, false)
	if r.body["publicHostNeeded"] != false || r.body["publicHostSuggestion"] != nil {
		t.Fatalf("configured public host must clear the advice: %v %v", r.body["publicHostNeeded"], r.body["publicHostSuggestion"])
	}
}

// The diagnostics endpoint runs every check, groups them and turns the missing public
// host into a warning with a one-click action.
func TestDiagnostics(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodGet, "/api/v1/system/diagnostics", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("diagnostics: %d %s", r.status, r.raw)
	}
	checks := r.body["checks"].([]any)
	byID := map[string]map[string]any{}
	for _, c := range checks {
		m := c.(map[string]any)
		byID[m["id"].(string)] = m
	}
	for _, id := range []string{"docker.engine", "storage.config", "storage.hostpaths", "storage.disk", "storage.backups", "storage.database", "network.publicHost", "network.proxy", "network.dns", "network.ssh", "security.tls", "maintenance.update", "maintenance.reconcile", "maintenance.notifications"} {
		if byID[id] == nil {
			t.Fatalf("check %s missing: %v", id, r.raw)
		}
	}
	for _, id := range []string{"docker.engine", "storage.database", "storage.backups", "network.publicHost"} {
		if byID[id]["status"] != "ok" {
			t.Fatalf("%s should be ok: %v", id, byID[id])
		}
	}
	if byID["docker.engine"]["detail"].(string) == "" || !strings.Contains(byID["docker.engine"]["detail"].(string), "fakehost") {
		t.Fatalf("docker detail: %v", byID["docker.engine"]["detail"])
	}
	summary := r.body["summary"].(map[string]any)
	if summary["error"].(float64) != 0 {
		t.Fatalf("no errors expected on a healthy test instance: %v", summary)
	}

	// Envoryx on its own IP without a public host: warning with the fix attached.
	a.proxy.Address = "192.168.1.5"
	r = a.do(http.MethodGet, "/api/v1/system/diagnostics", nil, false)
	for _, c := range r.body["checks"].([]any) {
		m := c.(map[string]any)
		if m["id"] == "network.publicHost" {
			action, _ := m["action"].(map[string]any)
			if m["status"] != "warning" || action["kind"] != "setPublicHost" || action["value"] != "fakehost" {
				t.Fatalf("public host check: %v", m)
			}
		}
	}
	// Docker down: the engine check errors, the rest still answers.
	a.engine.SetUnavailable(true)
	r = a.do(http.MethodGet, "/api/v1/system/diagnostics", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("diagnostics with docker down: %d", r.status)
	}
	for _, c := range r.body["checks"].([]any) {
		if m := c.(map[string]any); m["id"] == "docker.engine" && m["status"] != "error" {
			t.Fatalf("engine check with docker down: %v", m)
		}
	}
	a.engine.SetUnavailable(false)
	// Read-scoped tokens may not see it (it reveals paths and internal addresses).
	tok := a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "monitor", "scope": "read"}, true)
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+"/api/v1/system/diagnostics", nil)
	req.Header.Set("Authorization", "Bearer "+tok.body["secret"].(string))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("read token on diagnostics: %d", res.StatusCode)
	}
}

// Orphaned volumes are removed on request only, and only when they are on the list.
func TestRemoveOrphanOverHTTP(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	if err := a.engine.CreateVolume(context.Background(), "envoryx-ghost-database", docker.ManagedLabels("ghost-id", "ghost", "database", "test")); err != nil {
		t.Fatal(err)
	}
	r := a.do(http.MethodPost, "/api/v1/system/reconcile", nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("reconcile: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/docker", nil, false)
	orphans := r.body["orphans"].([]any)
	if len(orphans) != 1 {
		t.Fatalf("orphans: %s", r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/docker/orphans/remove", map[string]any{"type": "volume"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("missing id must be rejected: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/docker/orphans/remove", map[string]any{"type": "volume", "id": "not-listed"}, true)
	if r.status != http.StatusNotFound {
		t.Fatalf("unknown orphan must be 404: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/docker/orphans/remove", map[string]any{"type": "volume", "id": "envoryx-ghost-database"}, true)
	if r.status != http.StatusOK || len(r.body["orphans"].([]any)) != 0 {
		t.Fatalf("remove orphan: %d %s", r.status, r.raw)
	}
	if volumes, _ := a.engine.ListVolumes(context.Background(), true); len(volumes) != 0 {
		t.Fatalf("volume left: %+v", volumes)
	}
}

func TestPythonProjectOverHTTP(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{
		"name": "Api", "createStarter": true, "start": true,
		"python": map[string]any{"version": "3.13", "server": true, "preset": "asgi", "app": "main:app", "debug": true},
		"node":   map[string]any{"version": "24", "devServer": true, "preset": "vite"},
	}
	r := a.do(http.MethodPost, "/api/v1/projects/preview", create, true)
	if r.status != http.StatusOK {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	pv := r.body["preview"].(map[string]any)
	kinds := []string{}
	for _, c := range pv["containers"].([]any) {
		kinds = append(kinds, c.(map[string]any)["service"].(string))
	}
	// Python serves the project URL; the Node dev server keeps its own -dev host name.
	if strings.Join(kinds, ",") != "python,node,web" || pv["serves"] != "python" || pv["appService"] != "python" || pv["devHostname"] != "api-dev.test" {
		t.Fatalf("preview: %v", pv)
	}
	ports := map[string]int{}
	for _, c := range pv["containers"].([]any) {
		m := c.(map[string]any)
		ports[m["service"].(string)] = len(m["ports"].([]any))
	}
	if ports["python"] != 2 || ports["node"] != 1 || ports["web"] != 0 {
		t.Fatalf("published ports (python server+debugpy, node dev server, web unpublished): %v", ports)
	}

	r = a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	p := r.body["project"].(map[string]any)
	id := p["id"].(string)
	path := p["path"].(string)
	if p["serves"] != "python" || p["appService"] != "python" || p["status"].(map[string]any)["state"] != "running" {
		t.Fatalf("created project: %v", p)
	}
	var pyCfg map[string]any
	for _, svc := range p["services"].([]any) {
		m := svc.(map[string]any)
		if m["kind"] == "python" {
			pyCfg = m["config"].(map[string]any)
		}
	}
	if pyCfg == nil || pyCfg["preset"] != "asgi" || pyCfg["app"] != "main:app" || pyCfg["port"].(float64) != 8000 || pyCfg["hostPort"].(float64) == 0 || pyCfg["debugPort"].(float64) != 5678 || pyCfg["debugHostPort"].(float64) == 0 {
		t.Fatalf("python config: %v", pyCfg)
	}
	// The starter page is not written: the application server answers the project URL.
	if _, err := os.Stat(filepath.Join(a.projDir, path, "index.html")); err == nil {
		t.Fatalf("no starter page while the Python server serves the project")
	}

	// The Python terminal runs as the project owner with the venv on PATH.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{"Cookie": {auth.CookieName + "=" + a.cookie.Value}}
	base := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/api/v1/projects/" + id + "/"
	conn, _, err := websocket.Dial(ctx, base+"services/python/terminal/ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("python terminal: %v", err)
	}
	conn.CloseNow()
	rec := a.engine.Terminals()[0]
	if rec.Container != "envoryx-api-python" || rec.Opts.User != "1000:1000" || !slices.Contains(rec.Opts.Env, "VIRTUAL_ENV=/var/www/html/.venv") {
		t.Fatalf("terminal options: %+v", rec)
	}

	// Actions: Python entries only, Django ones once manage.py exists.
	actions := func() map[string]bool {
		r := a.do(http.MethodGet, "/api/v1/projects/"+id+"/actions", nil, false)
		if r.status != http.StatusOK {
			t.Fatalf("actions: %d %s", r.status, r.raw)
		}
		out := map[string]bool{}
		for _, x := range r.body["actions"].([]any) {
			act := x.(map[string]any)
			out[act["id"].(string)] = act["available"].(bool)
		}
		return out
	}
	avail := actions()
	for id := range avail {
		if strings.HasPrefix(id, "composer:") || strings.HasPrefix(id, "php:") {
			t.Fatalf("PHP action %s listed on a Python project: %v", id, avail)
		}
	}
	if on, ok := avail["python:version"]; !ok || !on || avail["pip:install"] || avail["django:migrate"] {
		t.Fatalf("python actions: %v", avail)
	}
	if err := os.WriteFile(filepath.Join(a.projDir, path, "manage.py"), []byte("#"), 0o644); err != nil {
		t.Fatal(err)
	}
	if avail = actions(); !avail["django:migrate"] {
		t.Fatalf("django:migrate after manage.py: %v", avail)
	}

	// Switching the server off keeps the container as tooling and publishes the web port.
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"python": map[string]any{"enabled": true, "version": "3.13", "server": false}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("server off: %d %s", r.status, r.raw)
	}
	p = r.body["project"].(map[string]any)
	if p["serves"] != "node" || p["appService"] != "python" {
		t.Fatalf("without the Python server the Node dev server serves: serves=%v app=%v", p["serves"], p["appService"])
	}
	// Removing Python hands the project to the dev server entirely.
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"python": map[string]any{"enabled": false}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("remove python: %d %s", r.status, r.raw)
	}
	p = r.body["project"].(map[string]any)
	if p["serves"] != "node" || p["appService"] != "node" {
		t.Fatalf("after removing Python: serves=%v app=%v", p["serves"], p["appService"])
	}
	for _, svc := range p["services"].([]any) {
		if svc.(map[string]any)["kind"] == "python" {
			t.Fatalf("python service must be gone: %v", p["services"])
		}
	}
	// A Python project rejects an unknown preset and a bad app path.
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Bad", "python": map[string]any{"version": "3.13", "server": true, "preset": "rails"}}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown preset: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Bad", "python": map[string]any{"version": "3.13", "server": true, "preset": "asgi", "app": "main:app; rm -rf /"}}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad app path: %d %s", r.status, r.raw)
	}
}

func TestRabbitMQEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{"name": "Queue", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"}, "rabbitmq": map[string]any{}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)

	// The extras list names the user but leaves the password to the credentials endpoint.
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/extras", nil, false)
	svc := r.body["services"].([]any)[0].(map[string]any)
	if r.status != http.StatusOK || svc["kind"] != "rabbitmq" || svc["username"] != "envoryx" || svc["password"] != nil || svc["webUiPort"] == nil {
		t.Fatalf("extras: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/rabbitmq/credentials", nil, false)
	creds := r.body["credentials"].(map[string]any)
	if r.status != http.StatusOK || creds["password"] == "" || !strings.HasPrefix(creds["url"].(string), "amqp://envoryx:") {
		t.Fatalf("credentials: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/rabbitmq/logs?tail=5", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("logs: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"rabbitmq": map[string]any{"enabled": false, "removeData": true}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("remove: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/rabbitmq/credentials", nil, false)
	if r.status != http.StatusNotFound {
		t.Fatalf("credentials after removal: %d %s", r.status, r.raw)
	}
}

func TestMemcachedEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{"name": "Cache", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"}, "memcached": map[string]any{"exposePort": true}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/extras", nil, false)
	svc := r.body["services"].([]any)[0].(map[string]any)
	if r.status != http.StatusOK || svc["kind"] != "memcached" || svc["port"] != float64(11211) || svc["hostPort"] == float64(0) {
		t.Fatalf("extras: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/memcached/logs?tail=5", nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("logs: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"memcached": map[string]any{"enabled": false}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("remove: %d %s", r.status, r.raw)
	}
}

func TestSearchEndpoints(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{"name": "Search", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"}, "meilisearch": map[string]any{}, "typesense": map[string]any{"exposePort": true}, "opensearch": map[string]any{"dashboards": true}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/extras", nil, false)
	if r.status != http.StatusOK || len(r.body["services"].([]any)) != 3 {
		t.Fatalf("extras: %d %s", r.status, r.raw)
	}
	for _, kind := range []string{"meilisearch", "typesense"} {
		r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/"+kind+"/credentials", nil, false)
		creds, _ := r.body["credentials"].(map[string]any)
		if r.status != http.StatusOK || creds["apiKey"] == "" || creds["url"] == "" {
			t.Fatalf("%s credentials: %d %s", kind, r.status, r.raw)
		}
		r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/"+kind+"/logs?tail=5", nil, false)
		if r.status != http.StatusOK {
			t.Fatalf("%s logs: %d %s", kind, r.status, r.raw)
		}
		r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{kind: map[string]any{"enabled": false}}, true)
		if r.status != http.StatusUnprocessableEntity {
			t.Fatalf("%s removal without removeData: %d %s", kind, r.status, r.raw)
		}
		r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{kind: map[string]any{"enabled": false, "removeData": true}}, true)
		if r.status != http.StatusOK {
			t.Fatalf("%s remove: %d %s", kind, r.status, r.raw)
		}
		r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/"+kind+"/credentials", nil, false)
		if r.status != http.StatusNotFound {
			t.Fatalf("%s credentials after removal: %d %s", kind, r.status, r.raw)
		}
	}
	// OpenSearch has no key; its logs and removal work like the others'. Dashboards shows
	// up on OpenSearch's entry and has logs of its own.
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/extras", nil, false)
	search := r.body["services"].([]any)[0].(map[string]any)
	if search["kind"] != "opensearch" || search["webUiPort"] == nil || search["dashboards"] == nil {
		t.Fatalf("opensearch with dashboards: %s", r.raw)
	}
	for _, kind := range []string{"opensearch", "opensearch-dashboards"} {
		r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/services/"+kind+"/logs?tail=5", nil, false)
		if r.status != http.StatusOK {
			t.Fatalf("%s logs: %d %s", kind, r.status, r.raw)
		}
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"opensearch": map[string]any{"enabled": true, "dashboards": false}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("dashboards off: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, "/api/v1/projects/"+id+"/extras", nil, false)
	if search := r.body["services"].([]any)[0].(map[string]any); search["webUiPort"] != nil || search["dashboards"] != nil {
		t.Fatalf("dashboards still listed: %s", r.raw)
	}
	r = a.do(http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"opensearch": map[string]any{"enabled": false, "removeData": true}}, true)
	if r.status != http.StatusOK {
		t.Fatalf("opensearch remove: %d %s", r.status, r.raw)
	}
}

func TestCronJobsOverHTTP(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	create := map[string]any{"name": "Cron", "createStarter": true, "start": true, "php": map[string]any{"version": "8.4"}}
	r := a.do(http.MethodPost, "/api/v1/projects", create, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	base := "/api/v1/projects/" + id + "/cron"

	r = a.do(http.MethodPost, "/api/v1/cron/preview", map[string]any{"schedule": "0 3 * * *"}, true)
	if r.status != http.StatusOK || len(r.body["next"].([]any)) != 5 || r.body["timezone"] == "" {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodPost, "/api/v1/cron/preview", map[string]any{"schedule": "0 0 30 2 *"}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("preview of a schedule that never fires: %d %s", r.status, r.raw)
	}

	job := map[string]any{"name": "report", "runtime": "php", "schedule": "*/10 * * * *", "command": "echo hello", "timeoutSeconds": 60, "enabled": true}
	r = a.do(http.MethodPost, base, job, true)
	if r.status != http.StatusCreated {
		t.Fatalf("add: %d %s", r.status, r.raw)
	}
	jobID := r.body["job"].(map[string]any)["id"].(string)
	if r.body["job"].(map[string]any)["nextRun"] == nil {
		t.Fatalf("no next run: %s", r.raw)
	}
	r = a.do(http.MethodPost, base, map[string]any{"name": "bad", "runtime": "php", "schedule": "61 * * * *", "command": "true", "enabled": true}, true)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid schedule: %d %s", r.status, r.raw)
	}

	a.engine.StreamHandler = func(_ string, cmd, _ []string, _ []byte) (string, int, error) {
		if cmd[0] == "timeout" {
			return "hello\n", 0, nil
		}
		return "", 0, nil
	}
	r = a.do(http.MethodPost, base+"/"+jobID+"/run", nil, true)
	if r.status != http.StatusAccepted {
		t.Fatalf("run: %d %s", r.status, r.raw)
	}
	var run map[string]any
	for i := 0; i < 100; i++ {
		r = a.do(http.MethodGet, base+"/"+jobID+"/runs", nil, false)
		if runs := r.body["runs"].([]any); len(runs) == 1 && runs[0].(map[string]any)["status"] != "running" {
			run = runs[0].(map[string]any)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run == nil || run["status"] != "succeeded" || run["output"] != "hello\n" || run["source"] != "manual" {
		t.Fatalf("run: %v", run)
	}
	r = a.do(http.MethodGet, base, nil, false)
	listed := r.body["jobs"].([]any)[0].(map[string]any)
	if r.status != http.StatusOK || listed["lastRun"].(map[string]any)["status"] != "succeeded" || listed["lastRun"].(map[string]any)["output"] != nil || r.body["timezone"] == "" {
		t.Fatalf("list: %d %s", r.status, r.raw)
	}

	// A read token sees the jobs but neither the output nor the run button.
	tok := a.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "monitor", "scope": "read"}, true)
	for path, want := range map[string]int{"GET " + base: http.StatusOK, "GET " + base + "/" + jobID + "/runs": http.StatusForbidden, "POST " + base + "/" + jobID + "/run": http.StatusForbidden} {
		method, url, _ := strings.Cut(path, " ")
		req, _ := http.NewRequest(method, a.srv.URL+url, nil)
		req.Header.Set("Authorization", "Bearer "+tok.body["secret"].(string))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("read token %s: %d, want %d", path, res.StatusCode, want)
		}
	}

	job["enabled"] = false
	r = a.do(http.MethodPut, base+"/"+jobID, job, true)
	if r.status != http.StatusOK || r.body["job"].(map[string]any)["nextRun"] != nil {
		t.Fatalf("disable: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodDelete, base+"/"+jobID, nil, true)
	if r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
	r = a.do(http.MethodGet, base+"/"+jobID+"/runs", nil, false)
	if r.status != http.StatusNotFound {
		t.Fatalf("runs after delete: %d %s", r.status, r.raw)
	}
}
