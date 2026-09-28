package project

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// A branch environment is a copy of a project (its parent) on one branch of the parent's
// repository: its own containers, host name and database, the parent's files as the
// starting point (so .env, vendor/ and node_modules/ come along), the database and bucket
// copied, and the working tree switched to the branch. The parent's BranchSettings say
// which commands deploy it and, with Watch on, the branch scheduler keeps the
// environments in step with the repository: a push is pulled and deployed, a deleted
// branch takes its environment with it, and a new branch matching the patterns gets one.

// Defaults and bounds of the branch settings.
const (
	defaultBranchPollMinutes = 5
	defaultMaxBranchEnvs     = 5
	maxBranchEnvsLimit       = 50
	maxBranchPatterns        = 20
	maxDeployCommands        = 20
	deployOutputTail         = 16 << 10
)

// BranchSettingsRequest is the validated intent to change a project's branch settings.
type BranchSettingsRequest = store.BranchSettings

// RemoteBranch is a branch of the parent's repository and the environment it has, if any.
type RemoteBranch struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
	// Environment is the slug of the branch environment, empty when it has none.
	Environment string `json:"environment,omitempty"`
	// Matches reports whether the branch matches the automatic patterns.
	Matches bool `json:"matches"`
}

// BranchPoll is the outcome of the scheduler's last look at a parent's repository.
type BranchPoll struct {
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

// branchPatternRe keeps patterns to the characters branch names use plus the glob
// characters path.Match knows.
var branchPatternRe = regexp.MustCompile(`^[A-Za-z0-9._/*?\[\]-]{1,100}$`)

// normalizeBranchSettings validates the settings and drops empty entries.
func normalizeBranchSettings(b store.BranchSettings) (store.BranchSettings, error) {
	var patterns []string
	for _, p := range b.Patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !branchPatternRe.MatchString(p) {
			return b, fmt.Errorf("%w: invalid branch pattern %q", validate.ErrInvalid, p)
		}
		if _, err := path.Match(p, ""); err != nil {
			return b, fmt.Errorf("%w: invalid branch pattern %q", validate.ErrInvalid, p)
		}
		if !slices.Contains(patterns, p) {
			patterns = append(patterns, p)
		}
	}
	if len(patterns) > maxBranchPatterns {
		return b, fmt.Errorf("%w: at most %d branch patterns", validate.ErrInvalid, maxBranchPatterns)
	}
	b.Patterns = patterns
	var deploy []string
	for _, c := range b.Deploy {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if len(c) > 1000 || strings.ContainsAny(c, "\x00\n\r") {
			return b, fmt.Errorf("%w: a deploy command must be one line of at most 1000 characters", validate.ErrInvalid)
		}
		deploy = append(deploy, c)
	}
	if len(deploy) > maxDeployCommands {
		return b, fmt.Errorf("%w: at most %d deploy commands", validate.ErrInvalid, maxDeployCommands)
	}
	b.Deploy = deploy
	if b.PollMinutes < 0 || b.PollMinutes > 1440 {
		return b, fmt.Errorf("%w: the poll interval must be between 1 and 1440 minutes", validate.ErrInvalid)
	}
	if b.IdleStopDays < 0 || b.IdleStopDays > 365 {
		return b, fmt.Errorf("%w: the idle stop must be between 1 and 365 days, or off", validate.ErrInvalid)
	}
	if b.MaxEnvironments < 0 || b.MaxEnvironments > maxBranchEnvsLimit {
		return b, fmt.Errorf("%w: at most %d automatic branch environments", validate.ErrInvalid, maxBranchEnvsLimit)
	}
	return b, nil
}

// branchMatches reports whether a branch matches one of the patterns.
func branchMatches(patterns []string, branch string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, branch); ok {
			return true
		}
	}
	return false
}

