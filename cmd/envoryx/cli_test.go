package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeServer answers the API calls the CLI makes, recording the requests so the tests can
// check what was sent.
type fakeServer struct {
	*httptest.Server
	requests []string
	bodies   map[string]json.RawMessage
	token    string
}

func newFakeServer(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *fakeServer {
	t.Helper()
	f := &fakeServer{bodies: map[string]json.RawMessage{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"projects": []map[string]any{
			{"id": "11111111-1111-4111-8111-111111111111", "name": "Acme Shop", "slug": "acme-shop", "path": "acme-shop",
				"appService": "php", "hostnames": []string{"acme-shop.test"},
				"lifecycle": "ready", "status": map[string]any{"state": "running"}},
			{"id": "22222222-2222-4222-8222-222222222222", "name": "Blog", "slug": "blog", "path": "blog",
				"appService": "node", "lifecycle": "ready", "status": map[string]any{"state": "stopped", "warnings": []string{"image changed"}}},
		}})
	})
	for pattern, h := range routes {
		mux.HandleFunc(pattern, h)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Body != nil {
			body, _ := readAllLimited(r.Body)
			if len(body) > 0 {
				f.bodies[r.URL.Path] = body
			}
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newTestCLI points the CLI at a server and keeps it away from the user's own
// configuration file.
func newTestCLI(t *testing.T, srv *fakeServer, stdin string) (*cli, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("ENVORYX_CLI_CONFIG", filepath.Join(t.TempDir(), "cli.json"))
	t.Setenv("ENVORYX_URL", srv.URL)
	t.Setenv("ENVORYX_TOKEN", "stq_testtoken")
	var out, errOut bytes.Buffer
	return &cli{out: &out, errOut: &errOut, stdin: strings.NewReader(stdin)}, &out, &errOut
}

func TestProjectListPrintsATable(t *testing.T) {
	srv := newFakeServer(t, nil)
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "list"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"NAME", "Acme Shop", "acme-shop", "running", "https://acme-shop.test", "stopped", "1 warning"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if srv.token != "stq_testtoken" {
		t.Fatalf("token sent = %q", srv.token)
	}
}

// A project is named by its name, its slug or its id; anything else is refused with the
// hint that the list shows them.
func TestFindProjectAcceptsNameSlugAndID(t *testing.T) {
	srv := newFakeServer(t, nil)
	c, _, _ := newTestCLI(t, srv, "")
	ctx := context.Background()
	for _, want := range []string{"Acme Shop", "acme shop", "acme-shop", "11111111-1111-4111-8111-111111111111"} {
		p, err := c.findProject(ctx, want)
		if err != nil || p.Slug != "acme-shop" {
			t.Fatalf("%q: %v %+v", want, err, p)
		}
	}
	if _, err := c.findProject(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "project list") {
		t.Fatalf("unknown project: %v", err)
	}
	if _, err := c.findProject(ctx, ""); !errors.Is(err, errCLIUsage) {
		t.Fatalf("missing project: %v", err)
	}
}

// exec keeps the container's streams apart and hands its exit code to the caller.
func TestProjectExecSplitsStreamsAndPassesTheExitCode(t *testing.T) {
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects/{id}/services/{kind}/exec": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			for _, frame := range []string{
				`{"type":"stdout","text":"building\n"}`,
				`{"type":"stderr","text":"warning: slow\n"}`,
				`{"type":"exit","code":4}`,
			} {
				_, _ = w.Write([]byte(frame + "\n"))
			}
		},
	})
	c, out, errOut := newTestCLI(t, srv, "")
	err := c.run(context.Background(), []string{"project", "exec", "acme-shop", "--", "composer", "install"})
	var exit *exitCodeError
	if !errors.As(err, &exit) || exit.code != 4 {
		t.Fatalf("exit: %v", err)
	}
	if out.String() != "building\n" || errOut.String() != "warning: slow\n" {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}
	var sent struct {
		Cmd   []string `json:"cmd"`
		Stdin string   `json:"stdin"`
	}
	if err := json.Unmarshal(srv.bodies["/api/v1/projects/11111111-1111-4111-8111-111111111111/services/php/exec"], &sent); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sent.Cmd, " ") != "composer install" {
		t.Fatalf("command sent: %v", sent.Cmd)
	}
	if cliCommandExit(err) != 4 {
		t.Fatalf("process exit code = %d", cliCommandExit(err))
	}
}

// Piped input reaches the command; the service defaults to the project's application
// container, and --service picks another one.
func TestProjectExecSendsStdinAndPicksTheService(t *testing.T) {
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects/{id}/services/{kind}/exec": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"type":"exit","code":0}` + "\n"))
		},
	})
	c, _, _ := newTestCLI(t, srv, "select 1;\n")
	if err := c.run(context.Background(), []string{"project", "exec", "blog", "--service", "database", "--", "psql"}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/22222222-2222-4222-8222-222222222222/services/database/exec"
	var sent struct {
		Stdin string `json:"stdin"`
	}
	if err := json.Unmarshal(srv.bodies[path], &sent); err != nil {
		t.Fatalf("no request to %s: %v", path, err)
	}
	if sent.Stdin != "select 1;\n" {
		t.Fatalf("stdin = %q", sent.Stdin)
	}
}

