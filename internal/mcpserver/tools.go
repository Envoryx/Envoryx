package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/project"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
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
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Slug      string       `json:"slug"`
	State     string       `json:"state"`
	URL       string       `json:"url,omitempty"`
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
	out := projectOut{ID: p.ID, Name: p.Name, Slug: p.Slug, State: string(v.Status.State), Path: p.Path, Docroot: p.Docroot,
		Warnings: v.Status.Warnings, GitURL: p.Git.URL, GitBranch: p.Git.Branch, Hostnames: []string{}, Services: []serviceOut{}}
	hosts, err := s.d.Projects.ProjectHostnames(ctx, p)
	if err == nil {
		out.Hostnames = hosts
	}
	if v.HTTPPort > 0 {
		host := ""
		if s.d.Links.PublicHost != nil {
			host = s.d.Links.PublicHost(ctx)
		}
		if host == "" {
			host = "<staqio-host>"
		}
		out.DirectURL = fmt.Sprintf("http://%s:%d", host, v.HTTPPort)
	}
	if len(out.Hostnames) > 0 {
		switch {
		case s.d.Links.HTTPSPort == 443:
			out.URL = "https://" + out.Hostnames[0]
		case s.d.Links.HTTPSPort > 0:
			out.URL = fmt.Sprintf("https://%s:%d", out.Hostnames[0], s.d.Links.HTTPSPort)
		case s.d.Links.HTTPPort == 80:
			out.URL = "http://" + out.Hostnames[0]
		case s.d.Links.HTTPPort > 0:
			out.URL = fmt.Sprintf("http://%s:%d", out.Hostnames[0], s.d.Links.HTTPPort)
		}
	}
	if out.URL == "" {
		out.URL = out.DirectURL
	}
	for _, st := range v.Status.Services {
		out.Services = append(out.Services, serviceOut{Kind: string(st.Kind), Variant: st.Variant, Version: st.Version, State: st.State, Health: st.Health})
	}
	return out
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
	mcp.AddTool(s.mcp, readOnly("list_projects", "List projects", "List all Staqio projects with state, URLs and services."), s.listProjects)
	mcp.AddTool(s.mcp, readOnly("get_project", "Get project", "Details and live status of one project."), s.getProject)
	mcp.AddTool(s.mcp, readOnly("list_runtimes", "List runtimes", "Available PHP/Node versions, database engines, services and PHP extension keys for create_project."), s.listRuntimes)
	mcp.AddTool(s.mcp, mutating("create_project", "Create project", "Create a new development environment (PHP + Caddy, optional database, Redis, Mailpit, Node, git clone). Returns the project including its URL.", false), s.createProject)
	mcp.AddTool(s.mcp, mutating("start_project", "Start project", "Start all containers of a project.", true), s.startProject)
	mcp.AddTool(s.mcp, mutating("stop_project", "Stop project", "Stop all containers of a project (data is kept).", true), s.stopProject)
	mcp.AddTool(s.mcp, mutating("restart_project", "Restart project", "Restart a project; also pulls updated runtime images.", true), s.restartProject)
	mcp.AddTool(s.mcp, readOnly("get_logs", "Get logs", "Recent log lines of one project container (web, php, node, database, redis, mailpit)."), s.getLogs)
	mcp.AddTool(s.mcp, readOnly("list_actions", "List actions", "Runnable project actions (composer, artisan, npm …) and whether they are currently available."), s.listActions)
	mcp.AddTool(s.mcp, mutating("run_action", "Run action", "Run one action from list_actions inside the project (e.g. composer:install) and return its output. Waits for completion (up to 20 minutes).", false), s.runAction)
	mcp.AddTool(s.mcp, readOnly("list_databases", "List databases", "Databases on the project's database server."), s.listDatabases)
	mcp.AddTool(s.mcp, mutating("create_database", "Create database", "Create an additional database on the project's database server (same credentials).", true), s.createDatabase)
	mcp.AddTool(s.mcp, readOnly("list_backups", "List backups", "Backups of a project."), s.listBackups)
	mcp.AddTool(s.mcp, mutating("create_backup", "Create backup", "Create a backup (database dump + files + configuration) of a project.", false), s.createBackup)
	mcp.AddTool(s.mcp, mutating("add_domain", "Add domain", "Add an extra host name routed to the project by the embedded proxy.", true), s.addDomain)
}

