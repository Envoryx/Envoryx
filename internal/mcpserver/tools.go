package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoryx/envoryx/internal/auth"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/project"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// ---- Shared output types -----------------------------------------------------------

type projectRef struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
}

type serviceOut struct {
	Kind    string `json:"kind"`
	Variant string `json:"variant,omitempty"`
	Version string `json:"version"`
	State   string `json:"state"`
	Health  string `json:"health,omitempty"`
}

type projectOut struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	State string `json:"state"`
	// Serves says what the project URL reaches: php, python (application server), node
	// (dev server) or static.
	Serves string `json:"serves"`
	URL    string `json:"url,omitempty"`
	// DevURL is the dev server's own host name while a Node dev server runs.
	DevURL string `json:"devUrl,omitempty"`
	// DirectURL bypasses the proxy: the web server's host port, or the application
	// container's when a Python server or Node dev server serves the project.
	DirectURL string       `json:"directUrl,omitempty"`
	Hostnames []string     `json:"hostnames"`
	Path      string       `json:"path"`
	Docroot   string       `json:"docroot,omitempty"`
	Services  []serviceOut `json:"services"`
	Warnings  []string     `json:"warnings,omitempty"`
	GitURL    string       `json:"gitUrl,omitempty"`
	GitBranch string       `json:"gitBranch,omitempty"`
}

func (s *Server) projectOut(ctx context.Context, v project.View) projectOut {
	p := v.Project
	out := projectOut{ID: p.ID, Name: p.Name, Slug: p.Slug, State: string(v.Status.State), Serves: project.Serves(p), Path: p.Path, Docroot: p.Docroot,
		Warnings: v.Status.Warnings, GitURL: p.Git.URL, GitBranch: p.Git.Branch, Hostnames: []string{}, Services: []serviceOut{}}
	hosts, err := s.d.Projects.ProjectHostnames(ctx, p)
	if err == nil {
		out.Hostnames = hosts
	}
	// The web container's port stays unpublished while an application server serves the
	// project, so the direct link is that container's host port then.
	directPort, ncfg, dev := v.HTTPPort, runtime.NodeConfig{}, false
	if svc := p.Service(store.ServiceNode); svc != nil && svc.Enabled && len(svc.Config) > 0 {
		dev = json.Unmarshal(svc.Config, &ncfg) == nil && ncfg.DevServer
	}
	switch out.Serves {
	case "node":
		directPort = ncfg.HostPort
	case "python":
		var pcfg runtime.PythonConfig
		if svc := p.Service(store.ServicePython); svc != nil && json.Unmarshal(svc.Config, &pcfg) == nil {
			directPort = pcfg.HostPort
		}
	}
	if directPort > 0 {
		host := ""
		if s.d.Links.PublicHost != nil {
			host = s.d.Links.PublicHost(ctx)
		}
		if host == "" {
			host = "<envoryx-host>"
		}
		out.DirectURL = fmt.Sprintf("http://%s:%d", host, directPort)
	}
	if len(out.Hostnames) > 0 {
		out.URL = s.proxiedURL(out.Hostnames[0])
	}
	if dev {
		out.DevURL = s.proxiedURL(project.DevHostname(p.Slug, s.d.Projects.BaseDomain(ctx)))
	}
	if out.URL == "" {
		out.URL = out.DirectURL
	}
	for _, st := range v.Status.Services {
		out.Services = append(out.Services, serviceOut{Kind: string(st.Kind), Variant: st.Variant, Version: st.Version, State: st.State, Health: st.Health})
	}
	return out
}

// proxiedURL builds the URL of a host name served by the embedded proxy ("" when the
// proxy publishes no port).
func (s *Server) proxiedURL(host string) string {
	switch {
	case s.d.Links.HTTPSPort == 443:
		return "https://" + host
	case s.d.Links.HTTPSPort > 0:
		return fmt.Sprintf("https://%s:%d", host, s.d.Links.HTTPSPort)
	case s.d.Links.HTTPPort == 80:
		return "http://" + host
	case s.d.Links.HTTPPort > 0:
		return fmt.Sprintf("http://%s:%d", host, s.d.Links.HTTPPort)
	}
	return ""
}

// toolErr converts manager errors into tool results the model can act on (instead of
// protocol-level errors).
func toolErr(err error) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
}

func readOnly(name, title, desc string) *mcp.Tool {
	return &mcp.Tool{Name: name, Title: title, Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}}
}

