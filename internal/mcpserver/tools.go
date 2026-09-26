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
	"github.com/envoryx/envoryx/internal/logs"
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
	mcp.AddTool(s.mcp, s.tool(auth.ScopeAdmin, mutating("create_project", "Create project", "Create a new development environment (web server plus PHP, Python and/or Node.js, optional database, Redis, Memcached, Mailpit, RabbitMQ, Meilisearch, Typesense, OpenSearch, Ollama, object storage, git clone or template). Returns the project including its URL.", false)), s.createProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeAdmin, mutating("duplicate_project", "Duplicate project", "Copy an existing project (shop → shop-test): configuration, environment, workers and git binding, optionally the files, the database contents and the objects of the bucket. The copy gets its own directory, host ports and containers and keeps the original's database credentials. Extra domains and the backup schedule are not copied.", false)), s.duplicateProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeAdmin, mutating("rename_project", "Rename project", "Rename a project and everything derived from its identifier: URL and host names, container, network and volume names, the project directory, the backups and – unless keepDataNames is set – the database, its login and the bucket. The containers are recreated, so the project is briefly unavailable; confirm must be the current identifier.", false)), s.renameProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("start_project", "Start project", "Start all containers of a project.", true)), s.startProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("stop_project", "Stop project", "Stop all containers of a project (data is kept).", true)), s.stopProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("restart_project", "Restart project", "Restart a project; also pulls updated runtime images.", true)), s.restartProject)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("get_logs", "Get logs", "Log lines of one project container (web, php, python, node, database, redis, memcached, mailpit, rabbitmq, meilisearch, typesense, opensearch, opensearch-dashboards, ollama, or db-<name> for an additional database), optionally limited to a time range, a search text or warnings/errors. Each line carries the level Envoryx guesses from its text.")), s.getLogs)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("get_log_stats", "Get log statistics", "Error frequency of one project container over a time range: lines, warnings and errors per time slot and the most frequent errors and warnings, grouped with numbers and ids masked.")), s.getLogStats)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_actions", "List actions", "Runnable project actions (composer, artisan, npm …) and whether they are currently available.")), s.listActions)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("run_action", "Run action", "Run one action from list_actions inside the project (e.g. composer:install) and return its output. Waits for completion (up to 20 minutes).", false)), s.runAction)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_databases", "List databases", "The project's database servers (the primary, reached as host \"database\", and additional ones reached by their name) and the databases on one of them.")), s.listDatabases)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("create_database", "Create database", "Create a database on one of the project's database servers (same credentials).", true)), s.createDatabase)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_backups", "List backups", "Backups of a project.")), s.listBackups)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("create_backup", "Create backup", "Create a backup (database dump + files + object storage + configuration) of a project.", false)), s.createBackup)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeRead, readOnly("list_snapshots", "List snapshots", "Snapshots of one database of a project: the dumps that hold that database and nothing else.")), s.listSnapshots)
	mcp.AddTool(s.mcp, s.tool(auth.ScopeOperate, mutating("create_snapshot", "Create snapshot", "Dump one database of the project and nothing else – what to do before a migration or a mass update, so the state before it can be put back. Works on a stopped project too. Only the ten newest snapshots of a database are kept.", false)), s.createSnapshot)
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
	Template           string            `json:"template,omitempty" jsonschema:"Scaffold an application: laravel, symfony, wordpress, drupal, typo3, shopware, craft (PHP; the CMS ones need a database and are installed afterwards with run_action: drush:site-install, typo3:setup, shopware:install, craft:install), vite, next, nuxt (Node.js; needs nodeVersion and phpVersion \"none\" for a Node-only project) or django, flask, fastapi (Python; needs pythonVersion and phpVersion \"none\"). See list_runtimes for details. Cannot be combined with gitUrl."`
	PHPVersion         string            `json:"phpVersion,omitempty" jsonschema:"PHP version such as 8.4 (default: the catalogue default). Use \"none\" for a Python, Node-only or static project."`
	WebServer          string            `json:"webServer,omitempty" jsonschema:"Web server: caddy (default), apache (mod_rewrite + .htaccess, e.g. for WordPress) or nginx."`
	PHPExtensions      []string          `json:"phpExtensions,omitempty" jsonschema:"PHP extensions to enable (keys from list_runtimes). Default: bcmath, gd, intl, opcache, pdo_mysql, zip."`
	Database           string            `json:"database,omitempty" jsonschema:"Database engine: mariadb, mysql or postgresql. Omit for no database."`
	DBVersion          string            `json:"databaseVersion,omitempty" jsonschema:"Database version (default: catalogue default)."`
	Databases          []namedDatabaseIn `json:"additionalDatabases,omitempty" jsonschema:"Additional database servers next to the primary, each reached by its name (host analytics, variables ANALYTICS_DB_HOST, ANALYTICS_DATABASE_URL …)."`
	Redis              bool              `json:"redis,omitempty" jsonschema:"Add a Redis service."`
	Mailpit            bool              `json:"mailpit,omitempty" jsonschema:"Add Mailpit (SMTP catcher with web inbox)."`
	Memcached          bool              `json:"memcached,omitempty" jsonschema:"Add Memcached (in-memory cache, MEMCACHED_* injected)."`
	RabbitMQ           bool              `json:"rabbitmq,omitempty" jsonschema:"Add RabbitMQ (message broker with management UI, RABBITMQ_* injected)."`
	Meilisearch        bool              `json:"meilisearch,omitempty" jsonschema:"Add Meilisearch (search engine with web dashboard, MEILISEARCH_* injected)."`
	Typesense          bool              `json:"typesense,omitempty" jsonschema:"Add Typesense (search engine, TYPESENSE_* injected)."`
	OpenSearch         bool              `json:"opensearch,omitempty" jsonschema:"Add OpenSearch (Elasticsearch-compatible search engine without authentication, OPENSEARCH_* injected)."`
	OpenSearchDash     bool              `json:"opensearchDashboards,omitempty" jsonschema:"Add OpenSearch Dashboards (web UI with Dev Tools console; implies opensearch)."`
	Ollama             bool              `json:"ollama,omitempty" jsonschema:"Add Ollama (LLM server, OLLAMA_HOST/OLLAMA_BASE_URL/OLLAMA_URL injected). Models live in one store shared by all projects and are pulled from the project's Services tab."`
	OllamaGPU          bool              `json:"ollamaGpu,omitempty" jsonschema:"Hand the host's GPUs to Ollama (implies ollama; needs the NVIDIA Container Toolkit on the host)."`
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

