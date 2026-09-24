// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/envoryx/envoryx/internal/manifest"
	"github.com/envoryx/envoryx/internal/validate"
)

const upUsage = `Usage: envoryx up [flags]

Brings up the project described by envoryx.yml in this repository. The file is looked
for in the current directory and its parents. When the server has no such project yet
it clones the repository (the origin remote and the checked-out branch) and creates the
project exactly as described; otherwise it brings the existing one in line. Then the
project is started.

  -f FILE              the manifest (default: envoryx.yml here or above)
  --name NAME          project name (default: name in the manifest, else the directory)
  --path DIR           directory below the projects directory (new projects)
  --git URL            repository the server clones (default: the origin remote)
  --remote NAME        take the URL from this remote instead of origin
  --branch NAME        branch to check out (default: the current one)
  --git-username NAME  user name for a private repository
  --git-token TOKEN    access token for a private repository ("-" reads stdin)
  --secret KEY=VALUE   value for a variable listed under secrets, repeatable; in a
                       terminal the missing ones are asked for
  --prune              also remove what the manifest no longer has – services with
                       their data, variables, domains, workers, cron jobs
  --dry-run            show what would change and stop
  --no-start           leave the project stopped

"envoryx project manifest <project>" writes the manifest of an existing project.
`

func (c *cli) up(ctx context.Context, args []string) error {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		fmt.Fprint(c.errOut, upUsage)
		return nil
	}
	fs := c.newFlags("up")
	var (
		file     = fs.String("f", "", "manifest file")
		name     = fs.String("name", "", "project name")
		path     = fs.String("path", "", "project directory")
		gitURL   = fs.String("git", "", "repository URL")
		remote   = fs.String("remote", "origin", "git remote")
		branch   = fs.String("branch", "", "branch")
		gitUser  = fs.String("git-username", "", "repository user name")
		gitToken = fs.String("git-token", "", "repository token")
		prune    = fs.Bool("prune", false, "remove what the manifest no longer has")
		dryRun   = fs.Bool("dry-run", false, "only show the changes")
		noStart  = fs.Bool("no-start", false, "do not start the project")
		secretKV stringList
	)
	fs.Var(&secretKV, "secret", "KEY=VALUE, repeatable")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("envoryx up takes no arguments (did you mean --name %s?)", pos[0])
	}

	manifestPath, err := findManifest(*file)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	// Checked here so a typo is reported before anything talks to the server.
	mf, err := manifest.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %s", manifestPath, strings.TrimPrefix(err.Error(), validate.ErrInvalid.Error()+": "))
	}
	dir := filepath.Dir(manifestPath)
	projectName := strings.TrimSpace(*name)
	if projectName == "" {
		projectName = mf.Name
	}
	if projectName == "" {
		projectName = filepath.Base(dir)
	}
	secrets := map[string]string{}
	for _, kv := range secretKV {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return usagef("--secret expects KEY=VALUE, got %q", kv)
		}
		if !slices.Contains(mf.Secrets, key) {
			return usagef("%s is not listed under secrets in %s", key, manifestPath)
		}
		secrets[key] = value
	}

	ctx, cancel := c.context(ctx)
	defer cancel()
	api, err := c.connect()
	if err != nil {
		return err
	}
	existing, found, err := c.lookupProject(ctx, projectName)
	if err != nil {
		return err
	}

	if found {
		if u, _ := gitOutput(dir, "remote", "get-url", *remote); u != "" && existing.Git.URL != "" && u != existing.Git.URL {
			fmt.Fprintf(c.errOut, "envoryx: note: %s clones %s, this checkout's %s is %s\n", existing.Slug, existing.Git.URL, *remote, u)
		}
		body := map[string]any{"yaml": string(raw), "prune": *prune, "secrets": secrets}
		var planned struct {
			Plan manifestPlan `json:"plan"`
		}
		if err := api.post(ctx, projectPath(existing.ID, "manifest", "plan"), body, &planned); err != nil {
			return err
		}
		if *dryRun {
			if c.json {
				return c.printJSON(planned.Plan)
			}
			c.printf("%s (%s):\n", existing.Name, existing.Slug)
			c.printPlan(planned.Plan, *prune)
			return nil
		}
		if err := c.askSecrets(planned.Plan.MissingSecrets, secrets); err != nil {
			return err
		}
		body["start"] = !*noStart
		var applied struct {
			Plan    manifestPlan   `json:"plan"`
			Project projectSummary `json:"project"`
		}
		if err := api.post(ctx, projectPath(existing.ID, "manifest", "apply"), body, &applied); err != nil {
			return err
		}
		if c.json {
			return c.printJSON(applied)
		}
		c.printf("%s (%s):\n", applied.Project.Name, applied.Project.Slug)
		c.printPlan(applied.Plan, *prune)
		c.printUp(applied.Project, applied.Plan, !*noStart)
		return nil
	}

	// A new project: the server clones the repository this manifest came from.
	git := &gitSpec{URL: strings.TrimSpace(*gitURL), Branch: strings.TrimSpace(*branch), Username: *gitUser}
	if git.URL == "" {
		u, err := gitOutput(dir, "remote", "get-url", *remote)
		if err != nil || u == "" {
			return fmt.Errorf("the server clones the project from its repository, but %s has no remote %q – push it somewhere the server can reach and add the remote, or pass --git URL", dir, *remote)
		}
		git.URL = u
		if isLocalGitURL(u) {
			return fmt.Errorf("remote %q is %s, which the server cannot reach – pass --git URL", *remote, u)
		}
	}
	if git.Branch == "" {
		// symbolic-ref also names a branch without commits and fails on a detached HEAD,
		// where the server's default branch is the honest choice.
		if b, _ := gitOutput(dir, "symbolic-ref", "--quiet", "--short", "HEAD"); b != "" {
			git.Branch = b
		}
	}
	if *gitToken != "" {
		token := *gitToken
		if token == "-" {
			raw, err := c.readFileOrStdin("-")
			if err != nil {
				return err
			}
			token = strings.TrimSpace(string(raw))
		}
		git.Token = &token
	}
	if *dryRun {
		c.printf("%s does not exist yet. envoryx up would create it from %s", projectName, git.URL)
		if git.Branch != "" {
			c.printf(" (%s)", git.Branch)
		}
		c.printf(":\n")
		out, _ := manifest.Marshal(mf)
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if !strings.HasPrefix(line, "#") {
				c.printf("  %s\n", line)
			}
		}
		return nil
	}
	var missing []string
	for _, k := range mf.Secrets {
		if _, ok := secrets[k]; !ok {
			missing = append(missing, k)
		}
	}
	if err := c.askSecrets(missing, secrets); err != nil {
		return err
	}
	req := map[string]any{"yaml": string(raw), "name": projectName, "path": *path, "git": git, "secrets": secrets, "start": !*noStart}
	var created struct {
		Plan    manifestPlan   `json:"plan"`
		Project projectSummary `json:"project"`
	}
	c.printf("Creating %s from %s …\n", projectName, git.URL)
	if err := api.post(ctx, "/api/v1/projects/from-manifest", req, &created); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(created)
	}
	c.printf("Created %s (%s)\n", created.Project.Name, created.Project.Slug)
	c.printUp(created.Project, created.Plan, !*noStart)
	return nil
}