func mutating(name, title, desc string, idempotent bool) *mcp.Tool {
	f := false
	return &mcp.Tool{Name: name, Title: title, Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: &f, IdempotentHint: idempotent}}
}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_projects", "List projects", "List all Envoryx projects with state, URLs and services.")), s.listProjects)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("get_project", "Get project", "Details and live status of one project.")), s.getProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_runtimes", "List runtimes", "Available runtimes (PHP, Node.js, Python, web servers), database engines, services, PHP extension keys and templates for create_project.")), s.listRuntimes)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeAdmin, mutating("create_project", "Create project", "Create a new development environment (web server plus PHP, Python and/or Node.js, optional database, Redis, Mailpit, object storage, git clone or template). Returns the project including its URL.", false)), s.createProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeAdmin, mutating("duplicate_project", "Duplicate project", "Copy an existing project (shop → shop-test): configuration, environment, workers and git binding, optionally the files, the database contents and the objects of the bucket. The copy gets its own directory, host ports and containers and keeps the original's database credentials. Extra domains and the backup schedule are not copied.", false)), s.duplicateProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeAdmin, mutating("rename_project", "Rename project", "Rename a project and everything derived from its identifier: URL and host names, container, network and volume names, the project directory, the backups and – unless keepDataNames is set – the database, its login and the bucket. The containers are recreated, so the project is briefly unavailable; confirm must be the current identifier.", false)), s.renameProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("start_project", "Start project", "Start all containers of a project.", true)), s.startProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("stop_project", "Stop project", "Stop all containers of a project (data is kept).", true)), s.stopProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("restart_project", "Restart project", "Restart a project; also pulls updated runtime images.", true)), s.restartProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("get_logs", "Get logs", "Recent log lines of one project container (web, php, python, node, database, redis, mailpit).")), s.getLogs)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_actions", "List actions", "Runnable project actions (composer, artisan, npm …) and whether they are currently available.")), s.listActions)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("run_action", "Run action", "Run one action from list_actions inside the project (e.g. composer:install) and return its output. Waits for completion (up to 20 minutes).", false)), s.runAction)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_databases", "List databases", "Databases on the project's database server.")), s.listDatabases)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("create_database", "Create database", "Create an additional database on the project's database server (same credentials).", true)), s.createDatabase)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_backups", "List backups", "Backups of a project.")), s.listBackups)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("create_backup", "Create backup", "Create a backup (database dump + files + object storage + configuration) of a project.", false)), s.createBackup)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("add_domain", "Add domain", "Add an extra host name routed to the project by the embedded proxy.", true)), s.addDomain)
}

// ---- Projects ---------------------------------------------------------------------

type listProjectsIn struct{}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, _ listProjectsIn) (*mcp.CallToolResult, listProjectsOut, error) {
	views, err := s.visibleProjects(ctx)
	if err != nil {
		r, _ := toolErr(err)
		return r, listProjectsOut{}, nil
	}
	out := listProjectsOut{Projects: []projectOut{}}
	for _, v := range views {
		out.Projects = append(out.Projects, s.projectOut(ctx, v))
	}
	return nil, out, nil
}

func (s *Server) getProject(ctx context.Context, _ *mcp.CallToolRequest, in projectRef) (*mcp.CallToolResult, projectOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	return nil, s.projectOut(ctx, v), nil
}

