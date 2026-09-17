package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/docker"
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

	env, err := buildEnv(req.Env)
	if err != nil {
		return store.Project{}, err
	}
	proj.Env = env
	sort.SliceStable(proj.Services, func(i, j int) bool { return proj.Services[i].Position < proj.Services[j].Position })
	return proj, nil
}

func buildEnv(in []EnvVarRequest) ([]store.EnvVar, error) {
	seen := map[string]bool{}
	var out []store.EnvVar
	for _, e := range in {
		key := strings.TrimSpace(e.Key)
		if err := validate.EnvKey(key); err != nil {
			return nil, err
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
func (m *Manager) allocatePort(ctx context.Context) (int, error) {
	used := map[int]bool{}
	dbPorts, err := m.store.Projects.UsedPorts(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range dbPorts {
		used[p] = true
	}
	containers, err := m.engine.ListContainers(ctx, false, "")
	if err != nil && !errors.Is(err, docker.ErrUnavailable) {
		return 0, err
	}
	for _, c := range containers {
		for _, p := range c.Ports {
			used[p.HostPort] = true
		}
	}
	for port := m.cfg.PortRangeStart; port <= m.cfg.PortRangeEnd; port++ {
		if !used[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("%w: no free port in range %d-%d", ErrConflict, m.cfg.PortRangeStart, m.cfg.PortRangeEnd)
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
		case store.ServiceWeb:
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
	views := make([]View, 0, len(projects))
	for _, p := range projects {
		st := deriveStatus(p, containers)
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
	st := deriveStatus(p, containers)
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
func (m *Manager) ensureProjectDir(planner *Planner, proj store.Project, starter bool) error {
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
		if err := os.MkdirAll(docroot, 0o755); err != nil {
			return fmt.Errorf("create document root: %w", err)
		}
	}
	chownTree(dir, planner.paths.PUID, planner.paths.PGID)
	if starter {
		entries, err := os.ReadDir(docroot)
		if err != nil {
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
