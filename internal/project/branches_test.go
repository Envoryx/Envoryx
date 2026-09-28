package project

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestBranchSettingsAndNames(t *testing.T) {
	b, err := normalizeBranchSettings(store.BranchSettings{Patterns: []string{" feature/* ", "", "feature/*", "fix-?"}, Deploy: []string{" composer install ", ""}})
	if err != nil || !slices.Equal(b.Patterns, []string{"feature/*", "fix-?"}) || !slices.Equal(b.Deploy, []string{"composer install"}) {
		t.Fatalf("normalized: %+v %v", b, err)
	}
	for _, bad := range []store.BranchSettings{
		{Patterns: []string{"feature/[a"}},
		{Patterns: []string{"$(id)"}},
		{Deploy: []string{"composer install\nrm -rf /"}},
		{PollMinutes: 2000},
		{IdleStopDays: -1},
		{MaxEnvironments: 51},
	} {
		if _, err := normalizeBranchSettings(bad); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%+v must be refused, got %v", bad, err)
		}
	}
	if !branchMatches([]string{"feature/*"}, "feature/login") || branchMatches([]string{"feature/*"}, "feature/a/b") || branchMatches(nil, "main") {
		t.Fatal("branch patterns match like path.Match")
	}
	taken := map[string]bool{"shop-feature-login": true}
	if s, _ := branchSlug("shop", "feature/login", func(s string) bool { return taken[s] }); s != "shop-feature-login-2" {
		t.Fatalf("taken slug gets a number: %s", s)
	}
	long := "a-rather-long-project-name"
	if s, err := branchSlug(long, "feature/an-even-longer-branch-name", func(string) bool { return false }); err != nil || len(s) > 40 || !strings.HasPrefix(s, long) || strings.HasSuffix(s, "-") {
		t.Fatalf("long slug: %q %v", s, err)
	}
}

// gitRemote scripts the git one-shots: ls-remote answers with heads, rev-parse with the
// commit of the branch the environment is on, everything else succeeds. It records the
// commands with the directory they ran in.
type gitRemote struct {
	mu    sync.Mutex
	heads map[string]string
	calls []string
}

func (g *gitRemote) handle(spec docker.ContainerSpec) (docker.ExecResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	args := spec.Cmd[3:] // git -C /var/www/html …
	dir := filepath.Base(spec.Mounts[0].Source)
	g.calls = append(g.calls, dir+": "+strings.Join(args, " "))
	switch args[0] {
	case "ls-remote":
		var out strings.Builder
		for name, commit := range g.heads {
			out.WriteString(commit + "\trefs/heads/" + name + "\n")
		}
		return docker.ExecResult{Stdout: out.String()}, nil
	case "rev-parse":
		return docker.ExecResult{Stdout: "c0ffee\n"}, nil
	}
	return docker.ExecResult{}, nil
}

