package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// A test suite is how a project runs its tests: found in the project directory (the
// PHPUnit binary Composer installed, the test scripts of package.json, a pytest
// configuration …), run like an action in the runtime container, and read back from the
// JUnit report the runner writes where it can.
type TestSuite struct {
	ID string `json:"id"`
	// Framework is pest, phpunit, npm, playwright, cypress, pytest, django or go.
	Framework string            `json:"framework"`
	Label     string            `json:"label"`
	Service   store.ServiceKind `json:"service"`
	// Cmd is the command as it runs without a filter (the report option left out).
	Cmd []string `json:"cmd"`
	// Report is true for runners that write a JUnit report: then the result names the
	// failed tests, otherwise only the exit code counts.
	Report bool `json:"report"`
	// FilterHint says what the filter narrows the run to ("" = no filter).
	FilterHint string `json:"filterHint,omitempty"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`

	build func(filter, report string) (argv, env []string)
}

// TestRunInfo is a run as the API shows it.
type TestRunInfo struct {
	ID         string     `json:"id"`
	Suite      string     `json:"suite"`
	Filter     string     `json:"filter,omitempty"`
	Status     string     `json:"status"`
	ExitCode   int        `json:"exitCode"`
	StartedAt  time.Time  `json:"startedAt"`
	DurationMs int64      `json:"durationMs"`
	Result     TestResult `json:"result"`
}

const (
	testRunsKept   = 50
	testOutputTail = 16 << 10
)

// TestSuites lists the suites the project has, available when their container runs and
// their runner is installed.
func (m *Manager) TestSuites(ctx context.Context, id string) ([]TestSuite, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	planner, err := m.planner()
	if err != nil {
		return nil, err
	}
	dir := planner.ProjectDir(view.Project)
	running := map[store.ServiceKind]bool{}
	for _, s := range view.Status.Services {
		running[s.Kind] = s.Running
	}
	suites := detectTestSuites(dir, view.Project)
	if suites == nil {
		suites = []TestSuite{}
	}
	for i := range suites {
		if suites[i].Available && !running[suites[i].Service] {
			suites[i].Available, suites[i].Reason = false, fmt.Sprintf("%s container is not running", suites[i].Service)
		}
	}
	return suites, nil
}