type runtimeOut struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Versions    []string `json:"versions"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
}

type templateOut struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Runtime the template scaffolds for: php, node or python.
	Runtime             string                `json:"runtime"`
	Node                *runtime.NodeConfig   `json:"node,omitempty"`
	Python              *runtime.PythonConfig `json:"python,omitempty"`
	Docroot             string                `json:"docroot,omitempty"`
	RequiresDatabase    bool                  `json:"requiresDatabase"`
	RecommendedDatabase string                `json:"recommendedDatabase,omitempty"`
	Notes               string                `json:"notes,omitempty"`
}

type listRuntimesOut struct {
	Runtimes      []runtimeOut  `json:"runtimes"`
	PHPExtensions []string      `json:"phpExtensions"`
	Templates     []templateOut `json:"templates"`
}

func (s *Server) listRuntimes(_ context.Context, _ *mcp.CallToolRequest, _ listProjectsIn) (*mcp.CallToolResult, listRuntimesOut, error) {
	out := listRuntimesOut{Runtimes: []runtimeOut{}, PHPExtensions: []string{}, Templates: []templateOut{}}
	for _, t := range project.Templates() {
		out.Templates = append(out.Templates, templateOut{ID: t.ID, Name: t.Name, Description: t.Description, Runtime: t.Runtime, Node: t.Node, Python: t.Python, Docroot: t.Docroot, RequiresDatabase: t.RequiresDatabase, RecommendedDatabase: t.RecommendedDatabase, Notes: t.Notes})
	}
	for _, r := range s.d.Catalog.All() {
		if !r.Available {
			continue
		}
		ro := runtimeOut{Key: r.Key, Name: r.Name, Kind: r.Kind, Description: r.Description, Versions: []string{}}
		for _, v := range r.Versions {
			label := v.Version
			if v.Preview {
				label += " (preview)"
			}
			if v.EOL {
				label += " (eol)"
			}
			ro.Versions = append(ro.Versions, label)
			if v.Default {
				ro.Default = v.Version
			}
		}
		out.Runtimes = append(out.Runtimes, ro)
	}
	for _, e := range runtime.PHPExtensions() {
		if e.Available && !e.BuiltIn {
			out.PHPExtensions = append(out.PHPExtensions, e.Name)
		}
	}
	sort.Strings(out.PHPExtensions)
	return nil, out, nil
}

type createProjectIn struct {
	Name               string            `json:"name" jsonschema:"Display name, e.g. \"Shop API\". The slug and directory are derived from it."`
	Template           string            `json:"template,omitempty" jsonschema:"Scaffold an application: laravel, symfony, wordpress (PHP), vite, next, nuxt (Node.js; needs nodeVersion and phpVersion \"none\" for a Node-only project) or django, flask, fastapi (Python; needs pythonVersion and phpVersion \"none\"). See list_runtimes for details. Cannot be combined with gitUrl."`
	PHPVersion         string            `json:"phpVersion,omitempty" jsonschema:"PHP version such as 8.4 (default: the catalogue default). Use \"none\" for a Python, Node-only or static project."`
	WebServer          string            `json:"webServer,omitempty" jsonschema:"Web server: caddy (default), apache (mod_rewrite + .htaccess, e.g. for WordPress) or nginx."`
	PHPExtensions      []string          `json:"phpExtensions,omitempty" jsonschema:"PHP extensions to enable (keys from list_runtimes). Default: bcmath, gd, intl, opcache, pdo_mysql, zip."`
	Database           string            `json:"database,omitempty" jsonschema:"Database engine: mariadb, mysql or postgresql. Omit for no database."`
	DBVersion          string            `json:"databaseVersion,omitempty" jsonschema:"Database version (default: catalogue default)."`
	Redis              bool              `json:"redis,omitempty" jsonschema:"Add a Redis service."`
	Mailpit            bool              `json:"mailpit,omitempty" jsonschema:"Add Mailpit (SMTP catcher with web inbox)."`
	Storage            bool              `json:"storage,omitempty" jsonschema:"Add S3-compatible object storage with a bucket per project (S3_* and AWS_* variables injected)."`
	NodeVersion        string            `json:"nodeVersion,omitempty" jsonschema:"Add a Node.js container with this major version (e.g. 24): the project's runtime (dev server) or a toolchain for asset builds."`
	NodeDevServer      bool              `json:"nodeDevServer,omitempty" jsonschema:"Run the package.json dev script as the project's main process. Without PHP it is reachable at the project URL, always at <slug>-dev.<base domain>. Requires nodeVersion."`
	NodePreset         string            `json:"nodePreset,omitempty" jsonschema:"Dev-server preset: vite, next, nuxt or generic (default vite). Sets how host/port are passed and the default port."`
	NodeScript         string            `json:"nodeScript,omitempty" jsonschema:"package.json script the dev server runs (default dev)."`
	NodePort           int               `json:"nodePort,omitempty" jsonschema:"Port the dev server listens on inside the container (default: the preset's port, 5173 for vite, 3000 for next/nuxt)."`
	NodePackageManager string            `json:"nodePackageManager,omitempty" jsonschema:"npm (default), pnpm or yarn."`
	PythonVersion      string            `json:"pythonVersion,omitempty" jsonschema:"Add a Python container with this version (e.g. 3.13): the project's runtime (application server) or a tooling container (pip, uv, venv)."`
	PythonServer       bool              `json:"pythonServer,omitempty" jsonschema:"Run the application server of pythonPreset as the project's main process. Without PHP it is reachable at the project URL. Requires pythonVersion."`
	PythonPreset       string            `json:"pythonPreset,omitempty" jsonschema:"Server preset: django (manage.py runserver), flask (flask run), asgi (uvicorn: FastAPI, Starlette …), wsgi (gunicorn) or module (python -m). Default django."`
	PythonApp          string            `json:"pythonApp,omitempty" jsonschema:"Import path of the application, module:attribute (e.g. main:app for FastAPI, app:app for Flask, config.wsgi:application for Django in production mode)."`
	PythonPort         int               `json:"pythonPort,omitempty" jsonschema:"Port the server listens on inside the container (default: the preset's port, 8000 or 5000 for flask)."`
	PythonMode         string            `json:"pythonMode,omitempty" jsonschema:"dev (default, reload + debug) or production (gunicorn/uvicorn without reload)."`
	Docroot            string            `json:"docroot,omitempty" jsonschema:"Document root relative to the project directory, e.g. public. Default: project root (public/ for Laravel/Symfony)."`
	GitURL             string            `json:"gitUrl,omitempty" jsonschema:"Repository to clone into the new project (https://… or git@…)."`
	GitBranch          string            `json:"gitBranch,omitempty" jsonschema:"Branch to check out."`
	Env                map[string]string `json:"env,omitempty" jsonschema:"Environment variables for the application containers."`
	Start              *bool             `json:"start,omitempty" jsonschema:"Start the project after creation (default true)."`
}

func (s *Server) createProject(ctx context.Context, _ *mcp.CallToolRequest, in createProjectIn) (*mcp.CallToolResult, projectOut, error) {
	req := project.CreateRequest{Name: strings.TrimSpace(in.Name), Docroot: strings.TrimSpace(in.Docroot), Template: strings.ToLower(strings.TrimSpace(in.Template)), CreateStarter: true, Start: true}
	if in.Start != nil {
		req.Start = *in.Start
	}
	req.Web = project.WebRequest{Type: strings.ToLower(strings.TrimSpace(in.WebServer))}
	if !strings.EqualFold(in.PHPVersion, "none") {
		cfg := runtime.DefaultPHPConfig()
		if in.PHPExtensions != nil {
			cfg.Extensions = in.PHPExtensions
		}
		req.PHP = &project.PHPRequest{Version: strings.TrimSpace(in.PHPVersion), Config: cfg}
	}
	if db := strings.ToLower(strings.TrimSpace(in.Database)); db != "" {
		req.Database = &project.DatabaseRequest{Type: db, Version: strings.TrimSpace(in.DBVersion)}
	}
	if in.Redis {
		req.Redis = &project.ExtraRequest{}
	}
	if in.Mailpit {
		req.Mailpit = &project.ExtraRequest{}
	}
	if in.Storage {
		req.Storage = &project.StorageRequest{}
	}
	if v := strings.TrimSpace(in.NodeVersion); v != "" {
		req.Node = &project.NodeRequest{Version: v, Config: runtime.NodeConfig{
			DevServer: in.NodeDevServer, Preset: strings.ToLower(strings.TrimSpace(in.NodePreset)),
			Script: strings.TrimSpace(in.NodeScript), Port: in.NodePort, PackageManager: strings.ToLower(strings.TrimSpace(in.NodePackageManager)),
		}}
	}
	if v := strings.TrimSpace(in.PythonVersion); v != "" {
		req.Python = &project.PythonRequest{Version: v, Config: runtime.PythonConfig{
			Server: in.PythonServer, Mode: strings.ToLower(strings.TrimSpace(in.PythonMode)), Preset: strings.ToLower(strings.TrimSpace(in.PythonPreset)),
			App: strings.TrimSpace(in.PythonApp), Port: in.PythonPort,
		}}
	}
	if u := strings.TrimSpace(in.GitURL); u != "" {
		req.Git = &project.GitRequest{URL: u, Branch: strings.TrimSpace(in.GitBranch)}
		req.CreateStarter = false
	}
	keys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		req.Env = append(req.Env, project.EnvVarRequest{Key: k, Value: in.Env[k]})
	}
	if p, _ := auth.PrincipalFrom(ctx); p.Restricted() {
		r, _ := toolErr(fmt.Errorf("%w: this token is limited to particular projects and cannot create new ones", auth.ErrForbidden))
		return r, projectOut{}, nil
	}
	v, err := s.d.Projects.Create(ctx, req)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	return nil, s.projectOut(ctx, v), nil
}

type duplicateProjectIn struct {
	Project  string `json:"project" jsonschema:"Project to copy: id, slug or name"`
	Name     string `json:"name" jsonschema:"Display name of the copy, e.g. \"Shop Test\". Slug and directory are derived from it."`
	Path     string `json:"path,omitempty" jsonschema:"Directory of the copy below the projects directory (default: its slug)."`
	Files    *bool  `json:"files,omitempty" jsonschema:"Copy the project directory (default true). vendor/, node_modules/ and other caches are left out unless includeDependencies is set."`
	Database *bool  `json:"database,omitempty" jsonschema:"Copy the contents of the database (default true; ignored when the project has none)."`
	Storage  *bool  `json:"storage,omitempty" jsonschema:"Copy the objects of the bucket (default true; ignored without object storage)."`
	Workers  *bool  `json:"workers,omitempty" jsonschema:"Copy the worker definitions (default true)."`
	Git      *bool  `json:"git,omitempty" jsonschema:"Copy the repository binding (default true)."`

	IncludeDependencies bool  `json:"includeDependencies,omitempty" jsonschema:"Copy vendor/, node_modules/ and the other regenerable directories too."`
	Start               *bool `json:"start,omitempty" jsonschema:"Start the copy once it is ready (default false)."`
}

func (s *Server) duplicateProject(ctx context.Context, _ *mcp.CallToolRequest, in duplicateProjectIn) (*mcp.CallToolResult, projectOut, error) {
	if p, _ := auth.PrincipalFrom(ctx); p.Restricted() {
		r, _ := toolErr(fmt.Errorf("%w: this token is limited to particular projects and cannot create new ones", auth.ErrForbidden))
		return r, projectOut{}, nil
	}
	src, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	on := func(b *bool) bool { return b == nil || *b }
	req := project.DuplicateRequest{
		Name: strings.TrimSpace(in.Name), Path: strings.TrimSpace(in.Path),
		Files: on(in.Files), IncludeDependencies: in.IncludeDependencies,
		Database: on(in.Database), Storage: on(in.Storage), Workers: on(in.Workers), Git: on(in.Git),
		Start: in.Start != nil && *in.Start,
	}
	v, err := s.d.Projects.Duplicate(ctx, src.Project.ID, req)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	return nil, s.projectOut(ctx, v), nil
}

type renameProjectIn struct {
	Project string `json:"project" jsonschema:"Project to rename: id, slug or name"`
	Name    string `json:"name" jsonschema:"New display name, e.g. \"Acme Blog\". Slug, host names and container names are derived from it."`
	Confirm string `json:"confirm" jsonschema:"The project's current identifier (slug), as confirmation."`
	Path    string `json:"path,omitempty" jsonschema:"New directory below the projects directory. Default: follows the new identifier as long as the old directory matched the old one."`

	KeepDataNames bool `json:"keepDataNames,omitempty" jsonschema:"Leave the database, its login and the bucket under their current names; everything else still moves."`
}

func (s *Server) renameProject(ctx context.Context, _ *mcp.CallToolRequest, in renameProjectIn) (*mcp.CallToolResult, projectOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	res, err := s.d.Projects.Rename(ctx, v.Project.ID, project.RenameRequest{
		Name: strings.TrimSpace(in.Name), Path: strings.TrimSpace(in.Path),
		Confirm: strings.TrimSpace(in.Confirm), KeepDataNames: in.KeepDataNames,
	})
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	return nil, s.projectOut(ctx, res.View), nil
}

func (s *Server) transition(ctx context.Context, ref string, op func(context.Context, string) (project.View, error)) (*mcp.CallToolResult, projectOut, error) {
	v, err := s.resolve(ctx, ref)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	v, err = op(ctx, v.Project.ID)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	return nil, s.projectOut(ctx, v), nil
}

func (s *Server) startProject(ctx context.Context, _ *mcp.CallToolRequest, in projectRef) (*mcp.CallToolResult, projectOut, error) {
	return s.transition(ctx, in.Project, s.d.Projects.Start)
}

func (s *Server) stopProject(ctx context.Context, _ *mcp.CallToolRequest, in projectRef) (*mcp.CallToolResult, projectOut, error) {
	return s.transition(ctx, in.Project, s.d.Projects.Stop)
}

func (s *Server) restartProject(ctx context.Context, _ *mcp.CallToolRequest, in projectRef) (*mcp.CallToolResult, projectOut, error) {
	return s.transition(ctx, in.Project, s.d.Projects.Restart)
}

// ---- Logs and actions --------------------------------------------------------------

type getLogsIn struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
	Service string `json:"service,omitempty" jsonschema:"Container: web, php, python, node, database, redis, mailpit or storage (default: the application container (php, else python, else node), else web)"`
	Tail    int    `json:"tail,omitempty" jsonschema:"Number of lines (default 200, max 2000)"`
}

type logLineOut struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

type getLogsOut struct {
	Service string       `json:"service"`
	Lines   []logLineOut `json:"lines"`
}

func (s *Server) getLogs(ctx context.Context, _ *mcp.CallToolRequest, in getLogsIn) (*mcp.CallToolResult, getLogsOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, getLogsOut{}, nil
	}
	kind := store.ServiceKind(strings.ToLower(strings.TrimSpace(in.Service)))
	if kind == "" {
		kind = store.ServiceWeb
		if app, ok := project.AppKind(v.Project); ok {
			kind = app
		}
	}
	switch kind {
	case store.ServiceWeb, store.ServicePHP, store.ServicePython, store.ServiceNode, store.ServiceDatabase, store.ServiceRedis, store.ServiceMailpit, store.ServiceStorage:
	default:
		r, _ := toolErr(fmt.Errorf("%w: unknown service %q", validate.ErrInvalid, in.Service))
		return r, getLogsOut{}, nil
	}
	n := in.Tail
	if n <= 0 {
		n = 200
	}
	if n > 2000 {
		n = 2000
	}
	lines, err := s.d.Projects.TailLogs(ctx, v.Project.ID, kind, n)
	if err != nil {
		r, _ := toolErr(err)
		return r, getLogsOut{}, nil
	}
	out := getLogsOut{Service: string(kind), Lines: []logLineOut{}}
	for _, l := range lines {
		out.Lines = append(out.Lines, logLineOut{Time: l.Time, Stream: l.Stream, Text: l.Text})
	}
	return nil, out, nil
}

type actionOut struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Service     string `json:"service"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

