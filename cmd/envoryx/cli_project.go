// Envoryx - Docker-native development environments for Unraid and Linux.
// Copyright (c) 2026 Stefan Mertens
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
)

func (c *cli) projectCommand(ctx context.Context, args []string) error {
	cmd, rest := splitCommand(args)
	switch cmd {
	case "help", "-h", "--help":
		return errCLIUsage
	case "", "list", "ls":
		return c.projectList(ctx, rest)
	case "show", "info":
		return c.projectShow(ctx, rest)
	case "create", "new":
		return c.projectCreate(ctx, rest)
	case "duplicate", "copy":
		return c.projectDuplicate(ctx, rest)
	case "start", "stop", "restart":
		return c.projectTransition(ctx, cmd, rest)
	case "delete", "rm":
		return c.projectDelete(ctx, rest)
	case "logs":
		return c.projectLogs(ctx, rest)
	case "exec":
		return c.projectExec(ctx, rest)
	case "run":
		return c.projectRun(ctx, rest)
	default:
		return usagef("unknown project command %q", cmd)
	}
}

func (c *cli) projectList(ctx context.Context, args []string) error {
	fs := c.newFlags("project list")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	if c.json {
		api, err := c.connect()
		if err != nil {
			return err
		}
		return c.printRaw(ctx, api, "/api/v1/projects", nil, "projects")
	}
	projects, err := c.listProjects(ctx)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		c.printf("No projects yet.\n")
		return nil
	}
	rows := make([][]string, 0, len(projects))
	for _, p := range projects {
		rows = append(rows, []string{p.Name, p.Slug, projectState(p), p.URL()})
	}
	c.table([]string{"NAME", "SLUG", "STATE", "URL"}, rows)
	return nil
}

// projectState is the line the list shows: the running state, plus what is holding the
// project up when something is wrong.
func projectState(p projectSummary) string {
	state := p.Status.State
	if state == "" {
		state = p.Lifecycle
	}
	if p.Lifecycle != "" && p.Lifecycle != "ready" {
		state += " (" + p.Lifecycle + ")"
	}
	if n := len(p.Status.Warnings); n > 0 {
		state += fmt.Sprintf(" · %d warning%s", n, plural(n))
	}
	return state
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (c *cli) projectShow(ctx context.Context, args []string) error {
	fs := c.newFlags("project show")
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
	if c.json {
		// Ask for the project itself: the list leaves out env and the service config.
		api, err := c.connect()
		if err != nil {
			return err
		}
		return c.printRaw(ctx, api, projectPath(p.ID), nil, "project")
	}
	c.printf("%s (%s)\n", p.Name, p.Slug)
	c.printf("  State     %s\n", projectState(p))
	if u := p.URL(); u != "" {
		c.printf("  URL       %s\n", u)
	}
	if p.DevHostname != "" {
		c.printf("  Dev URL   https://%s\n", p.DevHostname)
	}
	c.printf("  Path      %s\n", p.Path)
	if p.Git.URL != "" {
		c.printf("  Git       %s (%s)\n", p.Git.URL, p.Git.Branch)
	}
	if p.LastError != "" {
		c.printf("  Error     %s\n", p.LastError)
	}
	for _, w := range p.Status.Warnings {
		c.printf("  Warning   %s\n", w)
	}
	c.printf("\n")
	rows := make([][]string, 0, len(p.Status.Services))
	for _, s := range p.Status.Services {
		state := s.State
		if s.Health != "" {
			state += " (" + s.Health + ")"
		}
		// Docker publishes a port once per address family; the reader wants it once.
		var ports []string
		for _, port := range s.Ports {
			mapping := fmt.Sprintf("%d→%d", port.HostPort, port.ContainerPort)
			if !slices.Contains(ports, mapping) {
				ports = append(ports, mapping)
			}
		}
		rows = append(rows, []string{s.Kind, s.Version, state, strings.Join(ports, " ")})
	}
	c.table([]string{"SERVICE", "VERSION", "STATE", "PORTS"}, rows)
	return nil
}

func (c *cli) projectTransition(ctx context.Context, action string, args []string) error {
	fs := c.newFlags("project " + action)
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
		Project projectSummary `json:"project"`
	}
	// Starting pulls images and recreates containers when something changed, so this can
	// take a while; the server answers once the project has reached its new state.
	if err := api.post(ctx, projectPath(p.ID, action), struct{}{}, &body); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(body.Project)
	}
	c.printf("%s: %s\n", body.Project.Name, projectState(body.Project))
	return nil
}

