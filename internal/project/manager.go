package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/notify"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
)

// Config configures the manager.
type Config struct {
	PortRangeStart int
	PortRangeEnd   int
	// StopTimeout is the grace period for container stops.
	StopTimeout time.Duration
}

// PathsProvider returns the current planner paths (host paths may be detected lazily).
type PathsProvider func() (Paths, error)

// Manager owns the project lifecycle.
type Manager struct {
	store   *store.Store
	engine  docker.Engine
	catalog *runtime.Catalog
	paths   PathsProvider
	audit   *audit.Logger
	log     *slog.Logger
	cfg     Config

	locks    sync.Map // project id -> *sync.Mutex
	createMu sync.Mutex

	reportMu sync.RWMutex
	report   ReconcileReport

	notifier  notify.Sender
	unhealthy map[string]bool // project ids reported as unhealthy (for recovery events)
}

// SetNotifier installs the notification sink (nil = none).
func (m *Manager) SetNotifier(n notify.Sender) { m.notifier = n }

// notify delivers an event when a notifier is installed.
func (m *Manager) notify(ctx context.Context, e notify.Event) {
	if m.notifier != nil {
		m.notifier.Notify(ctx, e)
	}
}

// NewManager creates a manager.
func NewManager(st *store.Store, engine docker.Engine, catalog *runtime.Catalog, paths PathsProvider, auditLog *audit.Logger, cfg Config, log *slog.Logger) *Manager {
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = 10 * time.Second
	}
	return &Manager{store: st, engine: engine, catalog: catalog, paths: paths, audit: auditLog, log: log, cfg: cfg}
}

func (m *Manager) planner() (*Planner, error) {
	p, err := m.paths()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	p.BaseDomain = m.BaseDomain(context.Background())
	p.XdebugClientHost = m.XdebugClientHost(context.Background())
	return NewPlanner(p, m.catalog), nil
}

// lock acquires the per-project lock or returns ErrBusy.
func (m *Manager) lock(id string) (func(), error) {
	v, _ := m.locks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, ErrBusy
	}
	return mu.Unlock, nil
}

// ---------------------------------------------------------------------------
// Validation and planning
// ---------------------------------------------------------------------------