type listActionsOut struct {
	Actions []actionOut `json:"actions"`
}

func (s *Server) listActions(ctx context.Context, _ *mcp.CallToolRequest, in projectRef) (*mcp.CallToolResult, listActionsOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, listActionsOut{}, nil
	}
	infos, err := s.d.Projects.ListActions(ctx, v.Project.ID)
	if err != nil {
		r, _ := toolErr(err)
		return r, listActionsOut{}, nil
	}
	out := listActionsOut{Actions: []actionOut{}}
	for _, a := range infos {
		out.Actions = append(out.Actions, actionOut{ID: a.ID, Group: a.Group, Label: a.Label, Description: a.Description, Service: string(a.Service), Available: a.Available, Reason: a.Reason, Destructive: a.Destructive})
	}
	return nil, out, nil
}

type runActionIn struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
	Action  string `json:"action" jsonschema:"Action id from list_actions, e.g. composer:install"`
}

type runActionOut struct {
	Action   string `json:"action"`
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
	// Truncated is true when only the last part of the output is returned.
	Truncated bool `json:"truncated,omitempty"`
}

const (
	actionTimeout   = 20 * time.Minute
	actionOutputMax = 48 << 10
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Za-z0-9]|\r`)

func (s *Server) runAction(ctx context.Context, _ *mcp.CallToolRequest, in runActionIn) (*mcp.CallToolResult, runActionOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, runActionOut{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	term, action, release, err := s.d.Projects.RunAction(ctx, v.Project.ID, strings.TrimSpace(in.Action), 200, 50)
	if err != nil {
		r, _ := toolErr(err)
		return r, runActionOut{}, nil
	}
	defer release()
	output, truncated := collectOutput(ctx, term)
	_ = term.Close()
	code, err := term.ExitCode(context.Background())
	if err != nil {
		code = -1
	}
	out := runActionOut{Action: action.ID, Command: strings.Join(action.Cmd, " "), ExitCode: code, Output: output, Truncated: truncated}
	if ctx.Err() != nil {
		r, _ := toolErr(fmt.Errorf("action %s timed out after %s; partial output:\n%s", action.ID, actionTimeout, output))
		return r, out, nil
	}
	if code != 0 {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s exited with code %d\n%s", out.Command, code, output)}}}, out, nil
	}
	return nil, out, nil
}

// collectOutput reads the terminal until EOF, keeping only the last actionOutputMax
// bytes and stripping terminal control sequences.
func collectOutput(ctx context.Context, term docker.Terminal) (string, bool) {
	var buf []byte
	truncated := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := make([]byte, 8<<10)
		for {
			n, err := term.Output().Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
				if len(buf) > actionOutputMax {
					buf = buf[len(buf)-actionOutputMax:]
					truncated = true
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		_ = term.Close()
		<-done
	}
	return strings.TrimSpace(ansiRe.ReplaceAllString(string(buf), "")), truncated
}

// ---- Databases, backups, domains ------------------------------------------------------

type listDatabasesOut struct {
	Engine    string   `json:"engine"`
	Version   string   `json:"version"`
	Databases []string `json:"databases"`
}

func (s *Server) listDatabases(ctx context.Context, _ *mcp.CallToolRequest, in projectRef) (*mcp.CallToolResult, listDatabasesOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	info, err := s.d.Projects.DatabaseInfo(ctx, v.Project.ID)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	names, err := s.d.Projects.ListDatabases(ctx, v.Project.ID)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	if names == nil {
		names = []string{}
	}
	return nil, listDatabasesOut{Engine: info.Type, Version: info.Version, Databases: names}, nil
}

type createDatabaseIn struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
	Name    string `json:"name" jsonschema:"Database name: lower-case letters, digits, underscores"`
}

func (s *Server) createDatabase(ctx context.Context, req *mcp.CallToolRequest, in createDatabaseIn) (*mcp.CallToolResult, listDatabasesOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	if err := s.d.Projects.CreateDatabase(ctx, v.Project.ID, strings.TrimSpace(in.Name)); err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	return s.listDatabases(ctx, req, projectRef{Project: v.Project.ID})
}

type backupOut struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
	Note      string    `json:"note,omitempty"`
}

type listBackupsOut struct {
	Backups []backupOut `json:"backups"`
}

func toBackup(b project.BackupInfo) backupOut {
	return backupOut{ID: b.ID, Kind: b.Kind, SizeBytes: b.SizeBytes, CreatedAt: b.CreatedAt, Note: b.Meta.Note}
}

func (s *Server) listBackups(ctx context.Context, _ *mcp.CallToolRequest, in projectRef) (*mcp.CallToolResult, listBackupsOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, listBackupsOut{}, nil
	}
	list, err := s.d.Projects.ListBackups(ctx, v.Project.ID)
	if err != nil {
		r, _ := toolErr(err)
		return r, listBackupsOut{}, nil
	}
	out := listBackupsOut{Backups: []backupOut{}}
	for _, b := range list {
		out.Backups = append(out.Backups, toBackup(b))
	}
	return nil, out, nil
}

type createBackupIn struct {
	Project             string `json:"project" jsonschema:"Project id, slug or name"`
	Note                string `json:"note,omitempty" jsonschema:"Short note stored with the backup"`
	IncludeDependencies bool   `json:"includeDependencies,omitempty" jsonschema:"Keep vendor/ and node_modules/ in the file archive (default false)"`
}

func (s *Server) createBackup(ctx context.Context, _ *mcp.CallToolRequest, in createBackupIn) (*mcp.CallToolResult, backupOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, backupOut{}, nil
	}
	b, err := s.d.Projects.CreateBackup(ctx, v.Project.ID, project.BackupOptions{Database: true, Files: true, Storage: true, IncludeDependencies: in.IncludeDependencies, Note: strings.TrimSpace(in.Note)})
	if err != nil {
		r, _ := toolErr(err)
		return r, backupOut{}, nil
	}
	return nil, toBackup(b), nil
}

type addDomainIn struct {
	Project  string `json:"project" jsonschema:"Project id, slug or name"`
	Hostname string `json:"hostname" jsonschema:"Host name such as shop.local (no wildcard)"`
}

func (s *Server) addDomain(ctx context.Context, _ *mcp.CallToolRequest, in addDomainIn) (*mcp.CallToolResult, projectOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	if _, err := s.d.Projects.AddDomain(ctx, v.Project.ID, in.Hostname); err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	return nil, s.projectOut(ctx, v), nil
}
