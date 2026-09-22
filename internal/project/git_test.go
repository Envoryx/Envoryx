package project

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestValidateGitURL(t *testing.T) {
	ok := []string{"https://github.com/envoryx/envoryx.git", "http://gitea.lan/org/repo", "git@github.com:envoryx/envoryx.git", "ssh://git@gitlab.com/group/repo.git"}
	for _, u := range ok {
		if _, err := ValidateGitURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	bad := []string{"", "-oProxyCommand=evil", "file:///etc", "/srv/repo", "ext::sh -c id", "https://user:pw@host/repo", "https://host/repo; rm -rf /", "ftp://host/repo"}
	for _, u := range bad {
		if _, err := ValidateGitURL(u); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("%q must be rejected, got %v", u, err)
		}
	}
	for _, r := range []string{"main", "release/1.2", "feature-x"} {
		if err := ValidateRef(r); err != nil {
			t.Errorf("ref %s: %v", r, err)
		}
	}
	for _, r := range []string{"-x", "a..b", "x/", "y.lock", "a b"} {
		if err := ValidateRef(r); !errors.Is(err, validate.ErrInvalid) {
			t.Errorf("ref %q must be rejected", r)
		}
	}
}

func TestCreateWithGitClonesInTransientContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		if spec.Cmd[3] == "clone" {
			// Simulate a successful clone by creating a .git directory.
			dir := filepath.Join(e.projDir, "cloned")
			_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
			return docker.ExecResult{Stdout: "Cloning into '.'...\n"}, nil
		}
		if spec.Cmd[3] == "log" {
			return docker.ExecResult{Stdout: "abc123def\nabc123d\nInitial commit\nStefan\n2026-09-18T10:00:00+02:00\n"}, nil
		}
		if spec.Cmd[3] == "rev-parse" {
			return docker.ExecResult{Stdout: "main\n"}, nil
		}
		if spec.Cmd[3] == "status" {
			return docker.ExecResult{Stdout: " M index.php\n?? new.txt\n"}, nil
		}
		return docker.ExecResult{Stdout: "https://github.com/seramos/example.git\n"}, nil
	}
	req := phpRequest("Cloned", true)
	req.Git = &GitRequest{URL: "https://github.com/seramos/example.git", Branch: "main", Token: "ghp_secretTOKEN"}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "cloned", "public", "index.php")); err == nil {
		t.Fatal("starter page must not be written when cloning")
	}
	clone := e.engine.OneShots[0]
	cmd := strings.Join(clone.Cmd, " ")
	if cmd != "git -C /var/www/html clone --progress --branch main -- https://github.com/seramos/example.git ." {
		t.Fatalf("clone cmd: %s", cmd)
	}
	if strings.Contains(cmd, "ghp_secretTOKEN") {
		t.Fatal("token must never appear in argv")
	}
	envs := strings.Join(clone.Env, "\n")
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_secretTOKEN"))
	if !strings.Contains(envs, "GIT_CONFIG_KEY_1=http.extraHeader") || !strings.Contains(envs, want) || !strings.Contains(envs, "GIT_CONFIG_COUNT=2") {
		t.Fatalf("git env: %s", envs)
	}
	if clone.User != "1000:1000" || clone.Mounts[0].Source != "/host/development/cloned" || clone.Mounts[1].Source != "/host/appdata/envoryx/ssh" || clone.Mounts[1].Target != "/tmp/envoryx-ssh" {
		t.Fatalf("clone container spec: %+v", clone)
	}
	if clone.Labels[docker.LabelService] != "git" || clone.Labels[docker.LabelProjectID] != view.Project.ID {
		t.Fatalf("labels: %v", clone.Labels)
	}
	// The long-running php container must NOT get the deploy key.
	php, _ := e.engine.Container("envoryx-cloned-php")
	for _, m := range php.Spec.Mounts {
		if strings.Contains(m.Source, "/ssh") {
			t.Fatal("deploy key must not be mounted into the php container")
		}
	}
	// Deploy key generated with the right ownership expectations.
	for _, f := range []string{"id_ed25519", "id_ed25519.pub", "known_hosts"} {
		if _, err := os.Stat(filepath.Join(e.cfgDir, "ssh", f)); err != nil {
			t.Fatalf("ssh file %s: %v", f, err)
		}
	}
	info, _ := os.Stat(filepath.Join(e.cfgDir, "ssh", "id_ed25519"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %o", info.Mode().Perm())
	}
	pub, err := e.m.DeployKey(ctx)
	if err != nil || !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("deploy key: %q %v", pub, err)
	}

	st, err := e.m.GitStatus(ctx, view.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Configured || !st.IsRepo || !st.HasToken || st.Current != "main" || st.ShortHash != "abc123d" || st.Subject != "Initial commit" || st.Dirty != 2 {
		t.Fatalf("status: %+v", st)
	}
	entries, _ := e.store.Audit.Recent(ctx, 20)
	for _, en := range entries {
		if strings.Contains(string(en.Details), "ghp_secretTOKEN") {
			t.Fatal("token leaked into audit log")
		}
	}
}

