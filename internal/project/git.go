package project

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
)

// GitRequest binds a repository to a project. Token is write-only.
type GitRequest struct {
	URL      string
	Branch   string
	Username string
	Token    string
	// KeepToken leaves the stored token untouched (updates only).
	KeepToken bool
}

// GitStatus is the observed repository state of a project.
type GitStatus struct {
	Configured bool   `json:"configured"`
	URL        string `json:"url,omitempty"`
	Branch     string `json:"branch,omitempty"` // configured branch
	HasToken   bool   `json:"hasToken"`
	IsRepo     bool   `json:"isRepo"`
	Current    string `json:"currentBranch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	ShortHash  string `json:"shortHash,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Author     string `json:"author,omitempty"`
	Date       string `json:"date,omitempty"`
	Dirty      int    `json:"dirty"`
	Remote     string `json:"remote,omitempty"`
	Error      string `json:"error,omitempty"`
}

// GitResult is the outcome of a git operation.
type GitResult struct {
	Output   string    `json:"output"`
	ExitCode int       `json:"exitCode"`
	Status   GitStatus `json:"status"`
}

var (
	scpLikeRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$`)
	refRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

// ValidateGitURL accepts https://, http://, ssh:// and scp-like git@host:path URLs. Local
// paths, file://, ext:: and anything starting with "-" (option injection) are rejected.
func ValidateGitURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: repository URL is empty", validate.ErrInvalid)
	}
	if strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n'\"`$;|&<>\\") {
		return "", fmt.Errorf("%w: repository URL contains unsupported characters", validate.ErrInvalid)
	}
	if scpLikeRe.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: invalid repository URL", validate.ErrInvalid)
	}
	switch u.Scheme {
	case "https", "http", "ssh":
	default:
		return "", fmt.Errorf("%w: repository URL must use https://, ssh:// or git@host:path", validate.ErrInvalid)
	}
	if u.Host == "" || u.Path == "" || u.Path == "/" {
		return "", fmt.Errorf("%w: repository URL must contain a host and a path", validate.ErrInvalid)
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			return "", fmt.Errorf("%w: do not put credentials into the URL; use the access token field", validate.ErrInvalid)
		}
	}
	return raw, nil
}

// ValidateRef checks a branch or tag name.
func ValidateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if !refRe.MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") {
		return fmt.Errorf("%w: invalid branch name %q", validate.ErrInvalid, ref)
	}
	return nil
}

func isSSHURL(raw string) bool {
	return strings.HasPrefix(raw, "ssh://") || scpLikeRe.MatchString(raw)
}

func buildGitConfig(req GitRequest, current store.GitConfig) (store.GitConfig, error) {
	if strings.TrimSpace(req.URL) == "" {
		return store.GitConfig{}, nil
	}
	u, err := ValidateGitURL(req.URL)
	if err != nil {
		return store.GitConfig{}, err
	}
	if err := ValidateRef(req.Branch); err != nil {
		return store.GitConfig{}, err
	}
	username := strings.TrimSpace(req.Username)
	if username != "" && !regexp.MustCompile(`^[A-Za-z0-9._@-]{1,100}$`).MatchString(username) {
		return store.GitConfig{}, fmt.Errorf("%w: invalid git username", validate.ErrInvalid)
	}
	token := strings.TrimSpace(req.Token)
	if req.KeepToken {
		token = current.Token
	}
	if len(token) > 512 || strings.ContainsAny(token, " \r\n") {
		return store.GitConfig{}, fmt.Errorf("%w: invalid access token", validate.ErrInvalid)
	}
	return store.GitConfig{URL: u, Branch: req.Branch, Username: username, Token: token}, nil
}

// ---- deploy key ----------------------------------------------------------------------

const sshDirName = "ssh"

func (m *Manager) sshDir() (string, error) {
	p, err := m.paths()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.ConfigDir, sshDirName), nil
}

// DeployKey returns the public deploy key, generating an Ed25519 key pair on first use.
// The private key is owned by PUID:PGID (git runs as that user) with mode 0600.
func (m *Manager) DeployKey(ctx context.Context) (string, error) {
	dir, err := m.sshDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	pubPath := filepath.Join(dir, "id_ed25519.pub")
	if b, err := os.ReadFile(pubPath); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	return m.RegenerateDeployKey(ctx)
}

// RegenerateDeployKey creates a new key pair, replacing any existing one.
func (m *Manager) RegenerateDeployKey(ctx context.Context) (string, error) {
	dir, err := m.sshDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	paths, err := m.paths()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create ssh directory: %w", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "staqio deploy key")
	if err != nil {
		return "", fmt.Errorf("encode key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " staqio-deploy-key"
	privPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		return "", fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519.pub"), []byte(pubLine+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write public key: %w", err)
	}
	// known_hosts is written by ssh inside the container (accept-new); it must be writable.
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), nil, 0o644); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if os.Geteuid() == 0 {
		for _, f := range []string{dir, privPath, filepath.Join(dir, "id_ed25519.pub"), filepath.Join(dir, "known_hosts")} {
			_ = os.Chown(f, paths.PUID, paths.PGID)
		}
	}
	m.audit.Log(ctx, audit.ActionDeployKeyGenerated, "settings", "", nil)
	return pubLine, nil
}

