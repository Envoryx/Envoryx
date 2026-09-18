package mcpserver_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/docker/dockertest"
	"github.com/envoryx/envoryx/internal/mcpserver"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

type env struct {
	t       *testing.T
	srv     *mcpserver.Server
	engine  *dockertest.Fake
	st      *store.Store
	auth    *auth.Service
	projDir string
	session *mcp.ClientSession
}

func newEnv(t *testing.T) *env {
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
	paths := func() (project.Paths, error) {
		return project.Paths{ConfigDir: cfgDir, ConfigHostDir: "/host/config", ProjectsDir: projDir, ProjectsHostDir: "/host/projects", PUID: 1000, PGID: 1000}, nil
	}
	sessions := auth.NewService(st, auth.Options{IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour}, log)
	manager := project.NewManager(st, engine, runtime.Default(), paths, audit.New(st.Audit, log), project.Config{PortRangeStart: 20000, PortRangeEnd: 20010}, log)
	srv := mcpserver.New(mcpserver.Deps{Projects: manager, Catalog: runtime.Default(), Auth: sessions, Version: "test", Log: log,
		Links: mcpserver.Links{PublicHost: func(context.Context) string { return "nas.lan" }, HTTPPort: 80, HTTPSPort: 443}})

	ct, stt := mcp.NewInMemoryTransports()
	if _, err := srv.MCP().Connect(context.Background(), stt, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &env{t: t, srv: srv, engine: engine, st: st, auth: sessions, projDir: projDir, session: session}
}

// call invokes a tool and decodes its structured output.
func (e *env) call(name string, args map[string]any, out any) *mcp.CallToolResult {
	e.t.Helper()
	res, err := e.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	if !res.IsError && out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s: decode output: %v", name, err)
		}
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolsCoverTheProjectLifecycle(t *testing.T) {
	e := newEnv(t)

	tools, err := e.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"list_projects", "create_project", "run_action", "get_logs", "create_backup"} {
		if !names[want] {
			t.Fatalf("tool %s missing: %v", want, names)
		}
	}
	for _, forbidden := range []string{"delete_project", "drop_database", "restore_backup"} {
		if names[forbidden] {
			t.Fatalf("destructive tool %s must not be exposed", forbidden)
		}
	}

	var rt struct {
		Runtimes      []struct{ Key string }
		PHPExtensions []string `json:"phpExtensions"`
	}
	e.call("list_runtimes", nil, &rt)
	if len(rt.Runtimes) == 0 || len(rt.PHPExtensions) == 0 {
		t.Fatalf("runtimes: %+v", rt)
	}

	var p struct {
		ID, Slug, State, URL, DirectURL string
		Hostnames                       []string
		Services                        []struct{ Kind string }
	}
	e.call("create_project", map[string]any{"name": "Test API", "phpVersion": "8.4", "database": "mariadb", "redis": true, "env": map[string]string{"APP_ENV": "local"}}, &p)
	if p.Slug != "test-api" || p.State != "running" || p.URL != "https://test-api.test" || p.DirectURL != "http://nas.lan:20000" {
		t.Fatalf("created project: %+v", p)
	}
	kinds := map[string]bool{}
	for _, s := range p.Services {
		kinds[s.Kind] = true
	}
	if !kinds["php"] || !kinds["database"] || !kinds["redis"] || !kinds["web"] {
		t.Fatalf("services: %v", kinds)
	}

	res := e.call("create_project", map[string]any{"name": "Test API"}, nil)
	if !res.IsError || !strings.Contains(text(res), "already") {
		t.Fatalf("duplicate must be a tool error: %v %s", res.IsError, text(res))
	}
	res = e.call("create_project", map[string]any{"name": "Bad", "phpVersion": "1.0"}, nil)
	if !res.IsError {
		t.Fatalf("unknown version must fail: %s", text(res))
	}

	// Resolve by slug and by name.
	var got struct{ ID string }
	e.call("get_project", map[string]any{"project": "test-api"}, &got)
	if got.ID != p.ID {
		t.Fatalf("resolve by slug: %s != %s", got.ID, p.ID)
	}
	e.call("get_project", map[string]any{"project": "test api"}, &got)
	if got.ID != p.ID {
		t.Fatalf("resolve by name: %s != %s", got.ID, p.ID)
	}
	if res := e.call("get_project", map[string]any{"project": "nope"}, nil); !res.IsError {
		t.Fatal("unknown project must be a tool error")
	}

	var st struct{ State string }
	e.call("stop_project", map[string]any{"project": "test-api"}, &st)
	if st.State != "stopped" {
		t.Fatalf("stop: %s", st.State)
	}
	e.call("start_project", map[string]any{"project": "test-api"}, &st)
	if st.State != "running" {
		t.Fatalf("start: %s", st.State)
	}

	e.engine.Logs["envoryx-test-api-php"] = []docker.LogLine{{Time: time.Now(), Stream: "stderr", Text: "NOTICE: ready to handle connections"}}
	var logs struct {
		Service string
		Lines   []struct{ Text string }
	}
	e.call("get_logs", map[string]any{"project": "test-api", "tail": 10}, &logs)
	if logs.Service != "php" || len(logs.Lines) != 1 || !strings.Contains(logs.Lines[0].Text, "ready") {
		t.Fatalf("logs: %+v", logs)
	}
	if res := e.call("get_logs", map[string]any{"project": "test-api", "service": "shell"}, nil); !res.IsError {
		t.Fatal("unknown service must fail")
	}

	var dbs struct {
		Engine    string
		Databases []string
	}
	e.call("create_database", map[string]any{"project": "test-api", "name": "analytics"}, &dbs)
	if dbs.Engine != "mariadb" {
		t.Fatalf("databases: %+v", dbs)
	}
	if res := e.call("create_database", map[string]any{"project": "test-api", "name": "Drop Table"}, nil); !res.IsError {
		t.Fatal("invalid database name must fail")
	}

	var b struct{ ID, Kind string }
	e.call("create_backup", map[string]any{"project": "test-api", "note": "via mcp"}, &b)
	if b.ID == "" {
		t.Fatalf("backup: %+v", b)
	}
	var bl struct{ Backups []struct{ Note string } }
	e.call("list_backups", map[string]any{"project": "test-api"}, &bl)
	if len(bl.Backups) != 1 || bl.Backups[0].Note != "via mcp" {
		t.Fatalf("backups: %+v", bl)
	}

	var dom struct{ Hostnames []string }
	e.call("add_domain", map[string]any{"project": "test-api", "hostname": "api.local"}, &dom)
	if len(dom.Hostnames) != 2 || dom.Hostnames[1] != "api.local" {
		t.Fatalf("domains: %v", dom.Hostnames)
	}
}