// The API's refusals are printed as they arrive – a token without the right scope says so.
func TestAPIRefusalsAreReported(t *testing.T) {
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects/{id}/start": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "forbidden", "message": "this token has read scope, the operation needs operate"}})
		},
	})
	c, _, _ := newTestCLI(t, srv, "")
	err := c.run(context.Background(), []string{"project", "start", "acme-shop"})
	if err == nil || !strings.Contains(err.Error(), "needs operate") {
		t.Fatalf("refusal: %v", err)
	}
}

// Deleting asks for --yes, and sends the slug the API wants as the confirmation.
func TestProjectDeleteNeedsConfirmation(t *testing.T) {
	var deleted bool
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"DELETE /api/v1/projects/{id}": func(w http.ResponseWriter, r *http.Request) {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		},
	})
	c, _, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "delete", "acme-shop"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("delete without --yes: %v", err)
	}
	if deleted {
		t.Fatal("the project was deleted without confirmation")
	}
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "delete", "acme-shop", "--yes"}); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Confirm     string `json:"confirm"`
		DeleteFiles bool   `json:"deleteFiles"`
	}
	_ = json.Unmarshal(srv.bodies["/api/v1/projects/11111111-1111-4111-8111-111111111111"], &sent)
	if sent.Confirm != "acme-shop" || sent.DeleteFiles {
		t.Fatalf("delete body: %+v", sent)
	}
}

// login checks the token before it writes it down, and the file is readable by its owner
// only – it holds a credential.
func TestLoginStoresTheTokenOnlyAfterCheckingIt(t *testing.T) {
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/auth/me": func(w http.ResponseWriter, r *http.Request) {
			if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != "stq_good" {
				w.WriteHeader(http.StatusUnauthorized)
				writeJSON(w, map[string]any{"error": map[string]string{"code": "unauthenticated", "message": "invalid token"}})
				return
			}
			writeJSON(w, map[string]any{"user": map[string]any{"username": "admin"}, "token": map[string]any{"name": "laptop", "scope": "operate", "projects": []string{}}})
		},
	})
	c, out, _ := newTestCLI(t, srv, "")
	t.Setenv("ENVORYX_TOKEN", "")
	path := os.Getenv("ENVORYX_CLI_CONFIG")

	if err := c.run(context.Background(), []string{"login", "--token", "stq_wrong"}); err == nil {
		t.Fatal("a bad token was accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the configuration was written although the token was refused")
	}

	c, out, _ = newTestCLI(t, srv, "")
	t.Setenv("ENVORYX_TOKEN", "")
	path = os.Getenv("ENVORYX_CLI_CONFIG")
	if err := c.run(context.Background(), []string{"login", "--token", "stq_good"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "admin") || !strings.Contains(out.String(), "operate") {
		t.Fatalf("login output:\n%s", out.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("configuration mode = %v", info.Mode().Perm())
	}
	var stored cliConfig
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &stored)
	if stored.Token != "stq_good" || stored.URL != srv.URL {
		t.Fatalf("stored: %+v", stored)
	}
}

// Flags are understood before, between and after the positional arguments.
func TestParseAllowsFlagsAnywhere(t *testing.T) {
	c := &cli{}
	fs := c.newFlags("test")
	service := fs.String("service", "", "")
	pos, err := parse(fs, []string{"--json", "shop", "--service", "node", "extra"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 2 || pos[0] != "shop" || pos[1] != "extra" {
		t.Fatalf("positional = %v", pos)
	}
	if !c.json || *service != "node" {
		t.Fatalf("json=%v service=%q", c.json, *service)
	}
	if _, err := parse(c.newFlags("test"), []string{"--nope"}); !errors.Is(err, errCLIUsage) {
		t.Fatalf("unknown flag: %v", err)
	}
}

// Everything after "--" belongs to the command, flags included.
func TestSplitAtDashDash(t *testing.T) {
	own, cmd := splitAtDashDash([]string{"shop", "--service", "php", "--", "ls", "-la", "--json"})
	if strings.Join(own, " ") != "shop --service php" || strings.Join(cmd, " ") != "ls -la --json" {
		t.Fatalf("own=%v cmd=%v", own, cmd)
	}
}

// cliCommandExit mirrors what cliCommand turns an error into.
func cliCommandExit(err error) int {
	var exit *exitCodeError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.code
	case errors.Is(err, errCLIUsage):
		return 2
	default:
		return 1
	}
}