func (c *cli) projectDelete(ctx context.Context, args []string) error {
	fs := c.newFlags("project delete")
	yes := fs.Bool("yes", false, "confirm")
	files := fs.Bool("delete-files", false, "delete the project directory too")
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
	if !*yes {
		what := "its containers, database and backups"
		if *files {
			what = "its containers, database, backups and the files in " + p.Path
		}
		return fmt.Errorf("deleting %s removes %s; confirm with --yes", p.Slug, what)
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	// The API asks for the slug as the confirmation; --yes is what the user confirmed.
	body := map[string]any{"confirm": p.Slug, "deleteFiles": *files}
	if err := api.do(ctx, http.MethodDelete, projectPath(p.ID), nil, body, nil); err != nil {
		return err
	}
	c.printf("Deleted %s.\n", p.Slug)
	return nil
}

// ---- duplicate ---------------------------------------------------------------

// duplicateRequest mirrors the API's duplicate body. The parts are pointers because the
// server's default is "everything the original has": only a part switched off is sent.
type duplicateRequest struct {
	Name                string `json:"name"`
	Path                string `json:"path,omitempty"`
	Files               *bool  `json:"files,omitempty"`
	Database            *bool  `json:"database,omitempty"`
	Storage             *bool  `json:"storage,omitempty"`
	Workers             *bool  `json:"workers,omitempty"`
	Git                 *bool  `json:"git,omitempty"`
	IncludeDependencies bool   `json:"includeDependencies,omitempty"`
	Start               bool   `json:"start,omitempty"`
}

const duplicateUsage = `Usage: envoryx project duplicate <project> <new name> [flags]

Copies a project with its configuration: runtimes, services, environment, workers and
the repository binding. The copy gets its own directory, host ports and containers, and
keeps the original's database credentials, so a checked-in .env keeps working.

  --path DIR           directory for the copy (default: the slug of the new name)
  --no-files           do not copy the project directory
  --no-database        do not copy the contents of the database
  --no-storage         do not copy the objects of the bucket
  --no-workers         do not copy the worker definitions
  --no-git             do not copy the repository binding
  --with-dependencies  copy vendor/, node_modules/ and the other caches too
  --start              start the copy once it is ready

Extra domains and the backup schedule are never copied.
`

func (c *cli) projectDuplicate(ctx context.Context, args []string) error {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		fmt.Fprint(c.errOut, duplicateUsage)
		return nil
	}
	fs := c.newFlags("project duplicate")
	var (
		path     = fs.String("path", "", "directory for the copy")
		noFiles  = fs.Bool("no-files", false, "do not copy the project directory")
		noDB     = fs.Bool("no-database", false, "do not copy the database")
		noStore  = fs.Bool("no-storage", false, "do not copy the bucket")
		noWork   = fs.Bool("no-workers", false, "do not copy the workers")
		noGit    = fs.Bool("no-git", false, "do not copy the repository binding")
		withDeps = fs.Bool("with-dependencies", false, "copy vendor/, node_modules/ …")
		start    = fs.Bool("start", false, "start the copy")
	)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	// The rest of the line is the new name, so it does not have to be quoted.
	name := ""
	if len(pos) > 1 {
		name = strings.TrimSpace(strings.Join(pos[1:], " "))
	}
	if name == "" {
		fmt.Fprint(c.errOut, duplicateUsage)
		return usagef("which project should be copied, and what should the copy be called?")
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	src, err := c.findProject(ctx, pos[0])
	if err != nil {
		return err
	}
	off := false
	req := duplicateRequest{Name: name, Path: *path, IncludeDependencies: *withDeps, Start: *start}
	for _, part := range []struct {
		skip bool
		to   **bool
	}{{*noFiles, &req.Files}, {*noDB, &req.Database}, {*noStore, &req.Storage}, {*noWork, &req.Workers}, {*noGit, &req.Git}} {
		if part.skip {
			*part.to = &off
		}
	}
	api, err := c.connect()
	if err != nil {
		return err
	}
	var body struct {
		Project projectSummary `json:"project"`
	}
	// Copying files and streaming a dump takes as long as the project is big.
	if err := api.post(ctx, projectPath(src.ID, "duplicate"), req, &body); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(body.Project)
	}
	c.printf("Copied %s to %s (%s)\n", src.Slug, body.Project.Name, body.Project.Slug)
	if u := body.Project.URL(); u != "" {
		c.printf("  %s\n", u)
	}
	if !req.Start {
		c.printf("  Start it with: envoryx project start %s\n", body.Project.Slug)
	}
	return nil
}