func (g *gitRemote) ran(prefix string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.ContainsFunc(g.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// branchParent creates a started PHP project with a database, a repository and a git
// checkout, deploying with two commands.
func branchParent(t *testing.T, e *env) store.Project {
	t.Helper()
	ctx := context.Background()
	v, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetGit(ctx, v.Project.ID, GitRequest{URL: "https://git.example.com/shop.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.projDir, "shop", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetBranchSettings(ctx, v.Project.ID, store.BranchSettings{Deploy: []string{"composer install", "php artisan migrate --force"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.m.loadProject(ctx, v.Project.ID)
	return p
}

func TestCreateBranchEnvironment(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	git := &gitRemote{}
	e.engine.OneShotHandler = git.handle
	parent := branchParent(t, e)

	v, err := e.m.CreateBranchEnvironment(ctx, parent.ID, "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	env := v.Project
	if env.Slug != "shop-feature-login" || env.Name != "Shop (feature/login)" || env.ParentID != parent.ID || env.Git.Branch != "feature/login" || env.Git.URL != parent.Git.URL || env.DesiredState != store.DesiredRunning {
		t.Fatalf("environment: %+v", env)
	}
	// The copy is switched to the branch, keeping the parent's ignored files.
	for _, want := range []string{
		"shop-feature-login: fetch origin +refs/heads/feature/login:refs/remotes/origin/feature/login",
		"shop-feature-login: checkout -f -B feature/login origin/feature/login",
		"shop-feature-login: clean -fd",
	} {
		if !git.ran(want) {
			t.Fatalf("missing %q in %q", want, git.calls)
		}
	}
	// The first deploy runs the commands without a pull and records the commit.
	if git.ran("shop-feature-login: pull") {
		t.Fatal("the first deploy has nothing to pull")
	}
	for _, want := range []string{"envoryx-shop-feature-login-php: sh -c composer install", "envoryx-shop-feature-login-php: sh -c php artisan migrate --force"} {
		if !slices.Contains(e.engine.Execs, want) {
			t.Fatalf("missing exec %q in %q", want, e.engine.Execs)
		}
	}
	env, _ = e.m.loadProject(ctx, env.ID)
	if env.BranchState.Commit != "c0ffee" || env.BranchState.DeployStatus != store.DeploySucceeded || !strings.Contains(env.BranchState.DeployOutput, "$ composer install") {
		t.Fatalf("branch state: %+v", env.BranchState)
	}
	envs, err := e.m.BranchEnvironments(ctx, parent.ID)
	if err != nil || len(envs) != 1 || envs[0].Project.ID != env.ID {
		t.Fatalf("environments: %v %v", envs, err)
	}

	if _, err := e.m.CreateBranchEnvironment(ctx, parent.ID, "feature/login"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second environment of the same branch: %v", err)
	}
	if _, err := e.m.CreateBranchEnvironment(ctx, env.ID, "feature/x"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("an environment of an environment: %v", err)
	}
	if _, err := e.m.CreateBranchEnvironment(ctx, parent.ID, "--upload-pack=x"); err == nil {
		t.Fatal("an option as branch name must be refused")
	}
	if err := e.m.Delete(ctx, parent.ID, DeleteOptions{Confirm: parent.Slug}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "shop-feature-login") {
		t.Fatalf("the parent goes after its environments: %v", err)
	}

	// A failing command fails the deploy and keeps its output.
	e.engine.StreamHandler = func(container string, cmd []string, _ []string, _ []byte) (string, int, error) {
		if strings.Contains(strings.Join(cmd, " "), "migrate") {
			return "SQLSTATE[42S01]: table exists\n", 1, nil
		}
		return "", 0, nil
	}
	if _, err := e.m.Deploy(ctx, env.ID, true); err == nil {
		t.Fatal("the failed command must fail the deploy")
	}
	env, _ = e.m.loadProject(ctx, env.ID)
	if env.BranchState.DeployStatus != store.DeployFailed || !strings.Contains(env.BranchState.DeployOutput, "table exists") || !git.ran("shop-feature-login: pull --ff-only origin feature/login") {
		t.Fatalf("failed deploy: %+v", env.BranchState)
	}
	if _, err := e.m.Deploy(ctx, parent.ID, true); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("only a branch environment deploys: %v", err)
	}
}

func TestBranchSchedulerFollowsTheRepository(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	git := &gitRemote{}
	e.engine.OneShotHandler = git.handle
	parent := branchParent(t, e)
	old, err := e.m.CreateBranchEnvironment(ctx, parent.ID, "feature/old")
	if err != nil {
		t.Fatal(err)
	}
	login, err := e.m.CreateBranchEnvironment(ctx, parent.ID, "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetBranchSettings(ctx, parent.ID, store.BranchSettings{Watch: true, Patterns: []string{"feature/*"}, Deploy: []string{"composer install"}}); err != nil {
		t.Fatal(err)
	}
	// feature/old is gone, feature/login moved on, feature/new and main are new; main is the
	// parent's own branch and gets no environment.
	git.heads = map[string]string{"main": "aaa", "feature/login": "d00d", "feature/new": "beef"}
	log := slog.New(slog.DiscardHandler)
	e.m.runBranchPass(ctx, time.Now(), log)

	if _, err := e.m.store.Projects.Get(ctx, old.Project.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the environment of a deleted branch must go: %v", err)
	}
	if !git.ran("shop-feature-login: pull --ff-only origin feature/login") {
		t.Fatalf("the moved branch must be pulled: %q", git.calls)
	}
	envs, _ := e.m.BranchEnvironments(ctx, parent.ID)
	var branches []string
	for _, v := range envs {
		branches = append(branches, v.Project.Git.Branch)
	}
	slices.Sort(branches)
	if !slices.Equal(branches, []string{"feature/login", "feature/new"}) {
		t.Fatalf("environments after the poll: %v", branches)
	}
	if poll, ok := e.m.LastBranchPoll(parent.ID); !ok || poll.Error != "" {
		t.Fatalf("poll: %+v %v", poll, ok)
	}
	_ = login

	// The next pass within the interval leaves the repository alone.
	calls := len(git.calls)
	e.m.runBranchPass(ctx, time.Now(), log)
	if len(git.calls) != calls {
		t.Fatalf("polled again within the interval: %q", git.calls[calls:])
	}
}

func TestIdleBranchEnvironmentsStop(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.OneShotHandler = (&gitRemote{}).handle
	parent := branchParent(t, e)
	v, err := e.m.CreateBranchEnvironment(ctx, parent.ID, "feature/idle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetBranchSettings(ctx, parent.ID, store.BranchSettings{IdleStopDays: 2}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	// A visit a day later keeps it running on the third day …
	e.m.runBranchPass(ctx, time.Now(), log)
	e.m.Visited(v.Project.ID)
	e.m.runBranchPass(ctx, time.Now().Add(36*time.Hour), log)
	p, _ := e.m.loadProject(ctx, v.Project.ID)
	if p.DesiredState != store.DesiredRunning || p.BranchState.LastAccess.IsZero() {
		t.Fatalf("visited environment: %s %+v", p.DesiredState, p.BranchState)
	}
	// … and two days without one stop it; the parent is never stopped.
	e.m.runBranchPass(ctx, time.Now().Add(72*time.Hour), log)
	p, _ = e.m.loadProject(ctx, v.Project.ID)
	if p.DesiredState != store.DesiredStopped {
		t.Fatalf("idle environment still %s", p.DesiredState)
	}
	if par, _ := e.m.loadProject(ctx, parent.ID); par.DesiredState != store.DesiredRunning {
		t.Fatal("the parent must keep running")
	}
}

func TestBranchSettingsInTheManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.OneShotHandler = (&gitRemote{}).handle
	parent := branchParent(t, e)
	if _, err := e.m.SetBranchSettings(ctx, parent.ID, store.BranchSettings{Watch: true, Patterns: []string{"feature/*"}, IdleStopDays: 7, Deploy: []string{"composer install"}}); err != nil {
		t.Fatal(err)
	}
	mf, err := e.m.ExportManifest(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b := mf.Branches; b == nil || !b.Watch || !slices.Equal(b.Patterns, []string{"feature/*"}) || b.IdleStopDays != 7 || !slices.Equal(b.Deploy, []string{"composer install"}) {
		t.Fatalf("exported: %+v", mf.Branches)
	}
	// The file without the section removes the settings only when pruning.
	mf.Branches = nil
	plan, err := e.m.PlanManifest(ctx, parent.ID, mf, ManifestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Changes, func(c ManifestChange) bool {
		return c.Section == "branches" && c.Action == "remove" && c.Skipped == "prune"
	}) {
		t.Fatalf("plan: %+v", plan.Changes)
	}
	req := manifestRequest(mf, "Copy")
	if !req.Branches.Empty() {
		t.Fatalf("no section, no settings: %+v", req.Branches)
	}
}