type namedDatabaseIn struct {
	Name    string `json:"name" jsonschema:"Name of the database server: lowercase letters, digits and dashes (e.g. analytics)"`
	Type    string `json:"type" jsonschema:"mariadb, mysql, postgresql or mongodb"`
	Version string `json:"version,omitempty" jsonschema:"Version (default: catalogue default)"`
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
	for _, d := range in.Databases {
		req.Databases = append(req.Databases, project.NamedDatabaseRequest{Name: strings.TrimSpace(d.Name), DatabaseRequest: project.DatabaseRequest{Type: strings.ToLower(strings.TrimSpace(d.Type)), Version: strings.TrimSpace(d.Version)}})
	}
	if in.Redis {
		req.Redis = &project.ExtraRequest{}
	}
	if in.Mailpit {
		req.Mailpit = &project.ExtraRequest{}
	}
	if in.Memcached {
		req.Memcached = &project.ExtraRequest{}
	}
	if in.RabbitMQ {
		req.RabbitMQ = &project.ExtraRequest{}
	}
	if in.Meilisearch {
		req.Meilisearch = &project.ExtraRequest{}
	}
	if in.Typesense {
		req.Typesense = &project.ExtraRequest{}
	}
	if in.OpenSearch || in.OpenSearchDash {
		req.OpenSearch = &project.ExtraRequest{Dashboards: in.OpenSearchDash}
	}
	if in.Ollama || in.OllamaGPU {
		req.Ollama = &project.ExtraRequest{GPU: in.OllamaGPU}
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
	Workers  *bool  `json:"workers,omitempty" jsonschema:"Copy the worker and cron job definitions (default true)."`
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
	Service string `json:"service,omitempty" jsonschema:"Container: web, php, python, node, database, redis, memcached, mailpit, rabbitmq, meilisearch, typesense, opensearch, opensearch-dashboards, ollama or storage (default: the application container (php, else python, else node), else web)"`
	Tail    int    `json:"tail,omitempty" jsonschema:"Number of lines (default 200, max 2000)"`
	Since   string `json:"since,omitempty" jsonschema:"Only lines from this time on: RFC 3339 or a duration back from now such as 30m, 6h or 7d"`
	Until   string `json:"until,omitempty" jsonschema:"Only lines up to this time: RFC 3339 or a duration back from now"`
	Query   string `json:"query,omitempty" jsonschema:"Only lines containing this text (case-insensitive)"`
	Level   string `json:"level,omitempty" jsonschema:"Only warnings and errors (warn) or errors (error), as guessed from the text"`
}

type getLogsOut struct {
	Service string      `json:"service"`
	Lines   []logs.Line `json:"lines"`
	// Matched counts all matching lines in the range, of which the last are returned.
	Matched int `json:"matched"`
}

func (s *Server) getLogs(ctx context.Context, _ *mcp.CallToolRequest, in getLogsIn) (*mcp.CallToolResult, getLogsOut, error) {
	id, kind, q, err := s.logTarget(ctx, in)
	if err != nil {
		r, _ := toolErr(err)
		return r, getLogsOut{}, nil
	}
	n := in.Tail
	if n <= 0 {
		n = 200
	}
	if n > 2000 {
		n = 2000
	}
	page, err := s.d.Projects.QueryLogs(ctx, id, kind, q, n)
	if err != nil {
		r, _ := toolErr(err)
		return r, getLogsOut{}, nil
	}
	return nil, getLogsOut{Service: string(kind), Lines: page.Lines, Matched: page.Matched}, nil
}

// logTarget resolves the project, service and filter of a log tool call.
func (s *Server) logTarget(ctx context.Context, in getLogsIn) (string, store.ServiceKind, logs.Query, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		return "", "", logs.Query{}, err
	}
	kind := store.ServiceKind(strings.ToLower(strings.TrimSpace(in.Service)))
	if kind == "" {
		kind = store.ServiceWeb
		if app, ok := project.AppKind(v.Project); ok {
			kind = app
		}
	}
	switch kind {
	case store.ServiceWeb, store.ServicePHP, store.ServicePython, store.ServiceNode, store.ServiceDatabase, store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOpenSearchDashboards, store.ServiceOllama, store.ServiceStorage:
	default:
		if name := kind.DatabaseName(); name != "" && project.ValidateDatabaseServiceName(name) == nil {
			break
		}
		return "", "", logs.Query{}, fmt.Errorf("%w: unknown service %q", validate.ErrInvalid, in.Service)
	}
	now := time.Now()
	since, err := logs.ParseTime(in.Since, now)
	if err != nil {
		return "", "", logs.Query{}, fmt.Errorf("%w: since: %v", validate.ErrInvalid, err)
	}
	until, err := logs.ParseTime(in.Until, now)
	if err != nil {
		return "", "", logs.Query{}, fmt.Errorf("%w: until: %v", validate.ErrInvalid, err)
	}
	level, ok := logs.ParseLevel(in.Level)
	if !ok {
		return "", "", logs.Query{}, fmt.Errorf("%w: level must be warn or error", validate.ErrInvalid)
	}
	return v.Project.ID, kind, logs.Query{Since: since, Until: until, Text: in.Query, MinLevel: level}, nil
}