// branchSlug derives an environment's identifier: the parent's slug and the branch,
// shortened to the 40 characters a slug may have, with a number when it is taken.
func branchSlug(parent, branch string, taken func(string) bool) (string, error) {
	base := validate.Slugify(parent + "-" + branch)
	cut := func(s string, n int) string {
		if len(s) > n {
			s = s[:n]
		}
		return strings.TrimRight(s, "-")
	}
	slug := cut(base, 40)
	for i := 2; taken(slug); i++ {
		if i > 99 {
			return "", fmt.Errorf("%w: no free identifier for the branch %q", ErrConflict, branch)
		}
		suffix := fmt.Sprintf("-%d", i)
		slug = cut(base, 40-len(suffix)) + suffix
	}
	if err := validate.Slug(slug); err != nil {
		return "", err
	}
	return slug, nil
}

// SetBranchSettings stores a project's rules for its branch environments. Only a project
// that is not a branch environment itself has them.
func (m *Manager) SetBranchSettings(ctx context.Context, id string, req BranchSettingsRequest) (store.BranchSettings, error) {
	if err := validate.UUID(id); err != nil {
		return store.BranchSettings{}, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return store.BranchSettings{}, err
	}
	if p.ParentID != "" {
		return store.BranchSettings{}, fmt.Errorf("%w: a branch environment has no branch environments of its own; change the settings on its parent", validate.ErrInvalid)
	}
	b, err := normalizeBranchSettings(req)
	if err != nil {
		return store.BranchSettings{}, err
	}
	if b.Watch && p.Git.URL == "" {
		return store.BranchSettings{}, fmt.Errorf("%w: watching branches needs a repository; set one in the Git tab", validate.ErrInvalid)
	}
	if err := m.store.Projects.SetBranchSettings(ctx, id, b); err != nil {
		return store.BranchSettings{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"branches": map[string]any{"watch": b.Watch, "patterns": b.Patterns, "deploy": len(b.Deploy), "idleStopDays": b.IdleStopDays}}})
	return b, nil
}

// BranchEnvironments returns a project's branch environments, oldest first.
func (m *Manager) BranchEnvironments(ctx context.Context, id string) ([]View, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	if _, err := m.store.Projects.Get(ctx, id); err != nil {
		return nil, err
	}
	all, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []View
	for _, v := range all {
		if v.Project.ParentID == id {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b View) int { return a.Project.CreatedAt.Compare(b.Project.CreatedAt) })
	return out, nil
}

// children are the stored branch environments of a project.
func (m *Manager) children(ctx context.Context, id string) ([]store.Project, error) {
	all, err := m.store.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Project
	for _, p := range all {
		if p.ParentID == id {
			out = append(out, p)
		}
	}
	return out, nil
}

// lsRemote lists the branches of a project's repository with their commits.
func (m *Manager) lsRemote(ctx context.Context, p store.Project) (map[string]string, error) {
	if p.Git.URL == "" {
		return nil, fmt.Errorf("%w: the project has no repository; set one in the Git tab", validate.ErrInvalid)
	}
	res, err := m.runGit(ctx, p, "ls-remote", "--heads", "--", p.Git.URL)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%w: git ls-remote failed: %s", ErrConflict, lastLine(redactOutput(res, p.Git)))
	}
	out := map[string]string{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		commit, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if name, isHead := strings.CutPrefix(ref, "refs/heads/"); ok && isHead && ValidateRef(name) == nil {
			out[name] = commit
		}
	}
	return out, nil
}

