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
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/seramos/staqio/internal/api"
	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/auth"
	"github.com/seramos/staqio/internal/config"
	"github.com/seramos/staqio/internal/db"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/docker/dockertest"
	"github.com/seramos/staqio/internal/hostpath"
	"github.com/seramos/staqio/internal/mcpserver"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/server"
	"github.com/seramos/staqio/internal/stats"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/tlsca"
)

type dockerExecResult = docker.ExecResult
type dockerSpec = docker.ContainerSpec

type testApp struct {
	t       *testing.T
	srv     *httptest.Server
	engine  *dockertest.Fake
	cookie  *http.Cookie
	projDir string
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
	certs, err := tlsca.Open(filepath.Join(cfgDir, "ca"))
	if err != nil {
		t.Fatal(err)
	}
	invalidations := 0
	proxyInfo := &api.ProxyInfo{Enabled: true, HTTPPort: 80, HTTPSPort: 443, InDocker: true, Invalidate: func() { invalidations++ }}
	mcpSrv := mcpserver.New(mcpserver.Deps{Projects: manager, Catalog: runtime.Default(), Auth: sessions, Version: "test", Log: log})
	a := api.New(api.Deps{Config: cfg, Version: "test", Store: st, Auth: sessions, Audit: auditLog, Engine: engine, Projects: manager,
		Catalog: runtime.Default(), Stats: stats.New(engine, time.Second, log), HostPath: resolver, Certs: certs, Proxy: proxyInfo, MCP: mcpSrv.Handler(), Log: log, StartedAt: time.Now()})
	s := server.New(server.Options{Addr: ":0", Log: log, MCP: mcpSrv.Handler()}, a, sessions, nil)
	handler := serverHandler(s)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &testApp{t: t, srv: srv, engine: engine, projDir: projDir}
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

func TestServiceLogsRESTAndWebSocket(t *testing.T) {
	a := newApp(t)
	a.setupAndLogin()
	r := a.do(http.MethodPost, "/api/v1/projects", map[string]any{"name": "Logs", "start": true, "php": map[string]any{"version": "8.4"}}, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	id := r.body["project"].(map[string]any)["id"].(string)
	a.engine.Logs["staqio-logs-php"] = []docker.LogLine{
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
	if rec.Container != "staqio-term-php" || rec.Opts.User != "1000:1000" || rec.Opts.WorkingDir != "/var/www/html" || rec.Opts.Cols != 100 || rec.Opts.Rows != 30 {
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