func TestRunActionCapturesOutputAndExitCode(t *testing.T) {
	e := newEnv(t)
	var p struct{ ID, Path string }
	e.call("create_project", map[string]any{"name": "Act"}, &p)
	if err := os.WriteFile(filepath.Join(e.projDir, p.Path, "composer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var acts struct {
		Actions []struct {
			ID        string
			Available bool
		}
	}
	e.call("list_actions", map[string]any{"project": "act"}, &acts)
	found := false
	for _, a := range acts.Actions {
		if a.ID == "composer:install" && a.Available {
			found = true
		}
	}
	if !found {
		t.Fatalf("composer:install must be available: %+v", acts)
	}
	if res := e.call("run_action", map[string]any{"project": "act", "action": "artisan:migrate"}, nil); !res.IsError {
		t.Fatal("unavailable action must be a tool error")
	}

	// Finish the fake terminal once the action has started.
	go func() {
		for i := 0; i < 200; i++ {
			if term := e.engine.LastTerminal(); term != nil {
				term.Finish("\x1b[32mInstalling\x1b[0m dependencies\r\nDone\r\n", 0)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var out struct {
		ExitCode int
		Output   string
		Command  string
	}
	res := e.call("run_action", map[string]any{"project": "act", "action": "composer:install"}, &out)
	if res.IsError || out.ExitCode != 0 || out.Output != "Installing dependencies\nDone" || !strings.HasPrefix(out.Command, "composer install") {
		t.Fatalf("run_action: err=%v %+v %s", res.IsError, out, text(res))
	}

	go func() {
		for i := 0; i < 200; i++ {
			if term := e.engine.LastTerminal(); term != nil && len(e.engine.Terminals()) == 2 {
				term.Finish("boom\n", 2)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	res = e.call("run_action", map[string]any{"project": "act", "action": "composer:install"}, nil)
	if !res.IsError || !strings.Contains(text(res), "code 2") || !strings.Contains(text(res), "boom") {
		t.Fatalf("failed action must be reported: %v %s", res.IsError, text(res))
	}
}

func TestHTTPEndpointRequiresBearerToken(t *testing.T) {
	e := newEnv(t)
	ts := httptest.NewServer(e.srv.Handler())
	defer ts.Close()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	post := func(token string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(initialize))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	if res := post(""); res.StatusCode != http.StatusUnauthorized || res.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	if res := post("stq_definitely-not-a-valid-token-value"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", res.StatusCode)
	}

	user, err := e.auth.CreateInitialAdmin(context.Background(), "admin", "supersecret123")
	if err != nil {
		t.Fatal(err)
	}
	token, rec, err := e.auth.CreateAPIToken(context.Background(), auth.Principal{UserID: user.ID, Username: user.Username, Role: user.Role}, "Claude")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "stq_") || rec.Prefix != token[:10] {
		t.Fatalf("token format: %s %s", token, rec.Prefix)
	}
	res := post(token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("valid token: %d", res.StatusCode)
	}
	var body struct {
		Result struct {
			ServerInfo struct{ Name string } `json:"serverInfo"`
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.Result.ServerInfo.Name != "envoryx" {
		t.Fatalf("initialize result: %v %+v", err, body)
	}

	if err := e.auth.RevokeAPIToken(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	if res := post(token); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", res.StatusCode)
	}
}