// buildProject validates a create request and produces the desired-state record.
func (m *Manager) buildProject(req CreateRequest) (store.Project, error) {
	if err := validate.ProjectName(req.Name); err != nil {
		return store.Project{}, err
	}
	slug := validate.Slugify(req.Name)
	if err := validate.Slug(slug); err != nil {
		return store.Project{}, fmt.Errorf("%w: project name %q does not yield a usable identifier", validate.ErrInvalid, req.Name)
	}
	relPath := req.Path
	if strings.TrimSpace(relPath) == "" {
		relPath = slug
	}
	relPath, err := validate.RelativePath(relPath, 3)
	if err != nil {
		return store.Project{}, err
	}
	// Templates bring their own document root and PHP extensions.
	if req.Template != "" {
		tpl, ok := TemplateByID(req.Template)
		if !ok {
			return store.Project{}, fmt.Errorf("%w: unknown template %q", validate.ErrInvalid, req.Template)
		}
		if req.PHP == nil {
			return store.Project{}, fmt.Errorf("%w: template %s needs PHP", validate.ErrInvalid, tpl.ID)
		}
		if tpl.RequiresDatabase && req.Database == nil {
			return store.Project{}, fmt.Errorf("%w: template %s needs a database", validate.ErrInvalid, tpl.ID)
		}
		if req.Git != nil && req.Git.URL != "" {
			return store.Project{}, fmt.Errorf("%w: choose either a template or a repository", validate.ErrInvalid)
		}
		if strings.TrimSpace(req.Docroot) == "" {
			req.Docroot = tpl.Docroot
		}
		if req.PHP.Config.Extensions == nil {
			req.PHP.Config.Extensions = runtime.DefaultPHPConfig().Extensions
		}
		for _, ext := range tpl.PHPExtensions {
			if !slices.Contains(req.PHP.Config.Extensions, ext) {
				req.PHP.Config.Extensions = append(req.PHP.Config.Extensions, ext)
			}
		}
	}
	docroot, err := validate.OptionalRelativePath(req.Docroot, 4)
	if err != nil {
		return store.Project{}, fmt.Errorf("document root: %w", err)
	}

	proj := store.Project{
		ID:           store.NewID(),
		Name:         req.Name,
		Slug:         slug,
		Path:         relPath,
		Docroot:      docroot,
		DesiredState: store.DesiredStopped,
		Lifecycle:    store.LifecycleCreating,
	}
	if req.Start {
		proj.DesiredState = store.DesiredRunning
	}

	webType := req.Web.Type
	if webType == "" {
		webType = "caddy"
	}
	if webType != "caddy" {
		return store.Project{}, fmt.Errorf("%w: unsupported web server %q", validate.ErrInvalid, webType)
	}
	webVersion, err := m.catalog.Resolve("caddy", req.Web.Version)
	if err != nil {
		return store.Project{}, err
	}
	proj.Services = append(proj.Services, store.ProjectService{
		Kind: store.ServiceWeb, Variant: "caddy", Version: webVersion.Version, Image: webVersion.Image, Enabled: true, Position: 20,
	})

	if req.PHP != nil {
		v, err := m.catalog.Resolve("php", req.PHP.Version)
		if err != nil {
			return store.Project{}, err
		}
		cfg := req.PHP.Config
		if err := cfg.Normalize(); err != nil {
			return store.Project{}, err
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, store.ProjectService{
			Kind: store.ServicePHP, Variant: "php", Version: v.Version, Image: v.Image, Enabled: true, Config: raw, Position: 10,
		})
	}

	if req.Redis != nil {
		svc, err := m.buildExtraService(store.ServiceRedis, req.Redis.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}
	if req.Mailpit != nil {
		svc, err := m.buildExtraService(store.ServiceMailpit, req.Mailpit.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}
	if req.Node != nil {
		v, err := m.catalog.Resolve("node", req.Node.Version)
		if err != nil {
			return store.Project{}, err
		}
		cfg := req.Node.Config
		if err := cfg.Normalize(); err != nil {
			return store.Project{}, err
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, store.ProjectService{
			Kind: store.ServiceNode, Variant: "node", Version: v.Version, Image: v.Image, Enabled: true, Position: 15, Config: raw,
		})
	}
	if req.Git != nil {
		g, err := buildGitConfig(*req.Git, store.GitConfig{})
		if err != nil {
			return store.Project{}, err
		}
		proj.Git = g
	}
	if req.Database != nil {
		svc, err := m.buildDatabaseService(slug, req.Database.Type, req.Database.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}

	env, err := buildEnv(req.Env)
	if err != nil {
		return store.Project{}, err
	}
	proj.Env = env
	sort.SliceStable(proj.Services, func(i, j int) bool { return proj.Services[i].Position < proj.Services[j].Position })
	return proj, nil
}

// buildDatabaseService validates the database selection and generates credentials.
func (m *Manager) buildDatabaseService(slug, dbType, version string) (store.ProjectService, error) {
	if dbType == "" {
		dbType = "mariadb"
	}
	if _, ok := runtime.DialectFor(dbType); !ok {
		return store.ProjectService{}, fmt.Errorf("%w: database type %q is not supported", validate.ErrInvalid, dbType)
	}
	v, err := m.catalog.Resolve(dbType, version)
	if err != nil {
		return store.ProjectService{}, err
	}
	cfg, err := runtime.NewDatabaseConfig(slug)
	if err != nil {
		return store.ProjectService{}, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return store.ProjectService{}, err
	}
	return store.ProjectService{Kind: store.ServiceDatabase, Variant: dbType, Version: v.Version, Image: v.Image, Enabled: true, Config: raw, Position: 5}, nil
}

// buildExtraService validates an auxiliary service selection.
func (m *Manager) buildExtraService(kind store.ServiceKind, version string) (store.ProjectService, error) {
	v, err := m.catalog.Resolve(string(kind), version)
	if err != nil {
		return store.ProjectService{}, err
	}
	position := 6
	if kind == store.ServiceMailpit {
		position = 7
	}
	return store.ProjectService{Kind: kind, Variant: string(kind), Version: v.Version, Image: v.Image, Enabled: true, Config: json.RawMessage(`{"hostPort":0}`), Position: position}, nil
}

func buildEnv(in []EnvVarRequest) ([]store.EnvVar, error) {
	seen := map[string]bool{}
	var out []store.EnvVar
	for _, e := range in {
		key := strings.TrimSpace(e.Key)
		if err := validate.EnvKey(key); err != nil {
			return nil, err
		}
		if strings.HasPrefix(key, "MARIADB_") || strings.HasPrefix(key, "MYSQL_") || strings.HasPrefix(key, "POSTGRES_") {
			return nil, fmt.Errorf("%w: %s is reserved for the database container", validate.ErrInvalid, key)
		}
		if err := validate.EnvValue(e.Value); err != nil {
			return nil, err
		}
		if strings.HasPrefix(key, "STAQIO_") {
			return nil, fmt.Errorf("%w: environment variables starting with STAQIO_ are reserved", validate.ErrInvalid)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate environment variable %s", validate.ErrInvalid, key)
		}
		seen[key] = true
		out = append(out, store.EnvVar{Key: key, Value: e.Value, IsSecret: e.IsSecret})
	}
	return out, nil
}

// Preview validates a request and shows what would be created, without side effects.
func (m *Manager) Preview(ctx context.Context, req CreateRequest) (Preview, error) {
	proj, err := m.buildProject(req)
	if err != nil {
		return Preview{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return Preview{}, err
	}
	port, err := m.allocatePort(ctx)
	if err != nil {
		return Preview{}, err
	}
	proj.HTTPPort = port
	if err := m.assignServicePorts(ctx, &proj, req); err != nil {
		return Preview{}, err
	}
	plan, err := planner.Plan(proj)
	if err != nil {
		return Preview{}, err
	}
	pv := planner.Preview(proj, plan)
	// Surface name/path conflicts early so the wizard can react before submitting.
	if projects, err := m.store.Projects.List(ctx); err == nil {
		for _, p := range projects {
			if strings.EqualFold(p.Name, proj.Name) || p.Slug == proj.Slug {
				pv.Warnings = append(pv.Warnings, fmt.Sprintf("a project named %q already exists", p.Name))
			}
			if p.Path == proj.Path {
				pv.Warnings = append(pv.Warnings, fmt.Sprintf("the directory %q is already used by project %q", p.Path, p.Name))
			}
		}
	}
	for _, img := range plan.Images {
		exists, err := m.engine.ImageExists(ctx, img)
		if err == nil && !exists {
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("image %s will be pulled on first start", img))
		}
	}
	return pv, nil
}

// allocatePort finds a free host port in the configured range. Ports already recorded in
// the database or published by any container on the host are skipped.
func (m *Manager) allocatePort(ctx context.Context, exclude ...int) (int, error) {
	used := map[int]bool{}
	for _, p := range exclude {
		used[p] = true
	}
	if err := m.collectUsedPorts(ctx, used); err != nil {
		return 0, err
	}
	for port := m.cfg.PortRangeStart; port <= m.cfg.PortRangeEnd; port++ {
		if !used[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("%w: no free port in range %d-%d", ErrConflict, m.cfg.PortRangeStart, m.cfg.PortRangeEnd)
}

// collectUsedPorts records ports allocated in the database (web + database services) and
// ports published by any container on the host.
func (m *Manager) collectUsedPorts(ctx context.Context, used map[int]bool) error {
	dbPorts, err := m.store.Projects.UsedPorts(ctx)
	if err != nil {
		return err
	}
	for _, p := range dbPorts {
		used[p] = true
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if db := p.Service(store.ServiceDatabase); db != nil {
			var cfg runtime.DatabaseConfig
			if json.Unmarshal(db.Config, &cfg) == nil && cfg.HostPort > 0 {
				used[cfg.HostPort] = true
			}
		}
		for _, kind := range []store.ServiceKind{store.ServiceRedis, store.ServiceMailpit, store.ServiceNode} {
			if svc := p.Service(kind); svc != nil {
				var cfg runtime.ServiceConfig // NodeConfig shares the hostPort field name
				if json.Unmarshal(svc.Config, &cfg) == nil && cfg.HostPort > 0 {
					used[cfg.HostPort] = true
				}
			}
		}
	}
	containers, err := m.engine.ListContainers(ctx, false, "")
	if err != nil && !errors.Is(err, docker.ErrUnavailable) {
		return err
	}
	for _, c := range containers {
		for _, p := range c.Ports {
			used[p.HostPort] = true
		}
	}
	return nil
}

// assignServicePorts allocates host ports for services the request wants published
// (database, Redis) and always for Mailpit's web inbox. Ports already chosen for this
// project are excluded so the allocations do not collide with each other.
func (m *Manager) assignServicePorts(ctx context.Context, proj *store.Project, req CreateRequest) error {
	taken := []int{proj.HTTPPort}
	assign := func(kind store.ServiceKind) error {
		svc := proj.Service(kind)
		if svc == nil {
			return nil
		}
		port, err := m.allocatePort(ctx, taken...)
		if err != nil {
			return err
		}
		taken = append(taken, port)
		return setHostPort(svc, port)
	}
	if req.Database != nil && req.Database.ExposePort {
		if err := assign(store.ServiceDatabase); err != nil {
			return err
		}
	}
	if req.Redis != nil && req.Redis.ExposePort {
		if err := assign(store.ServiceRedis); err != nil {
			return err
		}
	}
	if req.Mailpit != nil {
		if err := assign(store.ServiceMailpit); err != nil {
			return err
		}
	}
	if req.Node != nil && req.Node.Config.DevServer {
		if err := assign(store.ServiceNode); err != nil {
			return err
		}
	}
	return nil
}

// setHostPort stores a host port in a service config (database or auxiliary service).
func setHostPort(svc *store.ProjectService, port int) error {
	if svc.Kind == store.ServiceNode {
		var cfg runtime.NodeConfig
		if len(svc.Config) > 0 {
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return err
			}
		}
		cfg.HostPort = port
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		svc.Config = raw
		return nil
	}
	if svc.Kind == store.ServiceDatabase {
		var cfg runtime.DatabaseConfig
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return err
		}
		cfg.HostPort = port
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		svc.Config = raw
		return nil
	}
	var cfg runtime.ServiceConfig
	if len(svc.Config) > 0 {
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return err
		}
	}
	cfg.HostPort = port
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	svc.Config = raw
	return nil
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

// resolveImages refreshes the image reference of every service from the catalogue. The
// catalogue owns the version→image mapping, so a Staqio update that ships a new runtime
// image propagates to existing projects on their next start/restart. Unknown versions keep
// the stored image.
func (m *Manager) resolveImages(p *store.Project) {
	for i := range p.Services {
		svc := &p.Services[i]
		key := ""
		switch svc.Kind {
		case store.ServicePHP:
			key = "php"
		case store.ServiceNode:
			key = "node"
		case store.ServiceRedis, store.ServiceMailpit:
			key = string(svc.Kind)
		case store.ServiceWeb, store.ServiceDatabase:
			key = svc.Variant
		}
		if key == "" {
			continue
		}
		if v, err := m.catalog.Resolve(key, svc.Version); err == nil && v.Image != "" {
			svc.Image = v.Image
		}
	}
}

func (m *Manager) loadProject(ctx context.Context, id string) (store.Project, error) {
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return store.Project{}, err
	}
	m.resolveImages(&p)
	return p, nil
}

func (m *Manager) loadProjects(ctx context.Context) ([]store.Project, error) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		m.resolveImages(&projects[i])
	}
	return projects, nil
}