// ---- running git ----------------------------------------------------------------------

const sshMountTarget = "/tmp/staqio-ssh"

// gitEnv builds the environment for git: credentials via GIT_CONFIG_* (never on the
// command line), the deploy key via GIT_SSH_COMMAND.
func gitEnv(g store.GitConfig) []string {
	env := []string{
		"HOME=/tmp",
		"GIT_TERMINAL_PROMPT=0",
		"LANG=C.UTF-8",
		"GIT_SSH_COMMAND=ssh -i " + sshMountTarget + "/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + sshMountTarget + "/known_hosts",
	}
	n := 0
	add := func(k, v string) {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, v))
		n++
	}
	add("safe.directory", appMountTarget)
	if g.Token != "" && !isSSHURL(g.URL) {
		user := g.Username
		if user == "" {
			user = "x-access-token"
		}
		add("http.extraHeader", "Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+g.Token)))
	}
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(n))
	return env
}

// runGit executes git with the given arguments in the project directory inside a transient
// container built from the project's PHP image (which ships git and ssh). The deploy key
// is only ever mounted into this short-lived container, never into the long-running PHP
// container, so application code cannot read it.
func (m *Manager) runGit(ctx context.Context, proj store.Project, args ...string) (docker.ExecResult, error) {
	php := proj.Service(store.ServicePHP)
	if php == nil {
		return docker.ExecResult{}, fmt.Errorf("%w: git needs a PHP service (the git client ships in the PHP image)", ErrConflict)
	}
	paths, err := m.paths()
	if err != nil {
		return docker.ExecResult{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	planner := NewPlanner(paths, m.catalog)
	if _, err := m.DeployKey(ctx); err != nil {
		return docker.ExecResult{}, err
	}
	if err := m.engine.EnsureImage(ctx, php.Image, m.pullProgress(proj.Slug)); err != nil {
		return docker.ExecResult{}, err
	}
	spec := docker.ContainerSpec{
		Name:       fmt.Sprintf("staqio-%s-git-%d", proj.Slug, time.Now().UnixNano()%1_000_000),
		Image:      php.Image,
		Labels:     docker.ManagedLabels(proj.ID, proj.Slug, "git", paths.StaqioVersion),
		Env:        gitEnv(proj.Git),
		Cmd:        append([]string{"git", "-C", appMountTarget}, args...),
		WorkingDir: appMountTarget,
		User:       fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
		Mounts: []docker.MountSpec{
			{Type: "bind", Source: planner.projectHostDir(proj), Target: appMountTarget},
			{Type: "bind", Source: filepath.Join(paths.ConfigHostDir, sshDirName), Target: sshMountTarget},
		},
		RestartPolicy: "no",
	}
	return m.engine.RunOneShot(ctx, spec)
}

// GitStatus returns the repository state of a project.
func (m *Manager) GitStatus(ctx context.Context, id string) (GitStatus, error) {
	if err := validate.UUID(id); err != nil {
		return GitStatus{}, ErrNotFound
	}
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return GitStatus{}, err
	}
	return m.gitStatus(ctx, proj), nil
}

func (m *Manager) gitStatus(ctx context.Context, proj store.Project) GitStatus {
	st := GitStatus{Configured: proj.Git.URL != "", URL: proj.Git.URL, Branch: proj.Git.Branch, HasToken: proj.Git.Token != ""}
	paths, err := m.paths()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	dir := NewPlanner(paths, m.catalog).ProjectDir(proj)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return st
	}
	st.IsRepo = true
	res, err := m.runGit(ctx, proj, "log", "-1", "--format=%H%n%h%n%s%n%an%n%cI")
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if res.ExitCode != 0 {
		st.Error = strings.TrimSpace(res.Stderr)
		return st
	}
	parts := strings.SplitN(strings.TrimSpace(res.Stdout), "\n", 5)
	if len(parts) == 5 {
		st.Commit, st.ShortHash, st.Subject, st.Author, st.Date = parts[0], parts[1], parts[2], parts[3], parts[4]
	}
	if res, err := m.runGit(ctx, proj, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && res.ExitCode == 0 {
		st.Current = strings.TrimSpace(res.Stdout)
	}
	if res, err := m.runGit(ctx, proj, "status", "--porcelain"); err == nil && res.ExitCode == 0 {
		for _, l := range strings.Split(res.Stdout, "\n") {
			if strings.TrimSpace(l) != "" {
				st.Dirty++
			}
		}
	}
	if res, err := m.runGit(ctx, proj, "remote", "get-url", "origin"); err == nil && res.ExitCode == 0 {
		st.Remote = redactURL(strings.TrimSpace(res.Stdout))
	}
	return st
}

func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = url.User(u.User.Username())
		return u.String()
	}
	return raw
}