// RemoteBranches lists the branches of a project's repository and their environments.
func (m *Manager) RemoteBranches(ctx context.Context, id string) ([]RemoteBranch, error) {
	if err := validate.UUID(id); err != nil {
		return nil, ErrNotFound
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	heads, err := m.lsRemote(ctx, p)
	if err != nil {
		return nil, err
	}
	kids, err := m.children(ctx, id)
	if err != nil {
		return nil, err
	}
	envs := map[string]string{}
	for _, k := range kids {
		envs[k.Git.Branch] = k.Slug
	}
	out := make([]RemoteBranch, 0, len(heads))
	for name, commit := range heads {
		out = append(out, RemoteBranch{Name: name, Commit: commit, Environment: envs[name], Matches: branchMatches(p.Branches.Patterns, name)})
	}
	slices.SortFunc(out, func(a, b RemoteBranch) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// CreateBranchEnvironment makes a branch environment of a project: a started copy with
// files, database, bucket, workers and cron jobs, switched to the branch and deployed.
func (m *Manager) CreateBranchEnvironment(ctx context.Context, id, branch string) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	branch = strings.TrimSpace(branch)
	if err := ValidateRef(branch); err != nil {
		return View{}, err
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if p.ParentID != "" {
		return View{}, fmt.Errorf("%w: %s is a branch environment itself; create the environment from its parent", validate.ErrInvalid, p.Name)
	}
	if p.Git.URL == "" {
		return View{}, fmt.Errorf("%w: branch environments need a repository; set one in the Git tab", validate.ErrInvalid)
	}
	paths, err := m.paths()
	if err != nil {
		return View{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	if _, err := os.Stat(filepath.Join(NewPlanner(paths, m.catalog).ProjectDir(p), ".git")); err != nil {
		return View{}, fmt.Errorf("%w: %s has no git checkout to start from; clone the repository first", ErrConflict, p.Name)
	}
	all, err := m.store.Projects.List(ctx)
	if err != nil {
		return View{}, err
	}
	used := map[string]bool{}
	for _, o := range all {
		if o.ParentID == id && o.Git.Branch == branch {
			return View{}, fmt.Errorf("%w: the branch %s already has the environment %s", ErrConflict, branch, o.Name)
		}
		used[o.Slug], used[o.Path] = true, true
	}
	slug, err := branchSlug(p.Slug, branch, func(s string) bool { return used[s] })
	if err != nil {
		return View{}, err
	}
	name := p.Name + " (" + branch + ")"
	if len([]rune(name)) > 64 {
		name = slug
	}
	v, err := m.Duplicate(ctx, id, DuplicateRequest{
		Name: name, Path: slug, Files: true, IncludeDependencies: true, Database: true, Storage: true, Workers: true, Git: true, Start: true,
		slug: slug, branch: branch,
	})
	if err != nil {
		return View{}, err
	}
	m.touch(v.Project.ID)
	// Whoever has a role in the parent has the same role in its environments.
	if err := m.store.Roles.Copy(ctx, id, v.Project.ID); err != nil {
		m.log.Warn("copy the parent's project roles", "project", v.Project.Slug, "err", err)
	}
	m.notify(ctx, notify.Event{Kind: "branch.changed", Level: notify.Info, Project: v.Project.Name, Title: fmt.Sprintf("Branch environment %s created", v.Project.Name), Message: fmt.Sprintf("%s runs the branch %s of %s.", v.Project.Slug, branch, p.Name)})
	// The copy carries the parent's state; the branch's own dependencies and migrations
	// come with the first deploy.
	if dv, err := m.Deploy(ctx, v.Project.ID, false); err == nil {
		v = dv
	} else {
		m.log.Warn("first deploy of a branch environment failed", "project", v.Project.Slug, "err", err)
	}
	return v, nil
}

// checkoutBranch switches a freshly copied working tree to a branch of its remote. The
// copy keeps the parent's ignored files (.env, vendor/, node_modules/); clean removes
// only untracked files the branch does not ignore.
func (m *Manager) checkoutBranch(ctx context.Context, p store.Project, branch string) error {
	for _, args := range [][]string{
		{"fetch", "origin", "+refs/heads/" + branch + ":refs/remotes/origin/" + branch},
		{"checkout", "-f", "-B", branch, "origin/" + branch},
		{"clean", "-fd"},
	} {
		res, err := m.runGit(ctx, p, args...)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("%w: git %s failed: %s", ErrConflict, args[0], lastLine(redactOutput(res, p.Git)))
		}
	}
	return nil
}

// Deploy brings a branch environment up to date: with pull it pulls its branch (fast
// forward only, local commits stay), then it runs the parent's deploy commands in the
// application container, which must be running. The result is kept in the
// environment's branch state; a failure is also notified.
func (m *Manager) Deploy(ctx context.Context, id string, pull bool) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	return m.runView(ctx, limitProvision, Operation{Action: "deploy", ProjectID: id}, func(ctx context.Context) (View, error) {
		unlock, err := m.lock(id)
		if err != nil {
			return View{}, err
		}
		defer unlock()
		p, err := m.loadProject(ctx, id)
		if err != nil {
			return View{}, err
		}
		if p.ParentID == "" {
			return View{}, fmt.Errorf("%w: %s is not a branch environment", validate.ErrInvalid, p.Name)
		}
		parent, err := m.store.Projects.Get(ctx, p.ParentID)
		if err != nil {
			return View{}, fmt.Errorf("the parent project of %s: %w", p.Name, err)
		}
		derr := m.deploy(ctx, p, parent.Branches.Deploy, pull)
		v, err := m.Get(ctx, id)
		if err != nil {
			return View{}, err
		}
		return v, derr
	})
}

// deploy is Deploy without the lock and operation. Callers hold the lock.
func (m *Manager) deploy(ctx context.Context, p store.Project, commands []string, pull bool) error {
	st := p.BranchState
	st.DeployStatus, st.DeployOutput = store.DeployRunning, ""
	_ = m.store.Projects.SetBranchState(ctx, p.ID, st)
	var out bytes.Buffer
	finish := func(cause error) error {
		st.DeployedAt = time.Now().UTC()
		st.DeployStatus = store.DeploySucceeded
		if cause != nil {
			st.DeployStatus = store.DeployFailed
			fmt.Fprintf(&out, "\nenvoryx: %v\n", cause)
		}
		text := ansiCodes.ReplaceAllString(out.String(), "")
		if len(text) > deployOutputTail {
			text = "…" + text[len(text)-deployOutputTail:]
		}
		st.DeployOutput = text
		if err := m.store.Projects.SetBranchState(context.WithoutCancel(ctx), p.ID, st); err != nil {
			m.log.Warn("record deploy state", "project", p.Slug, "err", err)
		}
		m.audit.Log(ctx, audit.ActionBranchDeployed, "project", p.ID, map[string]any{"name": p.Name, "branch": p.Git.Branch, "commit": st.Commit, "status": st.DeployStatus})
		if cause != nil {
			m.notify(ctx, notify.Event{Kind: "branch.failed", Level: notify.Error, Project: p.Name, Title: fmt.Sprintf("Deploying %s failed", p.Name), Message: cause.Error()})
		}
		return cause
	}
	if pull {
		step(ctx, "Pulling the branch {{branch}}", "branch", p.Git.Branch)
		res, err := m.runGit(ctx, p, "pull", "--ff-only", "origin", p.Git.Branch)
		if err != nil {
			return finish(err)
		}
		out.WriteString(redactOutput(res, p.Git))
		if res.ExitCode != 0 {
			return finish(fmt.Errorf("%w: git pull failed: %s", ErrConflict, lastLine(redactOutput(res, p.Git))))
		}
	}
	if res, err := m.runGit(ctx, p, "rev-parse", "HEAD"); err == nil && res.ExitCode == 0 {
		st.Commit = strings.TrimSpace(res.Stdout)
	}
	if len(commands) == 0 {
		return finish(nil)
	}
	kind, ok := AppKind(p)
	if !ok {
		return finish(fmt.Errorf("%w: the deploy commands need an application container (PHP, Python, Go, Ruby, Java, .NET or Node.js)", ErrConflict))
	}
	for _, c := range commands {
		step(ctx, "Running {{command}}", "command", truncateCmd(c, 60))
		fmt.Fprintf(&out, "\n$ %s\n", c)
		code, err := m.Exec(ctx, p.ID, kind, ExecOptions{Cmd: []string{"sh", "-c", c}, Stdout: &out, Stderr: &out})
		if err != nil {
			return finish(err)
		}
		if code != 0 {
			return finish(fmt.Errorf("%w: %q exited with %d", ErrConflict, truncateCmd(c, 80), code))
		}
	}
	return finish(nil)
}

// branchTracker keeps the scheduler's view in memory: the last visit of each project
// (from the proxy or a start), the last poll of each parent and the branches whose
// environment could not be created, by the commit that failed.
type branchTracker struct {
	mu     sync.Mutex
	visits map[string]time.Time
	polls  map[string]BranchPoll
	failed map[string]string
}

// touch records a visit of a project: the idle stop counts from the last one.
func (m *Manager) touch(id string) {
	m.branches.mu.Lock()
	defer m.branches.mu.Unlock()
	if m.branches.visits == nil {
		m.branches.visits = map[string]time.Time{}
	}
	m.branches.visits[id] = time.Now()
}

// Visited is the proxy's hook for requests that reach a project.
func (m *Manager) Visited(projectID string) { m.touch(projectID) }

// LastBranchPoll returns the outcome of the last poll of a parent's repository.
func (m *Manager) LastBranchPoll(id string) (BranchPoll, bool) {
	m.branches.mu.Lock()
	defer m.branches.mu.Unlock()
	p, ok := m.branches.polls[id]
	return p, ok
}

func (m *Manager) recordPoll(id string, err error) {
	m.branches.mu.Lock()
	defer m.branches.mu.Unlock()
	if m.branches.polls == nil {
		m.branches.polls = map[string]BranchPoll{}
	}
	poll := BranchPoll{At: time.Now().UTC()}
	if err != nil {
		poll.Error = err.Error()
	}
	m.branches.polls[id] = poll
}

// RunBranchScheduler keeps the branch environments in step with their repositories and
// stops idle ones until ctx ends.
func (m *Manager) RunBranchScheduler(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runBranchPass(ctx, time.Now(), log)
		}
	}
}

// runBranchPass is one scheduler pass: the visits recorded since the last pass are stored,
// idle environments stopped and every watched repository that is due polled.
func (m *Manager) runBranchPass(ctx context.Context, now time.Time, log *slog.Logger) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		log.Warn("branch scheduler: list projects", "err", err)
		return
	}
	byID := map[string]store.Project{}
	for _, p := range projects {
		byID[p.ID] = p
	}
	m.branches.mu.Lock()
	visits := m.branches.visits
	m.branches.visits = nil
	m.branches.mu.Unlock()
	for _, p := range projects {
		if p.ParentID == "" {
			continue
		}
		if at, ok := visits[p.ID]; ok && at.After(p.BranchState.LastAccess) {
			p.BranchState.LastAccess = at.UTC()
			byID[p.ID] = p
			if err := m.store.Projects.SetBranchState(ctx, p.ID, p.BranchState); err != nil {
				log.Warn("branch scheduler: record visit", "project", p.Slug, "err", err)
			}
		}
		parent, ok := byID[p.ParentID]
		if !ok || parent.Branches.IdleStopDays == 0 || p.DesiredState != store.DesiredRunning || p.Lifecycle != store.LifecycleReady {
			continue
		}
		last := p.BranchState.LastAccess
		for _, t := range []time.Time{p.BranchState.DeployedAt, p.CreatedAt} {
			if t.After(last) {
				last = t
			}
		}
		if now.Sub(last) < time.Duration(parent.Branches.IdleStopDays)*24*time.Hour {
			continue
		}
		if _, err := m.Stop(ctx, p.ID); err != nil {
			log.Warn("branch scheduler: stop idle environment", "project", p.Slug, "err", err)
			continue
		}
		log.Info("idle branch environment stopped", "project", p.Slug, "idleDays", parent.Branches.IdleStopDays)
		m.notify(ctx, notify.Event{Kind: "branch.changed", Level: notify.Info, Project: p.Name, Title: fmt.Sprintf("%s stopped", p.Name), Message: fmt.Sprintf("Nobody opened it for %d days. Start it again whenever you need it.", parent.Branches.IdleStopDays)})
	}
	for _, parent := range projects {
		b := parent.Branches
		if !b.Watch || parent.ParentID != "" || parent.Git.URL == "" || parent.Lifecycle != store.LifecycleReady {
			continue
		}
		every := time.Duration(b.PollMinutes) * time.Minute
		if every == 0 {
			every = defaultBranchPollMinutes * time.Minute
		}
		if last, ok := m.LastBranchPoll(parent.ID); ok && now.Sub(last.At) < every {
			continue
		}
		err := m.pollBranches(ctx, parent, projects, log)
		m.recordPoll(parent.ID, err)
		if err != nil {
			log.Warn("branch scheduler: poll", "project", parent.Slug, "err", err)
		}
	}
}

