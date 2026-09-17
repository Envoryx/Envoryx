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
	"testing"
	"time"

	"github.com/seramos/staqio/internal/api"
	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/auth"
	"github.com/seramos/staqio/internal/config"
	"github.com/seramos/staqio/internal/db"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/docker/dockertest"
	"github.com/seramos/staqio/internal/hostpath"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/server"
	"github.com/seramos/staqio/internal/stats"
	"github.com/seramos/staqio/internal/store"
)

type dockerExecResult = docker.ExecResult

type testApp struct {
	t      *testing.T
	srv    *httptest.Server
	engine *dockertest.Fake
	cookie *http.Cookie
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
	a := api.New(api.Deps{Config: cfg, Version: "test", Store: st, Auth: sessions, Audit: auditLog, Engine: engine, Projects: manager,
		Catalog: runtime.Default(), Stats: stats.New(engine, time.Second, log), HostPath: resolver, Log: log, StartedAt: time.Now()})
	s := server.New(server.Options{Addr: ":0", Log: log}, a, sessions, nil)
	handler := serverHandler(s)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &testApp{t: t, srv: srv, engine: engine}
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
		req.Header.Set("X-Requested-With", "Staqio")
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
	req.Header.Set("X-Requested-With", "Staqio")
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
	req.Header.Set("X-Requested-With", "Staqio")
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
		"name": "Shimly API", "docroot": "public", "createStarter": true, "start": true,
		"php": map[string]any{"version": "8.4", "config": runtime.DefaultPHPConfig()},
		"env": []map[string]any{{"key": "APP_ENV", "value": "local", "isSecret": false}},
	}
	r := a.do(http.MethodPost, "/api/v1/projects/preview", create, true)
	if r.status != http.StatusOK {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	pv := r.body["preview"].(map[string]any)
	if pv["network"] != "staqio-shimly-api" || len(pv["containers"].([]any)) != 2 {
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
	r = a.do(http.MethodDelete, "/api/v1/projects/"+id, map[string]any{"confirm": "shimly-api"}, true)
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
}