type getLogStatsIn struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
	Service string `json:"service,omitempty" jsonschema:"Container, as for get_logs (default: the application container)"`
	Since   string `json:"since,omitempty" jsonschema:"Start of the range: RFC 3339 or a duration back from now such as 6h or 7d (default: everything available)"`
	Until   string `json:"until,omitempty" jsonschema:"End of the range (default: now)"`
	Query   string `json:"query,omitempty" jsonschema:"Only count lines containing this text"`
}

func (s *Server) getLogStats(ctx context.Context, _ *mcp.CallToolRequest, in getLogStatsIn) (*mcp.CallToolResult, logs.Summary, error) {
	id, kind, q, err := s.logTarget(ctx, getLogsIn{Project: in.Project, Service: in.Service, Since: in.Since, Until: in.Until, Query: in.Query})
	if err != nil {
		r, _ := toolErr(err)
		return r, logs.Summary{}, nil
	}
	sum, err := s.d.Projects.LogStats(ctx, id, kind, q)
	if err != nil {
		r, _ := toolErr(err)
		return r, logs.Summary{}, nil
	}
	return nil, sum, nil
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
	// Servers are the project's database servers; DB names the one listed below.
	Servers   []databaseServerOut `json:"servers"`
	DB        string              `json:"db"`
	Engine    string              `json:"engine"`
	Version   string              `json:"version"`
	Databases []string            `json:"databases"`
}