func TestCloneFailureRollsBackProject(t *testing.T) {
	e := newEnv(t)
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		return docker.ExecResult{ExitCode: 128, Stderr: "fatal: Authentication failed for 'https://github.com/x/y.git/' token=ghp_bad"}, nil
	}
	req := phpRequest("Broken", true)
	req.Git = &GitRequest{URL: "https://github.com/x/y.git", Token: "ghp_bad"}
	_, err := e.m.Create(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "Authentication failed") || strings.Contains(err.Error(), "ghp_bad") {
		t.Fatalf("expected redacted clone failure, got %v", err)
	}
	if n, _ := e.store.Projects.Count(context.Background()); n != 0 {
		t.Fatal("project must be rolled back after a failed clone")
	}
	if len(e.engine.ContainerNames()) != 0 {
		t.Fatalf("containers left behind: %v", e.engine.ContainerNames())
	}
}

func TestPullAndCheckoutAndSSH(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var cmds []string
	var envs [][]string
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		cmds = append(cmds, strings.Join(spec.Cmd, " "))
		envs = append(envs, spec.Env)
		return docker.ExecResult{Stdout: "Already up to date.\n"}, nil
	}
	view, err := e.m.Create(ctx, phpRequest("Later", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	st, err := e.m.SetGit(ctx, id, GitRequest{URL: "git@github.com:seramos/later.git", Branch: "develop"})
	if err != nil || !st.Configured || st.HasToken {
		t.Fatalf("set git: %+v %v", st, err)
	}
	if _, err := e.m.SetGit(ctx, id, GitRequest{URL: "file:///etc"}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("invalid url must be rejected, got %v", err)
	}
	if _, err := e.m.Checkout(ctx, id, "--force"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("option injection must be rejected, got %v", err)
	}
	res, err := e.m.Pull(ctx, id)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "Already up to date") {
		t.Fatalf("pull: %+v %v", res, err)
	}
	if !strings.HasPrefix(cmds[0], "git -C /var/www/html pull --ff-only") {
		t.Fatalf("pull cmd: %s", cmds[0])
	}
	env := strings.Join(envs[0], "\n")
	if !strings.Contains(env, "GIT_SSH_COMMAND=ssh -i /tmp/envoryx-ssh/id_ed25519") || strings.Contains(env, "http.extraHeader") {
		t.Fatalf("ssh env: %s", env)
	}
	if _, err := e.m.Checkout(ctx, id, "release/2.0"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cmds {
		if c == "git -C /var/www/html checkout -- release/2.0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("checkout cmd missing: %v", cmds)
	}
	// Clone into a non-empty directory is refused.
	if _, err := e.m.Clone(ctx, id); !errors.Is(err, ErrConflict) {
		t.Fatalf("clone into non-empty dir must conflict, got %v", err)
	}
}

// Git one-shots run from whichever runtime image the project has: PHP, else Node, else the
// catalogue's default Node image – so clone and status work for every project shape.
func TestGitRunsFromTheProjectRuntimeImage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		if spec.Cmd[3] == "clone" {
			// The slug is the project directory name; simulate a successful clone.
			slug := strings.TrimSuffix(strings.TrimPrefix(spec.Name, "envoryx-"), spec.Name[strings.LastIndex(spec.Name, "-git-"):])
			_ = os.MkdirAll(filepath.Join(e.projDir, slug, ".git"), 0o755)
			return docker.ExecResult{Stdout: "Cloning into '.'...\n"}, nil
		}
		return docker.ExecResult{Stdout: "main\n"}, nil
	}
	lastGit := func() docker.ContainerSpec {
		t.Helper()
		for i := len(e.engine.OneShots) - 1; i >= 0; i-- {
			if e.engine.OneShots[i].Labels[docker.LabelService] == "git" {
				return e.engine.OneShots[i]
			}
		}
		t.Fatal("no git one-shot ran")
		return docker.ContainerSpec{}
	}

	// Node-only project: create with a git URL clones from the Node image (this used to
	// fail and roll the project back because git insisted on PHP).
	req := nodeRequest("Front", true)
	req.Git = &GitRequest{URL: "https://github.com/seramos/front.git"}
	front, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	clone := e.engine.OneShots[0]
	if strings.Join(clone.Cmd, " ") != "git -C /var/www/html clone --progress -- https://github.com/seramos/front.git ." {
		t.Fatalf("clone cmd: %v", clone.Cmd)
	}
	if clone.Image != "ghcr.io/envoryx/envoryx-node:24" || clone.User != "1000:1000" {
		t.Fatalf("clone container: %+v", clone)
	}
	if len(clone.Mounts) != 2 || clone.Mounts[1].Target != sshMountTarget || !strings.HasSuffix(clone.Mounts[1].Source, "/ssh") {
		t.Fatalf("deploy key mount: %+v", clone.Mounts)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "front", "index.html")); err == nil {
		t.Fatal("starter page must not be written when cloning")
	}
	if res, err := e.m.Pull(ctx, front.Project.ID); err != nil || res.ExitCode != 0 {
		t.Fatalf("pull: %+v %v", res, err)
	}
	if img := lastGit().Image; img != "ghcr.io/envoryx/envoryx-node:24" {
		t.Fatalf("pull image: %s", img)
	}
	// The long-running node container must not get the deploy key.
	node, _ := e.engine.Container("envoryx-front-node")
	for _, m := range node.Spec.Mounts {
		if strings.Contains(m.Source, "/ssh") {
			t.Fatal("deploy key must not be mounted into the node container")
		}
	}

	// Static project: no runtime at all → the catalogue's default Node image.
	static, err := e.m.Create(ctx, staticRequest("Site", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetGit(ctx, static.Project.ID, GitRequest{URL: "git@github.com:seramos/site.git"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Pull(ctx, static.Project.ID); err != nil {
		t.Fatal(err)
	}
	def, _ := e.m.catalog.Resolve("node", "")
	if img := lastGit().Image; img != def.Image || img == "" {
		t.Fatalf("static git image %q, want catalogue default %q", img, def.Image)
	}

	// PHP project (with Node as well): PHP image as before.
	req = phpRequest("Shop", true)
	req.Node = &NodeRequest{Version: "24"}
	shop, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetGit(ctx, shop.Project.ID, GitRequest{URL: "https://github.com/seramos/shop.git"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Pull(ctx, shop.Project.ID); err != nil {
		t.Fatal(err)
	}
	if img := lastGit().Image; img != "ghcr.io/envoryx/envoryx-php:8.4" {
		t.Fatalf("php git image: %s", img)
	}
}