// detectTestSuites looks at the project directory for the runners of the runtimes the
// project has.
func detectTestSuites(dir string, p store.Project) []TestSuite {
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		return err == nil
	}
	read := func(rel string) []byte {
		b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if len(b) > 1<<20 {
			return nil
		}
		return b
	}
	has := func(kind store.ServiceKind) bool { s := p.Service(kind); return s != nil && s.Enabled }
	var out []TestSuite

	if has(store.ServicePHP) {
		var composer struct {
			Require    map[string]string `json:"require"`
			RequireDev map[string]string `json:"require-dev"`
		}
		_ = json.Unmarshal(read("composer.json"), &composer)
		wants := func(pkg string) bool { _, a := composer.Require[pkg]; _, b := composer.RequireDev[pkg]; return a || b }
		phpunitReport := func(bin ...string) func(string, string) ([]string, []string) {
			return func(filter, report string) ([]string, []string) {
				argv := append(append([]string{}, bin...), "--colors=always", "--log-junit", report)
				if filter != "" {
					argv = append(argv, "--filter", filter)
				}
				return argv, nil
			}
		}
		switch {
		case exists("vendor/bin/pest"):
			out = append(out, TestSuite{ID: "pest", Framework: "pest", Label: "Pest", Service: store.ServicePHP, Cmd: []string{"vendor/bin/pest"}, Report: true, FilterHint: "--filter", Available: true, build: phpunitReport("vendor/bin/pest")})
		case exists("vendor/bin/phpunit"):
			out = append(out, TestSuite{ID: "phpunit", Framework: "phpunit", Label: "PHPUnit", Service: store.ServicePHP, Cmd: []string{"vendor/bin/phpunit"}, Report: true, FilterHint: "--filter", Available: true, build: phpunitReport("vendor/bin/phpunit")})
		case exists("bin/phpunit"):
			// symfony/phpunit-bridge installs its own PHPUnit behind this script.
			out = append(out, TestSuite{ID: "phpunit", Framework: "phpunit", Label: "PHPUnit (Symfony)", Service: store.ServicePHP, Cmd: []string{"php", "bin/phpunit"}, Report: true, FilterHint: "--filter", Available: true, build: phpunitReport("php", "bin/phpunit")})
		case wants("pestphp/pest"):
			out = append(out, TestSuite{ID: "pest", Framework: "pest", Label: "Pest", Service: store.ServicePHP, Cmd: []string{"vendor/bin/pest"}, Report: true, Reason: "vendor/bin/pest not found – run composer install"})
		case wants("phpunit/phpunit") || wants("symfony/phpunit-bridge"):
			out = append(out, TestSuite{ID: "phpunit", Framework: "phpunit", Label: "PHPUnit", Service: store.ServicePHP, Cmd: []string{"vendor/bin/phpunit"}, Report: true, Reason: "vendor/bin/phpunit not found – run composer install"})
		}
	}

	if has(store.ServiceNode) && exists("package.json") {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal(read("package.json"), &pkg)
		pm := "npm"
		switch {
		case exists("pnpm-lock.yaml"):
			pm = "pnpm"
		case exists("yarn.lock"):
			pm = "yarn"
		}
		var names []string
		for name, script := range pkg.Scripts {
			// npm init's placeholder only says there are no tests.
			if (name == "test" || strings.HasPrefix(name, "test:")) && !strings.Contains(script, "no test specified") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) > 8 {
			names = names[:8]
		}
		for _, name := range names {
			base := []string{pm, "run", name}
			if name == "test" {
				base = []string{pm, "test"}
			}
			out = append(out, TestSuite{
				ID: "npm:" + name, Framework: "npm", Label: strings.Join(base, " "), Service: store.ServiceNode, Cmd: base,
				FilterHint: "passed on to the script", Available: true,
				build: func(filter, _ string) ([]string, []string) {
					argv := append([]string{}, base...)
					if filter != "" {
						if pm == "npm" {
							argv = append(argv, "--")
						}
						argv = append(argv, filter)
					}
					return argv, nil
				},
			})
		}
		if cfg := firstExisting(exists, "playwright.config.ts", "playwright.config.js", "playwright.config.mjs", "playwright.config.cjs"); cfg != "" {
			s := TestSuite{ID: "playwright", Framework: "playwright", Label: "Playwright", Service: store.ServiceNode, Cmd: []string{"npx", "playwright", "test"}, Report: true, FilterHint: "--grep", Available: true,
				build: func(filter, report string) ([]string, []string) {
					argv := []string{"npx", "playwright", "test", "--reporter=list,junit"}
					if filter != "" {
						argv = append(argv, "--grep", filter)
					}
					return argv, []string{"PLAYWRIGHT_JUNIT_OUTPUT_NAME=" + report}
				}}
			if !exists("node_modules/.bin/playwright") {
				s.Available, s.Reason = false, "node_modules/.bin/playwright not found – run npm install"
			}
			out = append(out, s)
		}
		if cfg := firstExisting(exists, "cypress.config.ts", "cypress.config.js", "cypress.config.mjs", "cypress.config.cjs"); cfg != "" {
			s := TestSuite{ID: "cypress", Framework: "cypress", Label: "Cypress", Service: store.ServiceNode, Cmd: []string{"npx", "cypress", "run"}, Report: true, FilterHint: "--spec", Available: true,
				build: func(filter, report string) ([]string, []string) {
					argv := []string{"npx", "cypress", "run", "--reporter", "junit", "--reporter-options", "mochaFile=" + report}
					if filter != "" {
						argv = append(argv, "--spec", filter)
					}
					return argv, nil
				}}
			if !exists("node_modules/.bin/cypress") {
				s.Available, s.Reason = false, "node_modules/.bin/cypress not found – run npm install"
			}
			out = append(out, s)
		}
	}

	if has(store.ServicePython) {
		pytest := exists("pytest.ini") || exists("conftest.py") || exists("tests/conftest.py") ||
			bytes.Contains(read("pyproject.toml"), []byte("[tool.pytest")) || bytes.Contains(read("setup.cfg"), []byte("[tool:pytest]")) ||
			bytes.Contains(read("requirements.txt"), []byte("pytest")) || bytes.Contains(read("requirements-dev.txt"), []byte("pytest"))
		if pytest {
			out = append(out, TestSuite{ID: "pytest", Framework: "pytest", Label: "pytest", Service: store.ServicePython, Cmd: []string{"python", "-m", "pytest"}, Report: true, FilterHint: "-k", Available: true,
				build: func(filter, report string) ([]string, []string) {
					argv := []string{"python", "-m", "pytest", "--color=yes", "--junitxml=" + report}
					if filter != "" {
						argv = append(argv, "-k", filter)
					}
					return argv, nil
				}})
		}
		if exists("manage.py") {
			out = append(out, TestSuite{ID: "django", Framework: "django", Label: "manage.py test", Service: store.ServicePython, Cmd: []string{"python", "manage.py", "test"}, FilterHint: "test label", Available: true,
				build: func(filter, _ string) ([]string, []string) {
					argv := []string{"python", "manage.py", "test", "--no-input"}
					if filter != "" {
						argv = append(argv, filter)
					}
					return argv, nil
				}})
		}
	}
	if has(store.ServiceGo) && exists("go.mod") {
		// gotestsum runs go test and writes the JUnit report; the filter is go test's -run.
		out = append(out, TestSuite{ID: "go", Framework: "go", Label: "go test", Service: store.ServiceGo, Cmd: []string{"go", "test", "./..."}, Report: true, FilterHint: "-run", Available: true,
			build: func(filter, report string) ([]string, []string) {
				argv := []string{"gotestsum", "--format", "testname", "--junitfile", report, "--", "./..."}
				if filter != "" {
					argv = append(argv, "-run", filter)
				}
				return argv, nil
			}})
	}
	return out
}