// ---- create ------------------------------------------------------------------

// createRequest mirrors the API's create body. The CLI builds it from flags, or reads it
// whole from --from-json for the settings that have no flag.
type createRequest struct {
	Name     string          `json:"name"`
	Path     string          `json:"path,omitempty"`
	Docroot  string          `json:"docroot,omitempty"`
	PHP      *phpSpec        `json:"php,omitempty"`
	Node     *nodeSpec       `json:"node,omitempty"`
	Python   *pythonSpec     `json:"python,omitempty"`
	Database *databaseSpec   `json:"database,omitempty"`
	Redis    *extraSpec      `json:"redis,omitempty"`
	Mailpit  *extraSpec      `json:"mailpit,omitempty"`
	Storage  *extraSpec      `json:"storage,omitempty"`
	Git      *gitSpec        `json:"git,omitempty"`
	Env      []envSpec       `json:"env,omitempty"`
	Template string          `json:"template,omitempty"`
	Starter  bool            `json:"createStarter,omitempty"`
	Start    bool            `json:"start,omitempty"`
	Web      json.RawMessage `json:"web,omitempty"`
}

type phpSpec struct {
	Version string `json:"version,omitempty"`
}

type nodeSpec struct {
	Version   string `json:"version,omitempty"`
	DevServer bool   `json:"devServer,omitempty"`
	Preset    string `json:"preset,omitempty"`
}

type pythonSpec struct {
	Version string `json:"version,omitempty"`
	Server  bool   `json:"server,omitempty"`
	Preset  string `json:"preset,omitempty"`
	App     string `json:"app,omitempty"`
}

type databaseSpec struct {
	Type       string `json:"type,omitempty"`
	Version    string `json:"version,omitempty"`
	ExposePort bool   `json:"exposePort,omitempty"`
}

type extraSpec struct {
	Version string `json:"version,omitempty"`
}

type gitSpec struct {
	URL      string  `json:"url"`
	Branch   string  `json:"branch,omitempty"`
	Username string  `json:"username,omitempty"`
	Token    *string `json:"token,omitempty"`
}

type envSpec struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret,omitempty"`
}

const createUsage = `Usage: envoryx project create <name> [flags]

Runtimes (a project without any is a static site served by the web container):
  --php VERSION        PHP version, e.g. 8.4, or "default"
  --node VERSION       Node.js version; --node-dev-server runs the dev server,
                       --node-preset vite|next|nuxt|generic
  --python VERSION     Python version; --python-server runs the application server,
                       --python-preset django|flask|asgi|wsgi|module, --python-app NAME

Services:
  --database TYPE[:VERSION]   mysql, mariadb, postgres, mongodb …
  --expose-database           publish the database port on the host
  --redis, --mailpit, --storage

Files and repository:
  --path DIR           directory below the projects directory (default: the slug)
  --docroot DIR        document root inside the project
  --template ID        laravel, symfony, vite, next, nuxt, django, flask, fastapi …
  --starter            write starter files into an empty project
  --git URL            clone this repository
  --branch NAME        branch to check out
  --git-username NAME  user name for a private repository
  --git-token TOKEN    access token for a private repository ("-" reads it from stdin)

Other:
  --env KEY=VALUE      environment variable, repeatable (--secret-env for secrets)
  --start              start the project once it is created
  --from-json FILE     the whole create request as JSON ("-" reads stdin); flags
                       given alongside it win

The runtime versions Envoryx offers are listed by GET /api/v1/runtimes.
`