// List returns all projects with derived status.
func (m *Manager) List(ctx context.Context) ([]View, error) {
	projects, err := m.loadProjects(ctx)
	if err != nil {
		return nil, err
	}
	containers, dockerErr := m.engine.ListContainers(ctx, true, "")
	var imageIDs map[string]string
	if dockerErr == nil {
		imageIDs = m.localImageIDs(ctx, projects)
	}
	views := make([]View, 0, len(projects))
	for _, p := range projects {
		st := deriveStatus(p, containers, imageIDs)
		if dockerErr != nil {
			st.Warnings = append(st.Warnings, "Docker engine unavailable: "+dockerErr.Error())
		}
		views = append(views, View{Project: p, Status: st, HTTPPort: p.HTTPPort})
	}
	return views, nil
}

// Get returns one project with derived status.
func (m *Manager) Get(ctx context.Context, id string) (View, error) {
	if err := validate.UUID(id); err != nil {
		return View{}, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return View{}, err
	}
	containers, dockerErr := m.engine.ListContainers(ctx, true, id)
	var imageIDs map[string]string
	if dockerErr == nil {
		imageIDs = m.localImageIDs(ctx, []store.Project{p})
	}
	st := deriveStatus(p, containers, imageIDs)
	if dockerErr != nil {
		st.Warnings = append(st.Warnings, "Docker engine unavailable: "+dockerErr.Error())
	}
	return View{Project: p, Status: st, HTTPPort: p.HTTPPort}, nil
}