// ---- Projects ---------------------------------------------------------------------

type listProjectsIn struct{}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, _ listProjectsIn) (*mcp.CallToolResult, listProjectsOut, error) {
	views, err := s.d.Projects.List(ctx)
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

type listRuntimesOut struct {
	Runtimes      []runtimeOut `json:"runtimes"`
	PHPExtensions []string     `json:"phpExtensions"`
}

func (s *Server) listRuntimes(_ context.Context, _ *mcp.CallToolRequest, _ listProjectsIn) (*mcp.CallToolResult, listRuntimesOut, error) {
	out := listRuntimesOut{Runtimes: []runtimeOut{}, PHPExtensions: []string{}}
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
	Name          string            `json:"name" jsonschema:"Display name, e.g. \"Shop API\". The slug and directory are derived from it."`
	PHPVersion    string            `json:"phpVersion,omitempty" jsonschema:"PHP version such as 8.4 (default: the catalogue default). Use \"none\" for a project without PHP."`
	PHPExtensions []string          `json:"phpExtensions,omitempty" jsonschema:"PHP extensions to enable (keys from list_runtimes). Default: bcmath, gd, intl, opcache, pdo_mysql, zip."`
	Database      string            `json:"database,omitempty" jsonschema:"Database engine: mariadb, mysql or postgresql. Omit for no database."`
	DBVersion     string            `json:"databaseVersion,omitempty" jsonschema:"Database version (default: catalogue default)."`
	Redis         bool              `json:"redis,omitempty" jsonschema:"Add a Redis service."`
	Mailpit       bool              `json:"mailpit,omitempty" jsonschema:"Add Mailpit (SMTP catcher with web inbox)."`
	NodeVersion   string            `json:"nodeVersion,omitempty" jsonschema:"Add a Node.js toolchain container with this major version (e.g. 24)."`
	NodeDevServer bool              `json:"nodeDevServer,omitempty" jsonschema:"Run the package.json dev script as a dev server (Vite preset; reachable at <slug>-dev.<base domain>). Requires nodeVersion."`
	Docroot       string            `json:"docroot,omitempty" jsonschema:"Document root relative to the project directory, e.g. public. Default: project root (public/ for Laravel/Symfony)."`
	GitURL        string            `json:"gitUrl,omitempty" jsonschema:"Repository to clone into the new project (https://… or git@…)."`
	GitBranch     string            `json:"gitBranch,omitempty" jsonschema:"Branch to check out."`
	Env           map[string]string `json:"env,omitempty" jsonschema:"Environment variables for the application containers."`
	Start         *bool             `json:"start,omitempty" jsonschema:"Start the project after creation (default true)."`
}

func (s *Server) createProject(ctx context.Context, _ *mcp.CallToolRequest, in createProjectIn) (*mcp.CallToolResult, projectOut, error) {
	req := project.CreateRequest{Name: strings.TrimSpace(in.Name), Docroot: strings.TrimSpace(in.Docroot), CreateStarter: true, Start: true}
	if in.Start != nil {
		req.Start = *in.Start
	}
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
	if v := strings.TrimSpace(in.NodeVersion); v != "" {
		req.Node = &project.NodeRequest{Version: v, Config: runtime.NodeConfig{DevServer: in.NodeDevServer}}
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
	v, err := s.d.Projects.Create(ctx, req)
	if err != nil {
		r, _ := toolErr(err)
		return r, projectOut{}, nil
	}
	return nil, s.projectOut(ctx, v), nil
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
	Service string `json:"service,omitempty" jsonschema:"Container: web, php, node, database, redis or mailpit (default php)"`
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
		kind = store.ServicePHP
	}
	switch kind {
	case store.ServiceWeb, store.ServicePHP, store.ServiceNode, store.ServiceDatabase, store.ServiceRedis, store.ServiceMailpit:
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
	b, err := s.d.Projects.CreateBackup(ctx, v.Project.ID, project.BackupOptions{Database: true, Files: true, IncludeDependencies: in.IncludeDependencies, Note: strings.TrimSpace(in.Note)})
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
