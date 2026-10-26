package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
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
				"services":  []map[string]any{{"kind": "php", "enabled": true}, {"kind": "node", "enabled": false}},
				"lifecycle": "ready", "status": map[string]any{"state": "running"}},
			{"id": "22222222-2222-4222-8222-222222222222", "name": "Blog", "slug": "blog", "path": "blog",
				"appService": "node", "hostnames": []string{"blog.test"}, "services": []map[string]any{{"kind": "node", "enabled": true}}, "url": "https://blog.test:18443", "lifecycle": "ready", "status": map[string]any{"state": "stopped", "warnings": []string{"image changed"}}},
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

// Global flags work before the command as well as after it.
// Without an address from the server a project with a port has no proxy: its host name
// would lead nowhere, the port is what exists.
func TestProjectURLPrefersThePortOverAHostName(t *testing.T) {
	p := projectSummary{HTTPPort: 21100, Hostnames: []string{"shop.test"}}
	if got := p.URL(); got != "port 21100" {
		t.Fatalf("URL = %q", got)
	}
	p.Address = "https://shop.test"
	if got := p.URL(); got != "https://shop.test" {
		t.Fatalf("URL = %q", got)
	}
}

func TestGlobalFlagsBeforeTheCommand(t *testing.T) {
	srv := newFakeServer(t, nil)
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"--json", "project", "list"}); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal(out.Bytes(), &list); err != nil || len(list) != 2 {
		t.Fatalf("want the JSON list, got %v:\n%s", err, out.String())
	}
	var usage *usageError
	if err := c.run(context.Background(), []string{"--nonsense", "project", "list"}); !errors.As(err, &usage) {
		t.Fatalf("unknown flag: %v", err)
	}
}

func TestProjectListPrintsATable(t *testing.T) {
	srv := newFakeServer(t, nil)
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "list"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	// The server's URL carries the proxy port; without one the CLI falls back to the host name.
	for _, want := range []string{"NAME", "Acme Shop", "acme-shop", "running", "https://acme-shop.test", "https://blog.test:18443", "stopped", "1 warning"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if srv.token != "stq_testtoken" {
		t.Fatalf("token sent = %q", srv.token)
	}
}

// An internal error's message is generic; the cause the server adds in the details is
// what the operator needs.
func TestAPIErrorCarriesTheCause(t *testing.T) {
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/projects/{id}": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"an internal error occurred","details":{"cause":"inspect container: boom"}}}`))
		},
	})
	c, _, _ := newTestCLI(t, srv, "")
	err := c.run(context.Background(), []string{"project", "show", "--json", "blog"})
	if err == nil || err.Error() != "an internal error occurred: inspect container: boom" {
		t.Fatalf("error = %v", err)
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

// The API's refusals are printed as they arrive - a token without the right scope says so.
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
	if err := c.run(context.Background(), []string{"project", "delete", "acme-shop"}); !errors.Is(err, errCLIUsage) || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "backups are kept") {
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

func TestProjectDuplicateSendsOnlyTheSwitchedOffParts(t *testing.T) {
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects/{id}/duplicate": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"project": map[string]any{
				"id": "33333333-3333-4333-8333-333333333333", "name": "Acme Shop Test", "slug": "acme-shop-test",
				"lifecycle": "ready", "status": map[string]any{"state": "stopped"},
			}})
		},
	})
	c, out, _ := newTestCLI(t, srv, "")
	// The new name may be written without quotes.
	if err := c.run(context.Background(), []string{"project", "duplicate", "acme-shop", "Acme", "Shop", "Test", "--no-database"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(srv.bodies["/api/v1/projects/11111111-1111-4111-8111-111111111111/duplicate"], &sent)
	if sent["name"] != "Acme Shop Test" || sent["database"] != false {
		t.Fatalf("body: %v", sent)
	}
	for _, part := range []string{"files", "storage", "workers", "git", "start"} {
		if _, ok := sent[part]; ok {
			t.Fatalf("%s must be left to the server's default: %v", part, sent)
		}
	}
	if !strings.Contains(out.String(), "Copied acme-shop to Acme Shop Test") {
		t.Fatalf("output: %s", out)
	}

	c, _, errOut := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "duplicate", "acme-shop"}); err == nil || !strings.Contains(err.Error(), "what should the copy be called") {
		t.Fatalf("missing name: %v (%s)", err, errOut)
	}
}