func firstExisting(exists func(string) bool, names ...string) string {
	for _, n := range names {
		if exists(n) {
			return n
		}
	}
	return ""
}

// validateTestFilter keeps a filter a filter: it is handed to the runner as one argument
// of its own, never through a shell, and may not start like an option.
func validateTestFilter(f string) error {
	if f == "" {
		return nil
	}
	if len(f) > 200 || strings.HasPrefix(f, "-") || strings.ContainsFunc(f, unicode.IsControl) {
		return fmt.Errorf("%w: the filter is at most 200 characters, without line breaks, and does not start with a dash", validate.ErrInvalid)
	}
	return nil
}

// TestSession is a running test suite. Finish records it once the process has exited.
type TestSession struct {
	Term    docker.Terminal
	Suite   TestSuite
	Filter  string
	Release func()

	runID       string
	projectID   string
	containerID string
	report      string
	started     time.Time
}

// Argv is the command that runs, report option included.
func (s *TestSession) Argv() []string {
	argv, _ := s.Suite.build(s.Filter, s.report)
	return argv
}

// RunTests starts a suite in its runtime container and returns the live session (output
// only). The project lock is held until Release, like an action.
func (m *Manager) RunTests(ctx context.Context, id, suiteID, filter string, cols, rows uint) (*TestSession, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	if err := validateTestFilter(filter); err != nil {
		return nil, err
	}
	unlock, err := m.lock(id)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			unlock()
		}
	}()
	suites, err := m.TestSuites(ctx, id)
	if err != nil {
		return nil, err
	}
	var suite *TestSuite
	for i := range suites {
		if suites[i].ID == suiteID {
			suite = &suites[i]
		}
	}
	if suite == nil {
		return nil, fmt.Errorf("%w: the project has no test suite %q", store.ErrNotFound, suiteID)
	}
	if !suite.Available {
		return nil, fmt.Errorf("%w: %s", ErrConflict, suite.Reason)
	}
	if filter != "" && suite.FilterHint == "" {
		return nil, fmt.Errorf("%w: %s takes no filter", validate.ErrInvalid, suite.Label)
	}
	c, err := m.ServiceContainer(ctx, id, suite.Service)
	if err != nil {
		return nil, err
	}
	paths, err := m.paths()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	if cols == 0 || cols > 500 {
		cols = 120
	}
	if rows == 0 || rows > 200 {
		rows = 40
	}
	s := &TestSession{Suite: *suite, Filter: filter, runID: store.NewID(), projectID: id, containerID: c.ID, started: time.Now()}
	s.report = "/tmp/envoryx-tests-" + s.runID + ".xml"
	argv, env := suite.build(filter, s.report)
	opts := docker.TerminalOptions{
		Cmd:        argv,
		Cols:       cols,
		Rows:       rows,
		WorkingDir: appMountTarget,
		User:       fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
		Env:        append(append([]string{"TERM=xterm-256color", "COLORTERM=truecolor", "FORCE_COLOR=1", "LANG=C.UTF-8", "CI=1"}, toolEnv...), env...),
	}
	m.ensurePasswdEntry(ctx, c.ID, c.Name, opts.User)
	term, err := m.engine.OpenTerminal(ctx, c.ID, opts)
	if err != nil {
		return nil, err
	}
	s.Term, s.Release, ok = term, unlock, true
	m.audit.Log(ctx, audit.ActionTestRun, "project", id, map[string]any{"suite": suite.ID, "filter": filter})
	return s, nil
}