func (c *cli) projectCreate(ctx context.Context, args []string) error {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		fmt.Fprint(c.errOut, createUsage)
		return nil
	}
	fs := c.newFlags("project create")
	var (
		path       = fs.String("path", "", "project directory")
		docroot    = fs.String("docroot", "", "document root")
		php        = fs.String("php", "", "PHP version")
		node       = fs.String("node", "", "Node.js version")
		nodeDev    = fs.Bool("node-dev-server", false, "run the Node dev server")
		nodePreset = fs.String("node-preset", "", "vite, next, nuxt, generic")
		python     = fs.String("python", "", "Python version")
		pyServer   = fs.Bool("python-server", false, "run the Python application server")
		pyPreset   = fs.String("python-preset", "", "django, flask, asgi, wsgi, module")
		pyApp      = fs.String("python-app", "", "application module")
		database   = fs.String("database", "", "mysql, mariadb, postgres, mongodb[:version]")
		exposeDB   = fs.Bool("expose-database", false, "publish the database port")
		redis      = fs.Bool("redis", false, "add Redis")
		mailpit    = fs.Bool("mailpit", false, "add Mailpit")
		storage    = fs.Bool("storage", false, "add S3 storage")
		template   = fs.String("template", "", "project template")
		starter    = fs.Bool("starter", false, "write starter files")
		gitURL     = fs.String("git", "", "repository to clone")
		branch     = fs.String("branch", "", "branch")
		gitUser    = fs.String("git-username", "", "repository user name")
		gitToken   = fs.String("git-token", "", "repository token")
		start      = fs.Bool("start", false, "start the project")
		fromJSON   = fs.String("from-json", "", "create request as JSON")
		envVars    stringList
		secretVars stringList
	)
	fs.Var(&envVars, "env", "KEY=VALUE, repeatable")
	fs.Var(&secretVars, "secret-env", "KEY=VALUE stored as a secret, repeatable")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	var req createRequest
	if *fromJSON != "" {
		raw, err := c.readFileOrStdin(*fromJSON)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("%s: %w", *fromJSON, err)
		}
	}
	if name := arg(pos, 0); name != "" {
		req.Name = name
	}
	if req.Name == "" {
		fmt.Fprint(c.errOut, createUsage)
		return usagef("which name should the project have?")
	}
	if *path != "" {
		req.Path = *path
	}
	if *docroot != "" {
		req.Docroot = *docroot
	}
	if *template != "" {
		req.Template = *template
	}
	if *php != "" {
		req.PHP = &phpSpec{Version: runtimeVersion(*php)}
	}
	if *node != "" || *nodeDev || *nodePreset != "" {
		req.Node = &nodeSpec{Version: runtimeVersion(*node), DevServer: *nodeDev, Preset: *nodePreset}
	}
	if *python != "" || *pyServer || *pyPreset != "" || *pyApp != "" {
		req.Python = &pythonSpec{Version: runtimeVersion(*python), Server: *pyServer, Preset: *pyPreset, App: *pyApp}
	}
	if *database != "" {
		kind, version, _ := strings.Cut(*database, ":")
		req.Database = &databaseSpec{Type: kind, Version: runtimeVersion(version), ExposePort: *exposeDB}
	}
	if *redis {
		req.Redis = &extraSpec{}
	}
	if *mailpit {
		req.Mailpit = &extraSpec{}
	}
	if *storage {
		req.Storage = &extraSpec{}
	}
	if *gitURL != "" || *branch != "" || *gitUser != "" || *gitToken != "" {
		if req.Git == nil {
			req.Git = &gitSpec{}
		}
		if *gitURL != "" {
			req.Git.URL = *gitURL
		}
		if *branch != "" {
			req.Git.Branch = *branch
		}
		if *gitUser != "" {
			req.Git.Username = *gitUser
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
			req.Git.Token = &token
		}
	}
	for _, kv := range envVars {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return usagef("--env expects KEY=VALUE, got %q", kv)
		}
		req.Env = append(req.Env, envSpec{Key: key, Value: value})
	}
	for _, kv := range secretVars {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return usagef("--secret-env expects KEY=VALUE, got %q", kv)
		}
		req.Env = append(req.Env, envSpec{Key: key, Value: value, IsSecret: true})
	}
	req.Starter = req.Starter || *starter
	req.Start = req.Start || *start

	api, err := c.connect()
	if err != nil {
		return err
	}
	ctx, cancel := c.context(ctx)
	defer cancel()
	var body struct {
		Project projectSummary `json:"project"`
	}
	// Creating pulls the images and can run a template's installer: minutes, not seconds.
	if err := api.post(ctx, "/api/v1/projects", req, &body); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(body.Project)
	}
	c.printf("Created %s (%s)\n", body.Project.Name, body.Project.Slug)
	if u := body.Project.URL(); u != "" {
		c.printf("  %s\n", u)
	}
	if !req.Start {
		c.printf("  Start it with: envoryx project start %s\n", body.Project.Slug)
	}
	return nil
}

// runtimeVersion maps the words people type for "whatever Envoryx recommends" to the
// empty version the API understands as its default.
func runtimeVersion(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "default", "latest", "auto":
		return ""
	default:
		return strings.TrimSpace(v)
	}
}

// stringList collects a flag that may be given more than once.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func (c *cli) readFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		return readAllLimited(c.stdin)
	}
	return os.ReadFile(path)
}