// PlanFor returns the current plan of a project (for the "advanced" detail view).
func (m *Manager) PlanFor(ctx context.Context, id string) (Preview, error) {
	if err := validate.UUID(id); err != nil {
		return Preview{}, ErrNotFound
	}
	p, err := m.loadProject(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	planner, err := m.planner()
	if err != nil {
		return Preview{}, err
	}
	plan, err := planner.Plan(p)
	if err != nil {
		return Preview{}, err
	}
	return planner.Preview(p, plan), nil
}

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

func writePlanFiles(plan Plan) error {
	for _, f := range plan.Files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(f.Path, []byte(f.Content), mode); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
		// WriteFile honours umask; enforce the mode so read-only mounts work for any uid.
		if err := os.Chmod(f.Path, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", f.Path, err)
		}
	}
	return nil
}

const starterIndexPHP = `<?php
/**
 * Staqio starter page. Replace this file with your application.
 */
$project = getenv('STAQIO_PROJECT') ?: 'project';
?>
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title><?= htmlspecialchars($project) ?> · Staqio</title>
<style>
  body{font-family:system-ui,sans-serif;background:#0f1115;color:#e6e8ee;margin:0;display:grid;place-items:center;min-height:100vh}
  main{max-width:36rem;padding:2rem}
  h1{font-weight:600;margin:0 0 .5rem}
  code{background:#1b1f27;padding:.15rem .4rem;border-radius:.3rem}
  .ok{color:#4ade80}
</style>
</head>
<body>
<main>
  <p class="ok">● Running</p>
  <h1><?= htmlspecialchars($project) ?></h1>
  <p>Your Staqio project is served by PHP <?= PHP_VERSION ?> (<?= php_sapi_name() ?>).</p>
  <p>Document root: <code><?= htmlspecialchars($_SERVER['DOCUMENT_ROOT'] ?? '') ?></code></p>
  <p>Replace <code>index.php</code> to get started.</p>
</main>
</body>
</html>
`