// pollBranches compares a parent's environments with its repository: an environment whose
// branch is gone is deleted, a running one behind its branch deployed, and a matching new
// branch gets an environment (one per pass, up to the limit).
func (m *Manager) pollBranches(ctx context.Context, parent store.Project, projects []store.Project, log *slog.Logger) error {
	heads, err := m.lsRemote(ctx, parent)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	count := 0
	for _, p := range projects {
		if p.ParentID != parent.ID {
			continue
		}
		count++
		have[p.Git.Branch] = true
		commit, ok := heads[p.Git.Branch]
		switch {
		case !ok:
			if p.Lifecycle != store.LifecycleReady {
				continue
			}
			log.Info("branch gone, deleting its environment", "project", p.Slug, "branch", p.Git.Branch)
			if err := m.Delete(ctx, p.ID, DeleteOptions{Confirm: p.Slug, DeleteFiles: true}); err != nil {
				log.Warn("branch scheduler: delete environment", "project", p.Slug, "err", err)
				continue
			}
			count--
			m.notify(ctx, notify.Event{Kind: "branch.changed", Level: notify.Info, Project: p.Name, Title: fmt.Sprintf("%s deleted", p.Name), Message: fmt.Sprintf("The branch %s is gone from the repository.", p.Git.Branch)})
		case commit != p.BranchState.Commit && p.DesiredState == store.DesiredRunning && p.Lifecycle == store.LifecycleReady && p.BranchState.DeployStatus != store.DeployRunning:
			// A failed deploy of this very commit is not retried on every pass.
			if p.BranchState.DeployStatus == store.DeployFailed && p.BranchState.Commit == commit {
				continue
			}
			log.Info("branch moved, deploying", "project", p.Slug, "branch", p.Git.Branch, "commit", commit)
			if _, err := m.Deploy(ctx, p.ID, true); err != nil {
				log.Warn("branch scheduler: deploy", "project", p.Slug, "err", err)
			}
		}
	}
	limit := parent.Branches.MaxEnvironments
	if limit == 0 {
		limit = defaultMaxBranchEnvs
	}
	// A branch whose environment failed is tried again with its next commit, not on
	// every pass: each attempt copies the whole project.
	m.branches.mu.Lock()
	var fresh []string
	for name, commit := range heads {
		if !have[name] && name != parent.Git.Branch && branchMatches(parent.Branches.Patterns, name) && m.branches.failed[parent.ID+"\x00"+name] != commit {
			fresh = append(fresh, name)
		}
	}
	m.branches.mu.Unlock()
	slices.Sort(fresh)
	if len(fresh) == 0 || count >= limit {
		return nil
	}
	name := fresh[0]
	log.Info("new branch, creating its environment", "project", parent.Slug, "branch", name)
	if _, err := m.CreateBranchEnvironment(ctx, parent.ID, name); err != nil {
		m.branches.mu.Lock()
		if m.branches.failed == nil {
			m.branches.failed = map[string]string{}
		}
		m.branches.failed[parent.ID+"\x00"+name] = heads[name]
		m.branches.mu.Unlock()
		m.notify(ctx, notify.Event{Kind: "branch.failed", Level: notify.Error, Project: parent.Name, Title: fmt.Sprintf("No environment for the branch %s", name), Message: err.Error()})
		return fmt.Errorf("create the environment of %s: %w", name, err)
	}
	return nil
}