// manifestPlan is the server's comparison of a manifest with a project.
type manifestPlan struct {
	Changes []struct {
		Section string `json:"section"`
		Item    string `json:"item"`
		Action  string `json:"action"`
		From    string `json:"from"`
		To      string `json:"to"`
		Skipped string `json:"skipped"`
	} `json:"changes"`
	MissingSecrets []string `json:"missingSecrets"`
	InSync         bool     `json:"inSync"`
}

func (c *cli) printPlan(p manifestPlan, prune bool) {
	if len(p.Changes) == 0 {
		c.printf("  nothing to change\n")
		return
	}
	rows := make([][]string, 0, len(p.Changes))
	skipped := 0
	for _, ch := range p.Changes {
		mark := map[string]string{"add": "+", "change": "~", "remove": "-"}[ch.Action]
		subject := ch.Section
		if ch.Item != "" {
			subject += " " + ch.Item
		}
		detail := ch.To
		switch {
		case ch.From != "" && ch.To != "":
			detail = ch.From + " → " + ch.To
		case ch.Action == "remove":
			detail = ch.From
		}
		switch ch.Skipped {
		case "prune":
			detail = "kept – needs --prune"
			skipped++
		case "downgrade":
			detail = ch.From + " – not changed, the data format does not go back"
			skipped++
		}
		rows = append(rows, []string{"  " + mark, subject, detail})
	}
	c.table(nil, rows)
	if skipped > 0 && !prune {
		c.printf("  %d change%s kept back.\n", skipped, plural(skipped))
	}
}

