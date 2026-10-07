package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func writeProjectFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func suiteIDs(s []TestSuite) []string {
	var out []string
	for _, x := range s {
		out = append(out, x.ID)
	}
	return out
}

func TestDetectTestSuites(t *testing.T) {
	all := store.Project{Services: []store.ProjectService{
		{Kind: store.ServicePHP, Enabled: true}, {Kind: store.ServiceNode, Enabled: true}, {Kind: store.ServicePython, Enabled: true},
	}}
	dir := t.TempDir()
	writeProjectFiles(t, dir, map[string]string{
		"vendor/bin/pest":           "",
		"vendor/bin/phpunit":        "",
		"package.json":              `{"scripts": {"test": "vitest run", "test:e2e": "playwright test", "dev": "vite"}}`,
		"playwright.config.ts":      "",
		"cypress.config.js":         "",
		"node_modules/.bin/cypress": "",
		"pytest.ini":                "",
		"manage.py":                 "",
		"yarn.lock":                 "",
	})
	suites := detectTestSuites(dir, all)
	if got := suiteIDs(suites); !slices.Equal(got, []string{"pest", "npm:test", "npm:test:e2e", "playwright", "cypress", "pytest", "django"}) {
		t.Fatalf("suites: %v", got)
	}
	byID := map[string]TestSuite{}
	for _, s := range suites {
		byID[s.ID] = s
	}
	// Pest wins over the PHPUnit it brings along; yarn is used where its lock file is.
	argv, _ := byID["pest"].build("CartTest", "/tmp/r.xml")
	if strings.Join(argv, " ") != "vendor/bin/pest --colors=always --log-junit /tmp/r.xml --filter CartTest" {
		t.Fatalf("pest: %v", argv)
	}
	if argv, _ := byID["npm:test:e2e"].build("login", ""); strings.Join(argv, " ") != "yarn run test:e2e login" {
		t.Fatalf("yarn script: %v", argv)
	}
	if pw := byID["playwright"]; pw.Available || !strings.Contains(pw.Reason, "npm install") {
		t.Fatalf("playwright without node_modules: %+v", pw)
	}
	argv, env := byID["playwright"].build("checkout", "/tmp/r.xml")
	if !slices.Contains(argv, "--grep") || !slices.Equal(env, []string{"PLAYWRIGHT_JUNIT_OUTPUT_NAME=/tmp/r.xml"}) {
		t.Fatalf("playwright: %v %v", argv, env)
	}
	if argv, _ := byID["pytest"].build("slow", "/tmp/r.xml"); strings.Join(argv, " ") != "python -m pytest --color=yes --junitxml=/tmp/r.xml -k slow" {
		t.Fatalf("pytest: %v", argv)
	}

	// npm run needs "--" before what goes to the script; npm init's placeholder is no test.
	dir = t.TempDir()
	writeProjectFiles(t, dir, map[string]string{"package.json": `{"scripts": {"test": "echo \"Error: no test specified\" && exit 1", "test:unit": "vitest"}}`})
	suites = detectTestSuites(dir, store.Project{Services: []store.ProjectService{{Kind: store.ServiceNode, Enabled: true}}})
	if len(suites) != 1 || suites[0].ID != "npm:test:unit" {
		t.Fatalf("suites: %v", suiteIDs(suites))
	}
	if argv, _ := suites[0].build("cart", ""); strings.Join(argv, " ") != "npm run test:unit -- cart" {
		t.Fatalf("npm: %v", argv)
	}

	// PHPUnit asked for in composer.json but not installed: listed, with the way out.
	dir = t.TempDir()
	writeProjectFiles(t, dir, map[string]string{"composer.json": `{"require-dev": {"phpunit/phpunit": "^11"}}`})
	suites = detectTestSuites(dir, store.Project{Services: []store.ProjectService{{Kind: store.ServicePHP, Enabled: true}}})
	if len(suites) != 1 || suites[0].Available || !strings.Contains(suites[0].Reason, "composer install") {
		t.Fatalf("suites: %+v", suites)
	}
	// Runtimes the project does not have contribute nothing.
	if s := detectTestSuites(dir, store.Project{}); len(s) != 0 {
		t.Fatalf("without runtimes: %v", suiteIDs(s))
	}
}