// login checks the token before it writes it down, and the file is readable by its owner
// only - it holds a credential.
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

	// A certificate named relative to the current directory is stored with its full
	// path, so commands run elsewhere still find it.
	dir := t.TempDir()
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	tlsSrv.Close()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	c, _, _ = newTestCLI(t, srv, "")
	t.Setenv("ENVORYX_TOKEN", "")
	path = os.Getenv("ENVORYX_CLI_CONFIG")
	if err := c.run(context.Background(), []string{"login", "--token", "stq_good", "--ca-cert", "ca.crt"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	stored = cliConfig{}
	_ = json.Unmarshal(raw, &stored)
	if stored.CACert != filepath.Join(dir, "ca.crt") {
		t.Fatalf("stored CA certificate %q", stored.CACert)
	}

	// No token at all: the message must not send the user back to "envoryx login".
	c, _, _ = newTestCLI(t, srv, "")
	t.Setenv("ENVORYX_TOKEN", "")
	err = c.run(context.Background(), []string{"login"})
	if err == nil || strings.Contains(err.Error(), "run \"envoryx login\"") {
		t.Fatalf("login without a token: %v", err)
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

// Flag mistakes name the flag as it is typed and say what it expects, not the flag
// package's "flag provided but not defined: -nope".
func TestFlagErrorsAreReadable(t *testing.T) {
	for args, want := range map[string]string{
		"--nope":          "unknown flag --nope",
		"--url":           "--url needs a value",
		"--timeout=soon":  `--timeout expects a duration such as 30s, 10m or 2h, not "soon"`,
		"--limit=many":    `--limit expects a whole number, not "many"`,
		"--confirm=maybe": `--confirm takes no value (or =true, =false), not "maybe"`,
	} {
		c := &cli{}
		fs := c.newFlags("test")
		fs.Int("limit", 0, "")
		fs.Bool("confirm", false, "")
		_, err := parse(fs, []string{args})
		if !errors.Is(err, errCLIUsage) || err.Error() != want {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
}

// An explicit --help or "help" is an answer, not a usage error: it exits 0.
func TestHelpIsNotAUsageError(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"project", "--help"}, {"backup", "help"}, {"project", "list", "--help"}, {"whoami", "-h"}} {
		c := &cli{}
		if err := c.run(context.Background(), args); !errors.Is(err, errCLIHelp) || errors.Is(err, errCLIUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// --help on a command shows that command's entry and flags, not the whole usage text, and
// never runs it.
func TestCommandHelp(t *testing.T) {
	commands := []string{
		"project list", "project show", "project share", "project start", "project stop", "project restart",
		"project delete", "project logs", "project exec", "project run", "project branches", "project branch",
		"project deploy", "backup list", "backup create", "backup restore", "backup download", "backup delete",
		"backup offsite", "backup remote", "backup fetch", "backup recover", "instance backups", "instance backup",
		"instance download", "instance upload", "instance restore", "instance cancel-restore", "instance delete",
		"instance offsite", "instance remote", "instance fetch", "db snapshot", "db snapshots", "db restore",
		"db clone", "domain list", "domain add", "domain remove", "worker list", "worker presets", "worker add",
		"worker edit", "worker enable", "worker disable", "worker restart", "worker remove", "cron list", "cron add",
		"cron edit", "cron enable", "cron disable", "cron run", "cron runs", "cron remove", "git status", "git clone",
		"git pull", "git checkout", "diagnostics", "audit", "docker images", "docker prune", "docker orphans",
		"docker remove", "settings", "settings set", "login", "logout", "whoami",
		// Groups
		"project", "backup", "instance", "db", "domain", "worker", "cron", "git", "docker",
	}
	t.Setenv("ENVORYX_CLI_CONFIG", filepath.Join(t.TempDir(), "cli.json"))
	t.Setenv("ENVORYX_URL", "http://127.0.0.1:1") // nothing listens: a command that runs fails
	for _, cmd := range commands {
		for _, help := range []string{"--help", "-h"} {
			c := &cli{stdin: strings.NewReader("")}
			err := c.run(context.Background(), append(strings.Fields(cmd), help))
			var h *helpError
			if !errors.As(err, &h) {
				t.Errorf("%s %s: %v, want help", cmd, help, err)
				continue
			}
			text := commandHelp(h.cmd, h.flags)
			if !strings.HasPrefix(text, "Usage:\n  envoryx "+strings.Fields(cmd)[0]) || text == cliUsage {
				t.Errorf("%s %s: not its own help:\n%s", cmd, help, text)
			}
		}
	}
	c := &cli{}
	var h *helpError
	if err := c.run(context.Background(), []string{"help", "project", "logs"}); !errors.As(err, &h) {
		t.Fatalf("help project logs: %v", err)
	}
	text := commandHelp(h.cmd, h.flags)
	for _, want := range []string{"envoryx project logs <project>", "-f, --follow", "--tail N", "(default 200)"} {
		if !strings.Contains(text, want) {
			t.Errorf("help project logs lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "--insecure ") || strings.Contains(text, "project show") {
		t.Errorf("help project logs shows more than its own:\n%s", text)
	}
}

// The API's plain "does not exist" becomes what is missing, where the CLI knows it.
func TestNotFoundNamesWhatIsMissing(t *testing.T) {
	notFound := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "not_found", "message": "the requested resource does not exist"}})
	}
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects/{id}/database/snapshots":             notFound,
		"GET /api/v1/projects/{id}/database/snapshots":              notFound,
		"GET /api/v1/projects/{id}/backups/{b}/download":            notFound,
		"DELETE /api/v1/projects/{id}/backups/{b}":                  notFound,
		"POST /api/v1/projects/{id}/database/snapshots/{s}/restore": notFound,
	})
	for args, want := range map[string]string{
		"db snapshot acme-shop":                           "acme-shop has no database",
		"db restore blog nope --yes":                      "blog has no database",
		"backup download acme-shop 20261026-171442-x":     `acme-shop has no backup "20261026-171442-x" here (envoryx backup list acme-shop shows them; one on an offsite target needs envoryx backup fetch first)`,
		"backup delete acme-shop 20261026-171442-x --yes": `acme-shop has no backup "20261026-171442-x" here`,
	} {
		c, _, _ := newTestCLI(t, srv, "")
		err := c.run(context.Background(), strings.Fields(args))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
	c, _, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"docker", "remove", "bogus", "x", "--yes"}); !errors.Is(err, errCLIUsage) {
		t.Errorf("docker remove bogus: %v", err)
	}
}

// Piped action output loses the terminal's escape codes, also when one is split between
// two writes.
func TestANSIStripper(t *testing.T) {
	var b bytes.Buffer
	s := &ansiStripper{w: &b}
	for _, chunk := range []string{"\x1b[1mnpm\x1b[22m \x1b[33mwarn\x1b", "[39m deprecated\n\x1b[?25l", "\x1b]0;title\x07done\x1b(B\n"} {
		if _, err := s.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if b.String() != "npm warn deprecated\ndone\n" {
		t.Fatalf("stripped: %q", b.String())
	}
}

// The dev server's address goes through the project's proxy, port included.
func TestDevURL(t *testing.T) {
	p := projectSummary{Address: "https://lara.test:18904", Hostnames: []string{"lara.test"}, DevHostname: "lara-dev.test"}
	if got := p.devURL(); got != "https://lara-dev.test:18904" {
		t.Fatalf("devURL: %s", got)
	}
	p.Address = "http://192.168.1.5:21100" // a host port, not the proxy
	if got := p.devURL(); got != "https://lara-dev.test" {
		t.Fatalf("devURL without the proxy: %s", got)
	}
}

// git's progress keeps its last state, and a failure doesn't repeat the line its reason
// already quotes.
func TestGitOutput(t *testing.T) {
	raw := "From https://example.com/repo\nremote: Counting objects:   9% (1/11)   \rremote: Counting objects: 100% (11/11), done.\nUpdating 1a2b..3c4d\n"
	if got := gitTerminalOutput(raw); got != "From https://example.com/repo\nremote: Counting objects: 100% (11/11), done.\nUpdating 1a2b..3c4d" {
		t.Fatalf("gitTerminalOutput: %q", got)
	}
	body, _ := json.Marshal(map[string]any{"result": map[string]any{"output": "fatal: invalid reference: nope\n"}})
	err := gitFailure(&apiError{Status: 409, Code: "git_failed", Message: "conflict: git switch failed: fatal: invalid reference: nope", Body: body})
	if err.Error() != "conflict: git switch failed: fatal: invalid reference: nope" {
		t.Fatalf("gitFailure: %q", err)
	}
}

// A failed deploy's output loses its last "envoryx:" line, which the CLI prints anyway.
func TestWithoutErrorLine(t *testing.T) {
	for in, want := range map[string]string{
		"Already up to date.\n\nenvoryx: conflict: the deploy commands need an application container\n": "Already up to date.\n",
		"envoryx: conflict: no repository\n": "",
		"$ make deploy\nboom\n":              "$ make deploy\nboom",
	} {
		if got := withoutErrorLine(in); got != want {
			t.Errorf("withoutErrorLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// A flag right after a group belongs to its default command; a wrong one is a flag error.
func TestFlagAfterGroup(t *testing.T) {
	srv := newFakeServer(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/v1/settings": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"baseDomain": "test"}) },
	})
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"settings", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"baseDomain"`) {
		t.Errorf("settings --json printed %q", out.String())
	}
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"settings", "--bogus"}); !errors.Is(err, errCLIUsage) || err.Error() != "unknown flag --bogus" {
		t.Errorf("settings --bogus: %v", err)
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

// A snapshot is listed and taken with nothing but the project; restoring one and cloning a
// database over another ask for --yes first.
func TestDatabaseSnapshotsAndClone(t *testing.T) {
	var restored, cloned bool
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v1/projects/{id}/database/snapshots": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"snapshots": []map[string]any{
				{"id": "44444444-4444-4444-8444-444444444444", "kind": "database", "sizeBytes": 2048,
					"createdAt": "2026-09-01T10:00:00Z", "meta": map[string]any{"source": "snapshot", "note": "before the orders migration"}},
			}})
		},
		"POST /api/v1/projects/{id}/database/snapshots": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"snapshot": map[string]any{"id": "55555555-5555-4555-8555-555555555555", "kind": "database", "sizeBytes": 4096}})
		},
		"POST /api/v1/projects/{id}/database/snapshots/{snapshot}/restore": func(w http.ResponseWriter, r *http.Request) {
			restored = true
			writeJSON(w, map[string]any{"snapshot": map[string]any{"id": "44444444-4444-4444-8444-444444444444", "kind": "database"}})
		},
		"POST /api/v1/projects/{id}/database/clone": func(w http.ResponseWriter, r *http.Request) {
			cloned = true
			writeJSON(w, map[string]any{"clone": map[string]any{"source": "blog", "database": "acme_shop",
				"snapshot": map[string]any{"id": "66666666-6666-4666-8666-666666666666", "kind": "database"}}})
		},
	})
	shop := "/api/v1/projects/11111111-1111-4111-8111-111111111111"

	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "snapshots", "acme-shop"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATED", "2.0 KiB", "snapshot", "before the orders migration"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}

	c, out, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "snapshot", "acme-shop", "--note", "before deploy"}); err != nil {
		t.Fatal(err)
	}
	var taken struct {
		Note string `json:"note"`
	}
	_ = json.Unmarshal(srv.bodies[shop+"/database/snapshots"], &taken)
	if taken.Note != "before deploy" || !strings.Contains(out.String(), "4.0 KiB") {
		t.Fatalf("snapshot: %+v %s", taken, out.String())
	}

	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "restore", "acme-shop", "44444444-4444-4444-8444-444444444444"}); !errors.Is(err, errCLIUsage) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("restore without --yes: %v", err)
	}
	if restored {
		t.Fatal("the database was restored without confirmation")
	}
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "restore", "acme-shop", "44444444-4444-4444-8444-444444444444", "--yes"}); err != nil {
		t.Fatal(err)
	}
	var confirm struct {
		Confirm string `json:"confirm"`
	}
	_ = json.Unmarshal(srv.bodies[shop+"/database/snapshots/44444444-4444-4444-8444-444444444444/restore"], &confirm)
	if !restored || confirm.Confirm != "acme-shop" {
		t.Fatalf("restore body: %+v", confirm)
	}
	// The first block of the ID is enough; one that fits nothing says so.
	restored = false
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "restore", "acme-shop", "44444444", "--yes"}); err != nil || !restored {
		t.Fatalf("restore by prefix: %v", err)
	}
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "restore", "acme-shop", "abcdef", "--yes"}); err == nil || !strings.Contains(err.Error(), `starts with "abcdef"`) {
		t.Fatalf("unknown prefix: %v", err)
	}

	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "clone", "acme-shop", "--from", "blog"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("clone without --yes: %v", err)
	}
	if cloned {
		t.Fatal("the database was cloned without confirmation")
	}
	// The source may also stand as the second argument, and it is sent by id.
	c, out, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"db", "clone", "acme-shop", "blog", "--yes"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(srv.bodies[shop+"/database/clone"], &sent)
	if sent["source"] != "22222222-2222-4222-8222-222222222222" || sent["confirm"] != "acme-shop" || sent["snapshot"] != true {
		t.Fatalf("clone body: %v", sent)
	}
	if !strings.Contains(out.String(), "db restore acme-shop 66666666-6666-4666-8666-666666666666") {
		t.Fatalf("the way back must be printed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Copied the database of blog into the database of acme-shop.") {
		t.Fatalf("what was copied where:\n%s", out.String())
	}
}

func TestProjectLogsPassesTheFilterAndExports(t *testing.T) {
	var queries []string
	srv := newFakeServer(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/v1/projects/11111111-1111-4111-8111-111111111111/services/php/logs": func(w http.ResponseWriter, r *http.Request) {
			queries = append(queries, r.URL.RawQuery)
			writeJSON(w, map[string]any{"lines": []map[string]any{{"time": "2026-09-24T10:00:00Z", "stream": "stderr", "text": "PHP Fatal error: boom"}}})
		},
		"/api/v1/projects/11111111-1111-4111-8111-111111111111/services/php/logs/download": func(w http.ResponseWriter, r *http.Request) {
			queries = append(queries, r.URL.RawQuery)
			_, _ = w.Write([]byte("2026-09-24T10:00:00Z [stderr] PHP Fatal error: boom\n"))
		},
	})
	c, _, errOut := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "logs", "acme-shop", "--since", "6h", "--grep", "fatal", "--level", "error"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "PHP Fatal error: boom") || queries[0] != "level=error&q=fatal&since=6h&tail=200" {
		t.Fatalf("query %q, stderr %q", queries[0], errOut.String())
	}

	target := filepath.Join(t.TempDir(), "php.log")
	c, out, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "logs", "acme-shop", "--since", "7d", "-o", target}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if queries[1] != "since=7d" || !strings.Contains(string(data), "boom") || !strings.Contains(out.String(), "Wrote "+target) {
		t.Fatalf("export: query %q, file %q, out %q", queries[1], data, out.String())
	}
	if err := c.run(context.Background(), []string{"project", "logs", "acme-shop", "-f", "--until", "1h"}); err == nil {
		t.Fatal("--until with --follow must be refused")
	}
}

func TestProjectCreateMapsPostgresToTheCatalogueKey(t *testing.T) {
	srv := newFakeServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/projects": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"project": map[string]any{"id": "33333333-3333-4333-8333-333333333333", "name": "Shop", "slug": "shop"}})
		},
	})
	sent := func() databaseSpec {
		t.Helper()
		var body createRequest
		if err := json.Unmarshal(srv.bodies["/api/v1/projects"], &body); err != nil || body.Database == nil {
			t.Fatalf("request: %v %s", err, srv.bodies["/api/v1/projects"])
		}
		return *body.Database
	}

	c, _, _ := newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "create", "Shop", "--php", "8.4", "--database", "postgres:17"}); err != nil {
		t.Fatal(err)
	}
	if db := sent(); db.Type != "postgresql" || db.Version != "17" {
		t.Fatalf("--database postgres:17 sent %+v", db)
	}

	file := filepath.Join(t.TempDir(), "req.json")
	_ = os.WriteFile(file, []byte(`{"name":"Shop","database":{"type":"PG"}}`), 0o644)
	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "create", "--from-json", file}); err != nil {
		t.Fatal(err)
	}
	if db := sent(); db.Type != "postgresql" {
		t.Fatalf("--from-json sent %+v", db)
	}

	c, _, _ = newTestCLI(t, srv, "")
	if err := c.run(context.Background(), []string{"project", "create", "Shop", "--database", "mariadb"}); err != nil {
		t.Fatal(err)
	}
	if db := sent(); db.Type != "mariadb" {
		t.Fatalf("other types stay as they are: %+v", db)
	}
}

// The configuration holds a token: an older file readable by others is made the owner's
// own when it is saved again.
func TestSaveCLIConfigKeepsTheTokenPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "envoryx", "cli.json")
	t.Setenv("ENVORYX_CLI_CONFIG", path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"url":"https://old.example.net"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveCLIConfig(cliConfig{URL: "https://envoryx.example.net", Token: "stq_secret"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if cfg := loadCLIConfig(); cfg.Token != "stq_secret" || cfg.URL != "https://envoryx.example.net" {
		t.Fatalf("saved %+v", cfg)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}