type databaseServerOut struct {
	Name     string `json:"name" jsonschema:"\"\" for the primary, otherwise the additional database's name"`
	Engine   string `json:"engine"`
	Version  string `json:"version"`
	Host     string `json:"host"`
	Database string `json:"database"`
	State    string `json:"state"`
}

type databaseRef struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
	DB      string `json:"db,omitempty" jsonschema:"Database server: empty for the primary, or the name of an additional one"`
}

func (s *Server) listDatabases(ctx context.Context, _ *mcp.CallToolRequest, in databaseRef) (*mcp.CallToolResult, listDatabasesOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	db := strings.TrimSpace(in.DB)
	servers, err := s.d.Projects.Databases(ctx, v.Project.ID)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	out := listDatabasesOut{DB: db, Servers: []databaseServerOut{}, Databases: []string{}}
	for _, info := range servers {
		out.Servers = append(out.Servers, databaseServerOut{Name: info.Name, Engine: info.Type, Version: info.Version, Host: info.Host, Database: info.Database, State: info.State})
	}
	info, err := s.d.Projects.DatabaseInfo(ctx, v.Project.ID, db)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	out.Engine, out.Version = info.Type, info.Version
	names, err := s.d.Projects.ListDatabases(ctx, v.Project.ID, db)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	if names != nil {
		out.Databases = names
	}
	return nil, out, nil
}

type createDatabaseIn struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
	DB      string `json:"db,omitempty" jsonschema:"Database server: empty for the primary, or the name of an additional one"`
	Name    string `json:"name" jsonschema:"Database name: lower-case letters, digits, underscores"`
}

func (s *Server) createDatabase(ctx context.Context, req *mcp.CallToolRequest, in createDatabaseIn) (*mcp.CallToolResult, listDatabasesOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	if err := s.d.Projects.CreateDatabase(ctx, v.Project.ID, strings.TrimSpace(in.DB), strings.TrimSpace(in.Name)); err != nil {
		r, _ := toolErr(err)
		return r, listDatabasesOut{}, nil
	}
	return s.listDatabases(ctx, req, databaseRef{Project: v.Project.ID, DB: in.DB})
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

func (s *Server) listSnapshots(ctx context.Context, _ *mcp.CallToolRequest, in databaseRef) (*mcp.CallToolResult, listBackupsOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, listBackupsOut{}, nil
	}
	list, err := s.d.Projects.ListSnapshots(ctx, v.Project.ID, strings.TrimSpace(in.DB))
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

type createSnapshotIn struct {
	Project string `json:"project" jsonschema:"Project id, slug or name"`
	DB      string `json:"db,omitempty" jsonschema:"Database server: empty for the primary, or the name of an additional one"`
	Note    string `json:"note,omitempty" jsonschema:"Short note stored with the snapshot, e.g. what it was taken before"`
}

func (s *Server) createSnapshot(ctx context.Context, _ *mcp.CallToolRequest, in createSnapshotIn) (*mcp.CallToolResult, backupOut, error) {
	v, err := s.resolve(ctx, in.Project)
	if err != nil {
		r, _ := toolErr(err)
		return r, backupOut{}, nil
	}
	b, err := s.d.Projects.CreateSnapshot(ctx, v.Project.ID, strings.TrimSpace(in.DB), strings.TrimSpace(in.Note))
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