var ansiCodes = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\a]*\a|\r`)

// FinishTestRun reads the report of a finished run, records the run and returns it. output is
// what the run printed (its end is kept); cancelled marks a run stopped from the UI.
func (m *Manager) FinishTestRun(ctx context.Context, s *TestSession, exitCode int, cancelled bool, output []byte) (TestRunInfo, error) {
	ctx = context.WithoutCancel(ctx)
	res := TestResult{Failed: []TestCase{}}
	if s.Suite.Report {
		out, err := m.engine.Exec(ctx, s.containerID, []string{"cat", s.report}, nil)
		if err == nil && out.ExitCode == 0 && strings.TrimSpace(out.Stdout) != "" {
			if parsed, perr := parseJUnit(strings.NewReader(out.Stdout), appMountTarget); perr == nil {
				res = parsed
			} else {
				m.log.Warn("test report unreadable", "project", s.projectID, "suite", s.Suite.ID, "err", perr)
			}
		}
		_, _ = m.engine.Exec(ctx, s.containerID, []string{"rm", "-f", s.report}, nil)
	}
	text := ansiCodes.ReplaceAllString(string(output), "")
	if len(text) > testOutputTail {
		text = "…" + text[len(text)-testOutputTail:]
	}
	res.Output = text
	status := store.TestPassed
	switch {
	case cancelled:
		status = store.TestCancelled
	case exitCode != 0 || res.Failures+res.Errors > 0:
		status = store.TestFailed
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return TestRunInfo{}, err
	}
	run := store.TestRun{ID: s.runID, ProjectID: s.projectID, Suite: s.Suite.ID, Filter: s.Filter, Status: status, ExitCode: exitCode, StartedAt: s.started.UTC(), Duration: time.Since(s.started), Result: raw}
	if err := m.store.TestRuns.Add(ctx, &run, testRunsKept); err != nil {
		return TestRunInfo{}, err
	}
	return testRunInfo(run), nil
}

func testRunInfo(r store.TestRun) TestRunInfo {
	info := TestRunInfo{ID: r.ID, Suite: r.Suite, Filter: r.Filter, Status: r.Status, ExitCode: r.ExitCode, StartedAt: r.StartedAt, DurationMs: r.Duration.Milliseconds()}
	_ = json.Unmarshal(r.Result, &info.Result)
	if info.Result.Failed == nil {
		info.Result.Failed = []TestCase{}
	}
	return info
}

// TestRuns returns the newest runs of a project; the list leaves out the output, which
// TestRun has.
func (m *Manager) TestRuns(ctx context.Context, id string, limit int) ([]TestRunInfo, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	runs, err := m.store.TestRuns.List(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	out := make([]TestRunInfo, 0, len(runs))
	for _, r := range runs {
		info := testRunInfo(r)
		info.Result.Output = ""
		out = append(out, info)
	}
	return out, nil
}

// TestRun returns one run with its output.
func (m *Manager) TestRun(ctx context.Context, id, runID string) (TestRunInfo, error) {
	if validate.UUID(id) != nil || validate.UUID(runID) != nil {
		return TestRunInfo{}, ErrNotFound
	}
	r, err := m.store.TestRuns.Get(ctx, id, runID)
	if err != nil {
		return TestRunInfo{}, err
	}
	return testRunInfo(r), nil
}