func (c *cli) printUp(p projectSummary, plan manifestPlan, started bool) {
	if u := p.URL(); u != "" && started {
		c.printf("  %s\n", u)
	}
	if !started {
		c.printf("  Start it with: envoryx project start %s\n", p.Slug)
	}
	if len(plan.MissingSecrets) > 0 {
		c.printf("  Without a value: %s – set it with envoryx up --secret KEY=VALUE or under Environment in the web interface.\n", strings.Join(plan.MissingSecrets, ", "))
	}
}

// askSecrets asks for missing secret values in a terminal; elsewhere they stay empty and
// the result names them.
func (c *cli) askSecrets(keys []string, into map[string]string) error {
	f, ok := c.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	for _, k := range keys {
		if _, done := into[k]; done {
			continue
		}
		fmt.Fprintf(c.errOut, "%s (secret, empty to set it later): ", k)
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(c.errOut)
		if err != nil {
			return fmt.Errorf("reading %s: %w", k, err)
		}
		if v := strings.TrimSpace(string(raw)); v != "" {
			into[k] = v
		}
	}
	return nil
}

// lookupProject finds the project a manifest belongs to by name or slug; found is false
// when there is none.
func (c *cli) lookupProject(ctx context.Context, name string) (projectSummary, bool, error) {
	projects, err := c.listProjects(ctx)
	if err != nil {
		return projectSummary{}, false, err
	}
	slug := validate.Slugify(name)
	for _, p := range projects {
		if strings.EqualFold(p.Name, name) || p.Slug == slug {
			return p, true, nil
		}
	}
	return projectSummary{}, false, nil
}

// findManifest returns the manifest to use: the given file, or envoryx.yml in the
// current directory or the nearest parent that has one.
func findManifest(file string) (string, error) {
	if file != "" {
		abs, err := filepath.Abs(file)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", err
		}
		return abs, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, manifest.FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		// A repository root ends the search: a manifest above it belongs to something else.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no %s here or above – write one with \"envoryx project manifest <project> -o %s\" or by hand", manifest.FileName, manifest.FileName)
}

// gitOutput runs git in dir and returns its trimmed output.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("git is not installed – pass --git URL")
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isLocalGitURL reports a remote that only exists on this machine.
func isLocalGitURL(u string) bool {
	return strings.HasPrefix(u, "/") || strings.HasPrefix(u, "file://") || strings.HasPrefix(u, ".") || (len(u) > 1 && u[1] == ':')
}

const manifestUsage = `Usage: envoryx project manifest <project> [flags]

Prints the project as an envoryx.yml – commit it, and "envoryx up" in a clone brings the
same project up again.

  -o FILE    write it to FILE instead of printing it
  --write    save it as envoryx.yml in the project directory on the server
`

// projectManifest exports a project's manifest.
func (c *cli) projectManifest(ctx context.Context, args []string) error {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		fmt.Fprint(c.errOut, manifestUsage)
		return nil
	}
	fs := c.newFlags("project manifest")
	var (
		output = fs.String("o", "", "output file")
		write  = fs.Bool("write", false, "save into the project directory")
	)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	p, err := c.findProject(ctx, arg(pos, 0))
	if err != nil {
		return err
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	var body struct {
		YAML string `json:"yaml"`
	}
	if *write {
		if err := api.do(ctx, http.MethodPut, projectPath(p.ID, "manifest", "file"), nil, nil, &body); err != nil {
			return err
		}
		c.printf("Saved %s in %s on the server.\n", manifest.FileName, p.Path)
		return nil
	}
	if err := api.get(ctx, projectPath(p.ID, "manifest"), nil, &body); err != nil {
		return err
	}
	if *output != "" {
		if err := os.WriteFile(*output, []byte(body.YAML), 0o644); err != nil {
			return err
		}
		c.printf("Wrote %s\n", *output)
		return nil
	}
	fmt.Fprint(c.out, body.YAML)
	return nil
}