// ensureProjectDir creates the project directory (and document root) if missing and
// optionally writes a starter page when the document root is empty.
//
// scaffold is true when a repository clone or template fills the directory afterwards:
// the document root is then not pre-created (it must stay empty) and no starter page is
// written.
func (m *Manager) ensureProjectDir(planner *Planner, proj store.Project, starter, scaffold bool) error {
	root := planner.paths.ProjectsDir
	dir, err := validate.ResolveUnder(root, proj.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create project directory: %w", err)
	}
	// Symlink escape check after creation.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve projects root: %w", err)
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("%w: project directory resolves outside the projects root", validate.ErrInvalid)
	}
	docroot := dir
	if proj.Docroot != "" {
		docroot, err = validate.ResolveUnder(dir, proj.Docroot)
		if err != nil {
			return err
		}
		// A repository/template brings its own document root; creating it would make the
		// directory non-empty and block the clone.
		if !scaffold {
			if err := os.MkdirAll(docroot, 0o755); err != nil {
				return fmt.Errorf("create document root: %w", err)
			}
		}
	}
	chownTree(dir, planner.paths.PUID, planner.paths.PGID)
	if starter {
		entries, err := os.ReadDir(docroot)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if len(entries) == 0 {
			target := filepath.Join(docroot, "index.php")
			if err := os.WriteFile(target, []byte(starterIndexPHP), 0o644); err != nil {
				return fmt.Errorf("write starter page: %w", err)
			}
			_ = os.Chown(target, planner.paths.PUID, planner.paths.PGID)
		}
	}
	return nil
}

// chownTree changes ownership of dir and its direct children created by Staqio. Errors are
// ignored: on hosts where Staqio does not run as root the files already belong to the user.
func chownTree(dir string, uid, gid int) {
	if os.Geteuid() != 0 {
		return
	}
	_ = os.Chown(dir, uid, gid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Lchown(filepath.Join(dir, e.Name()), uid, gid)
	}
}