// SetGit stores the repository binding without touching files.
func (m *Manager) SetGit(ctx context.Context, id string, req GitRequest) (GitStatus, error) {
	if err := validate.UUID(id); err != nil {
		return GitStatus{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return GitStatus{}, err
	}
	defer unlock()
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return GitStatus{}, err
	}
	cfg, err := buildGitConfig(req, proj.Git)
	if err != nil {
		return GitStatus{}, err
	}
	if err := m.store.Projects.UpdateGit(ctx, id, cfg); err != nil {
		return GitStatus{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": proj.Name, "changes": map[string]any{"git": redactURL(cfg.URL), "branch": cfg.Branch}})
	proj.Git = cfg
	return m.gitStatus(ctx, proj), nil
}

// Clone clones the configured repository into the (empty) project directory.
func (m *Manager) Clone(ctx context.Context, id string) (GitResult, error) {
	if err := validate.UUID(id); err != nil {
		return GitResult{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return GitResult{}, err
	}
	defer unlock()
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return GitResult{}, err
	}
	return m.clone(ctx, proj)
}

func (m *Manager) clone(ctx context.Context, proj store.Project) (GitResult, error) {
	if proj.Git.URL == "" {
		return GitResult{}, fmt.Errorf("%w: no repository configured", validate.ErrInvalid)
	}
	paths, err := m.paths()
	if err != nil {
		return GitResult{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	dir := NewPlanner(paths, m.catalog).ProjectDir(proj)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return GitResult{}, err
	}
	if len(entries) > 0 {
		return GitResult{}, fmt.Errorf("%w: the project directory is not empty; clone needs an empty directory", ErrConflict)
	}
	args := []string{"clone", "--progress"}
	if proj.Git.Branch != "" {
		args = append(args, "--branch", proj.Git.Branch)
	}
	args = append(args, "--", proj.Git.URL, ".")
	res, err := m.runGit(ctx, proj, args...)
	if err != nil {
		return GitResult{}, err
	}
	out := redactOutput(res, proj.Git)
	m.audit.Log(ctx, audit.ActionGitClone, "project", proj.ID, map[string]any{"name": proj.Name, "url": redactURL(proj.Git.URL), "exitCode": res.ExitCode})
	if res.ExitCode != 0 {
		return GitResult{Output: out, ExitCode: res.ExitCode, Status: m.gitStatus(ctx, proj)}, fmt.Errorf("%w: git clone failed: %s", ErrConflict, lastLine(out))
	}
	return GitResult{Output: out, ExitCode: 0, Status: m.gitStatus(ctx, proj)}, nil
}

// Pull fast-forwards the current branch.
func (m *Manager) Pull(ctx context.Context, id string) (GitResult, error) {
	return m.gitOp(ctx, id, audit.ActionGitPull, "pull", "--ff-only", "--progress")
}

// Checkout switches to a branch (validated ref name).
func (m *Manager) Checkout(ctx context.Context, id, ref string) (GitResult, error) {
	if ref == "" {
		return GitResult{}, fmt.Errorf("%w: branch name is empty", validate.ErrInvalid)
	}
	if err := ValidateRef(ref); err != nil {
		return GitResult{}, err
	}
	return m.gitOp(ctx, id, audit.ActionGitCheckout, "checkout", "--", ref)
}

func (m *Manager) gitOp(ctx context.Context, id, action string, args ...string) (GitResult, error) {
	if err := validate.UUID(id); err != nil {
		return GitResult{}, ErrNotFound
	}
	unlock, err := m.lock(id)
	if err != nil {
		return GitResult{}, err
	}
	defer unlock()
	proj, err := m.loadProject(ctx, id)
	if err != nil {
		return GitResult{}, err
	}
	res, err := m.runGit(ctx, proj, args...)
	if err != nil {
		return GitResult{}, err
	}
	out := redactOutput(res, proj.Git)
	m.audit.Log(ctx, action, "project", id, map[string]any{"name": proj.Name, "exitCode": res.ExitCode})
	status := m.gitStatus(ctx, proj)
	if res.ExitCode != 0 {
		return GitResult{Output: out, ExitCode: res.ExitCode, Status: status}, fmt.Errorf("%w: git %s failed: %s", ErrConflict, args[0], lastLine(out))
	}
	return GitResult{Output: out, ExitCode: 0, Status: status}, nil
}

func redactOutput(res docker.ExecResult, g store.GitConfig) string {
	out := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	if g.Token != "" {
		out = strings.ReplaceAll(out, g.Token, "***")
	}
	if len(out) > 20000 {
		out = out[len(out)-20000:]
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "unknown error"
}