func TestTestFilter(t *testing.T) {
	for _, bad := range []string{"--bootstrap=/tmp/x.php", "-k", "a\nb", strings.Repeat("x", 201)} {
		if err := validateTestFilter(bad); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%q must be refused", bad)
		}
	}
	for _, good := range []string{"", "CartTest", "tests/Feature/CheckoutTest.php", "test_total and not slow", "Cart::total"} {
		if err := validateTestFilter(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestRunTestsRecordsTheReport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Shop", true)
	req.CreateStarter = false
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	writeProjectFiles(t, filepath.Join(e.projDir, "shop"), map[string]string{"vendor/bin/phpunit": ""})
	e.engine.ExecHandler = func(container string, cmd []string, env []string) (docker.ExecResult, error) {
		if cmd[0] == "cat" && strings.HasPrefix(cmd[1], "/tmp/envoryx-tests-") {
			return docker.ExecResult{Stdout: phpunitReport}, nil
		}
		return docker.ExecResult{}, nil
	}

	suites, err := e.m.TestSuites(ctx, id)
	if err != nil || len(suites) != 1 || !suites[0].Available {
		t.Fatalf("suites: %+v %v", suites, err)
	}
	if _, err := e.m.RunTests(ctx, id, "phpunit", "-x", 0, 0); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("a filter like an option: %v", err)
	}
	if _, err := e.m.RunTests(ctx, id, "pytest", "", 0, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a suite the project lacks: %v", err)
	}
	s, err := e.m.RunTests(ctx, id, "phpunit", "CartTest", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	term := e.engine.Terminals()[len(e.engine.Terminals())-1]
	if term.Container != "envoryx-shop-php" || term.Opts.Cmd[0] != "vendor/bin/phpunit" || !slices.Contains(term.Opts.Cmd, "--log-junit") || term.Opts.Cmd[len(term.Opts.Cmd)-1] != "CartTest" || term.Opts.WorkingDir != appMountTarget {
		t.Fatalf("terminal: %+v", term)
	}
	// The project is busy while the tests run.
	if _, err := e.m.Stop(ctx, id); !errors.Is(err, ErrBusy) {
		t.Fatalf("stop during a run: %v", err)
	}
	run, err := e.m.FinishTestRun(ctx, s, 1, false, []byte("\x1b[31mFAILURES!\x1b[0m\r\nTests: 3, Failures: 1, Errors: 1.\n"))
	s.Release()
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.TestFailed || run.Result.Tests != 3 || run.Result.Failures != 1 || len(run.Result.Failed) != 2 || run.Filter != "CartTest" {
		t.Fatalf("run: %+v", run)
	}
	if strings.Contains(run.Result.Output, "\x1b") || !strings.Contains(run.Result.Output, "FAILURES!") {
		t.Fatalf("output: %q", run.Result.Output)
	}
	if !slices.Contains(e.engine.Execs, "envoryx-shop-php: rm -f "+s.report) {
		t.Fatalf("the report must be removed: %v", e.engine.Execs)
	}
	runs, err := e.m.TestRuns(ctx, id, 10)
	if err != nil || len(runs) != 1 || runs[0].Result.Output != "" || runs[0].Result.Failures != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	full, err := e.m.TestRun(ctx, id, run.ID)
	if err != nil || full.Result.Output == "" {
		t.Fatalf("run: %+v %v", full, err)
	}

	// Without a report the exit code decides; a cancelled run is marked as such.
	s, err = e.m.RunTests(ctx, id, "phpunit", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.engine.ExecHandler = nil
	run, _ = e.m.FinishTestRun(ctx, s, 130, true, nil)
	s.Release()
	if run.Status != store.TestCancelled || run.Result.Report {
		t.Fatalf("cancelled run: %+v", run)
	}
}

// A PHP test run must never reach the development database: Laravel's RefreshDatabase
// runs migrate:fresh. DB_DATABASE, DB_URL and DATABASE_URL gain _test (before a query),
// except DATABASE_URL where Doctrine adds the suffix itself and DB_DATABASE of SQLite.
func TestPHPTestScript(t *testing.T) {
	run := func(keep string, env ...string) string {
		t.Helper()
		cmd := exec.Command("sh", "-c", phpTestScript, "envoryx-phpunit", keep, "sh", "-c", `printf '%s|%s|%s' "$DB_DATABASE" "${DB_URL-unset}" "$DATABASE_URL"`)
		cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := run("0", "DB_CONNECTION=pgsql", "DB_DATABASE=shop", "DATABASE_URL=pgsql://u:p@database:5432/shop"); got != "shop_test|unset|pgsql://u:p@database:5432/shop_test" {
		t.Fatalf("laravel: %q", got)
	}
	if got := run("0", "DB_DATABASE=shop", "DB_URL=mysql://u:p@database:3306/shop?charset=utf8mb4", "DATABASE_URL=mysql://u:p@database:3306/shop?serverVersion=8.4&charset=utf8mb4"); got != "shop_test|mysql://u:p@database:3306/shop_test?charset=utf8mb4|mysql://u:p@database:3306/shop_test?serverVersion=8.4&charset=utf8mb4" {
		t.Fatalf("urls with a query: %q", got)
	}
	if got := run("1", "DB_DATABASE=shop", "DATABASE_URL=mysql://u:p@database:3306/shop"); got != "shop_test|unset|mysql://u:p@database:3306/shop" {
		t.Fatalf("doctrine suffix: %q", got)
	}
	if got := run("0", "DB_CONNECTION=sqlite", "DB_DATABASE=database/database.sqlite"); got != "database/database.sqlite|unset|" {
		t.Fatalf("sqlite: %q", got)
	}
}

// With an SQL database PHPUnit runs behind phpTestScript, which keeps DATABASE_URL when
// Symfony's Doctrine config adds _test itself, and the run creates <database>_test first.
func TestPHPTestsUseTheTestDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var sql []string
	e.engine.ExecHandler = func(container string, cmd []string, env []string) (docker.ExecResult, error) {
		if container == "envoryx-shop-database" {
			sql = append(sql, cmd[len(cmd)-1])
			return docker.ExecResult{Stdout: "shop\n"}, nil
		}
		return docker.ExecResult{}, nil
	}
	req := phpRequest("Shop", true)
	req.CreateStarter = false
	req.Database = &DatabaseRequest{Type: "mariadb", Version: "11"}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	writeProjectFiles(t, filepath.Join(e.projDir, "shop"), map[string]string{
		"bin/phpunit":                   "",
		"config/packages/doctrine.yaml": "when@test:\n    doctrine:\n        dbal:\n            dbname_suffix: '_test%env(default::TEST_TOKEN)%'\n",
	})
	s, err := e.m.RunTests(ctx, id, "phpunit", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Release()
	term := e.engine.Terminals()[len(e.engine.Terminals())-1]
	if term.Opts.Cmd[2] != phpTestScript || strings.Join(term.Opts.Cmd[4:7], " ") != "1 php bin/phpunit" {
		t.Fatalf("argv: %q", term.Opts.Cmd)
	}
	if !slices.ContainsFunc(sql, func(s string) bool { return strings.HasPrefix(s, "CREATE DATABASE `shop_test`") }) {
		t.Fatalf("the test database must be created: %q", sql)
	}

	// Laravel: no Doctrine suffix, so DATABASE_URL is redirected too.
	_ = os.Remove(filepath.Join(e.projDir, "shop", "config", "packages", "doctrine.yaml"))
	suites, _ := e.m.TestSuites(ctx, id)
	if argv, _ := suites[0].build("", "/tmp/r.xml"); argv[4] != "0" {
		t.Fatalf("laravel argv: %q", argv)
	}
}

// Django's test runner and pytest-django create test_<database>, which MySQL's project
// login may not: Envoryx creates it with a grant first.
func TestDjangoTestDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var sql []string
	e.engine.ExecHandler = func(container string, cmd []string, env []string) (docker.ExecResult, error) {
		if container == "envoryx-api-database" {
			sql = append(sql, cmd[len(cmd)-1])
			return docker.ExecResult{Stdout: "api\n"}, nil
		}
		return docker.ExecResult{}, nil
	}
	req := pythonRequest("Api", true)
	req.Database = &DatabaseRequest{Type: "mysql", Version: "8.4"}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFiles(t, filepath.Join(e.projDir, "api"), map[string]string{"manage.py": "", "pytest.ini": ""})
	for _, suite := range []string{"django", "pytest"} {
		sql = nil
		s, err := e.m.RunTests(ctx, view.Project.ID, suite, "", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		s.Release()
		if !slices.ContainsFunc(sql, func(s string) bool { return strings.Contains(s, "GRANT ALL PRIVILEGES ON `test_api`.* TO 'api'") }) {
			t.Fatalf("%s: test_api must be created with a grant: %q", suite, sql)
		}
	}
}
