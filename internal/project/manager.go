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

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/logs"
	"github.com/envoryx/envoryx/internal/notify"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/s3"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
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
	ops      *ops      // running lifecycle operations, drained at Shutdown
	progress *progress // what those operations are doing, for the UI

	reportMu sync.RWMutex
	report   ReconcileReport
	activity []Activity // autonomous actions since start, newest first (see activity.go)

	notifier notify.Sender
	// metrics holds the resource history's sampling state (see metrics.go).
	metrics     *metricsState
	metricsOnce sync.Once
	// oom remembers recent OOM kills for the project warnings (see oom.go).
	oom     *oomLog
	oomOnce sync.Once
	// healthSt holds the application health checks (see health.go).
	healthSt   *healthState
	healthOnce sync.Once
	// backupHook is told about every backup created (offsite uploads).
	backupHook func(projectID string, b BackupInfo)
	unhealthy  map[string]bool // project ids reported as unhealthy (for recovery events)

	cronOnce sync.Once
	cronRuns *cronState // cron runs in progress (see cronjobs.go)

	ollamaOnce sync.Once
	ollama     *ollamaPulls // model downloads in progress (see ollama.go)

	// logStore keeps container output beyond the containers; nil = queries read Docker
	// only (see loghistory.go).
	logStore     *logs.Store
	logCollector *logs.Collector

	// provisioner creates project buckets; nil = the real S3 client. Tests inject a fake.
	provisioner s3.Provisioner
	// objectStore builds the client backups use; nil = the real S3 client.
	objectStore func(endpoint, accessKey, secretKey string) s3.ObjectStore
	// links tells the planner how the LAN reaches the proxy (public host, ports).
	links func(ctx context.Context) (publicHost string, httpPort, httpsPort int)
	// shareVia is the proxy's plain HTTP listener, which share tunnels send their requests
	// to so the project's proxy rules apply ("" = straight to the application).
	shareVia string
	// shareHosts maps project ids to the host name of their share address.
	shareHosts sync.Map
}

// SetShareProxy tells the manager where the proxy's plain HTTP listener is (":80",
// "127.0.0.1:8080"); share tunnels then go through the proxy.
func (m *Manager) SetShareProxy(addr string) { m.shareVia = addr }

// SetLinks installs the function that reports the public host and the proxy's host-side
// ports, which become part of the URLs injected into projects.
func (m *Manager) SetLinks(f func(ctx context.Context) (publicHost string, httpPort, httpsPort int)) {
	m.links = f
}

// SetProvisioner replaces the object-storage provisioner (tests).
func (m *Manager) SetProvisioner(p s3.Provisioner) { m.provisioner = p }

// SetObjectStoreFactory replaces the object store backups talk to (tests).
func (m *Manager) SetObjectStoreFactory(f func(endpoint, accessKey, secretKey string) s3.ObjectStore) {
	m.objectStore = f
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
	return &Manager{store: st, engine: engine, catalog: catalog, paths: paths, audit: auditLog, log: log, cfg: cfg, ops: newOps(), progress: newProgress()}
}

func (m *Manager) planner() (*Planner, error) {
	p, err := m.paths()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	p.BaseDomain = m.BaseDomain(context.Background())
	p.XdebugClientHost = m.XdebugClientHost(context.Background())
	p.FolderViewFolder = m.FolderViewFolder(context.Background())
	if m.links != nil {
		p.PublicHost, p.ProxyHTTPPort, p.ProxyHTTPSPort = m.links(context.Background())
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
	// Templates bring their own document root and runtime defaults (PHP extensions or
	// Node dev-server settings). This is the single place that checks a template against
	// the requested runtimes; MCP and REST only map the error.
	if req.Template != "" {
		tpl, ok := TemplateByID(req.Template)
		if !ok {
			return store.Project{}, fmt.Errorf("%w: unknown template %q", validate.ErrInvalid, req.Template)
		}
		switch tpl.Runtime {
		case "node":
			if req.Node == nil {
				return store.Project{}, fmt.Errorf("%w: template %s needs Node.js", validate.ErrInvalid, tpl.ID)
			}
			if tpl.Node != nil {
				// Merge per field: a request that only sets e.g. devServer or the package
				// manager must still get the template's preset, port and script (a Next
				// scaffold on the Vite preset crash-loops on --strictPort and listens on
				// the wrong port). When the request carries no dev-server settings at
				// all, the template also decides whether the dev server runs; a request
				// that configured them (preset/port/script) keeps its own devServer flag.
				c := &req.Node.Config
				if c.Preset == "" && c.Port == 0 && c.Script == "" {
					c.DevServer = tpl.Node.DevServer
				}
				if c.Preset == "" {
					c.Preset = tpl.Node.Preset
					if c.Port == 0 {
						c.Port = tpl.Node.Port
					}
				}
				// The template's script is its dev script; in production mode the serve
				// script (start/preview) comes from the preset defaults instead.
				if c.Script == "" && c.Mode != runtime.NodeModeProduction {
					c.Script = tpl.Node.Script
				}
			}
		case "python":
			if req.Python == nil {
				return store.Project{}, fmt.Errorf("%w: template %s needs Python", validate.ErrInvalid, tpl.ID)
			}
			if tpl.Python != nil {
				// Same merge as for Node: the template's preset, port and app win where the
				// request left them empty; a request without server settings takes the
				// template's Server flag.
				c := &req.Python.Config
				if c.Preset == "" && c.Port == 0 && c.App == "" {
					c.Server = tpl.Python.Server
				}
				if c.Preset == "" {
					c.Preset = tpl.Python.Preset
					if c.Port == 0 {
						c.Port = tpl.Python.Port
					}
					if c.App == "" {
						c.App = tpl.Python.App
					}
				}
			}
		default: // "php"
			if req.PHP == nil {
				return store.Project{}, fmt.Errorf("%w: template %s needs PHP", validate.ErrInvalid, tpl.ID)
			}
			if req.PHP.Config.Extensions == nil {
				req.PHP.Config.Extensions = runtime.DefaultPHPConfig().Extensions
			}
			for _, ext := range tpl.PHPExtensions {
				if !slices.Contains(req.PHP.Config.Extensions, ext) {
					req.PHP.Config.Extensions = append(req.PHP.Config.Extensions, ext)
				}
			}
			if tpl.PHPMemoryLimit != "" && (req.PHP.Config.MemoryLimit == "" || req.PHP.Config.MemoryLimit == runtime.DefaultPHPConfig().MemoryLimit) {
				req.PHP.Config.MemoryLimit = tpl.PHPMemoryLimit
			}
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
	}
	docroot, err := validate.OptionalRelativePath(req.Docroot, 4)
	if err != nil {
		return store.Project{}, fmt.Errorf("document root: %w", err)
	}
	if err := validateLimits(req.Limits, 0, 0); err != nil {
		return store.Project{}, err
	}
	health, err := normalizeHealthCheck(req.HealthCheck)
	if err != nil {
		return store.Project{}, err
	}

	proj := store.Project{
		ID:           store.NewID(),
		Name:         req.Name,
		Slug:         slug,
		Path:         relPath,
		Docroot:      docroot,
		DesiredState: store.DesiredStopped,
		Lifecycle:    store.LifecycleCreating,
		Limits:       req.Limits,
		HealthCheck:  health,
	}
	if req.Start {
		proj.DesiredState = store.DesiredRunning
	}

	spa := req.Web.SPAFallback != nil && *req.Web.SPAFallback
	if spa && req.PHP != nil {
		return store.Project{}, fmt.Errorf("%w: SPA fallback needs a project without PHP; the front controller handles unknown paths", validate.ErrInvalid)
	}
	web, err := m.buildWebService(req.Web.Type, req.Web.Version, spa)
	if err != nil {
		return store.Project{}, err
	}
	proj.Services = append(proj.Services, web)

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
		if e := req.Redis.External; e != nil {
			if req.Redis.ExposePort {
				return store.Project{}, errExternalRedisPort
			}
			if err := setExternalRedis(&svc, *e, ""); err != nil {
				return store.Project{}, err
			}
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
	if req.Memcached != nil {
		svc, err := m.buildExtraService(store.ServiceMemcached, req.Memcached.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}
	if req.RabbitMQ != nil {
		svc, err := m.buildExtraService(store.ServiceRabbitMQ, req.RabbitMQ.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}
	if req.Meilisearch != nil {
		svc, err := m.buildExtraService(store.ServiceMeilisearch, req.Meilisearch.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}
	if req.Typesense != nil {
		svc, err := m.buildExtraService(store.ServiceTypesense, req.Typesense.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}
	if req.Ollama != nil {
		svc, err := m.buildExtraService(store.ServiceOllama, req.Ollama.Version)
		if err != nil {
			return store.Project{}, err
		}
		if req.Ollama.GPU {
			if err := editConfig(&svc, func(c *runtime.ServiceConfig) error { c.GPU = true; return nil }); err != nil {
				return store.Project{}, err
			}
		}
		proj.Services = append(proj.Services, svc)
	}
	if req.OpenSearch != nil {
		svc, err := m.buildExtraService(store.ServiceOpenSearch, req.OpenSearch.Version)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
		if req.OpenSearch.Dashboards {
			// On OpenSearch's version: Dashboards refuses to talk to another one.
			dash, err := m.buildExtraService(store.ServiceOpenSearchDashboards, svc.Version)
			if err != nil {
				return store.Project{}, err
			}
			proj.Services = append(proj.Services, dash)
		}
	}
	if req.Storage != nil {
		svc, err := m.buildStorageService(proj.Slug, req.Storage.Version, req.Storage.PublicRead)
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
	if req.Python != nil {
		v, err := m.catalog.Resolve("python", req.Python.Version)
		if err != nil {
			return store.Project{}, err
		}
		cfg := req.Python.Config
		if err := cfg.Normalize(); err != nil {
			return store.Project{}, err
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, store.ProjectService{
			Kind: store.ServicePython, Variant: "python", Version: v.Version, Image: v.Image, Enabled: true, Position: 12, Config: raw,
		})
	}
	if req.Git != nil {
		g, err := buildGitConfig(*req.Git, store.GitConfig{})
		if err != nil {
			return store.Project{}, err
		}
		proj.Git = g
	}
	for _, d := range append([]DatabaseRequest{derefDB(req.Database)}, namedDBs(req.Databases)...) {
		if d.External != nil && d.ExposePort {
			return store.Project{}, errExternalPort
		}
	}
	if req.Database != nil {
		svc, err := m.buildDatabaseService(slug, req.Database.Type, req.Database.Version, req.Database.External)
		if err != nil {
			return store.Project{}, err
		}
		proj.Services = append(proj.Services, svc)
	}
	seenDB := map[string]bool{}
	for _, extra := range req.Databases {
		if err := ValidateDatabaseServiceName(extra.Name); err != nil {
			return store.Project{}, err
		}
		if seenDB[extra.Name] {
			return store.Project{}, fmt.Errorf("%w: two databases are named %q", validate.ErrInvalid, extra.Name)
		}
		seenDB[extra.Name] = true
		svc, err := m.buildDatabaseService(slug, extra.Type, extra.Version, extra.External)
		if err != nil {
			return store.Project{}, err
		}
		svc.Kind = store.DatabaseKind(extra.Name)
		proj.Services = append(proj.Services, svc)
	}

	env, err := buildEnv(req.Env)
	if err != nil {
		return store.Project{}, err
	}
	proj.Env = env
	sort.SliceStable(proj.Services, func(i, j int) bool { return proj.Services[i].Position < proj.Services[j].Position })
	if err := enableDriverExtensions(&proj); err != nil {
		return store.Project{}, err
	}
	return proj, nil
}

// buildWebService validates the web server selection.
func (m *Manager) buildWebService(webType, version string, spa bool) (store.ProjectService, error) {
	if webType == "" {
		webType = runtime.DefaultWebServer
	}
	if !runtime.IsWebServer(webType) {
		return store.ProjectService{}, fmt.Errorf("%w: unsupported web server %q", validate.ErrInvalid, webType)
	}
	v, err := m.catalog.Resolve(webType, version)
	if err != nil {
		return store.ProjectService{}, err
	}
	raw, err := json.Marshal(runtime.WebServiceConfig{SPAFallback: spa})
	if err != nil {
		return store.ProjectService{}, err
	}
	return store.ProjectService{Kind: store.ServiceWeb, Variant: webType, Version: v.Version, Image: v.Image, Enabled: true, Config: raw, Position: 20}, nil
}

// buildDatabaseService validates the database selection and generates credentials, or
// takes those of an external server (whose reachability create and update check).
func (m *Manager) buildDatabaseService(slug, dbType, version string, ext *ExternalDatabase) (store.ProjectService, error) {
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
	var cfg runtime.DatabaseConfig
	if ext != nil {
		cfg = runtime.DatabaseConfig{Host: ext.Host, Port: ext.Port, Username: ext.Username, Password: ext.Password, Database: ext.Database}
		if err := runtime.NormalizeExternalDatabase(&cfg, dbType); err != nil {
			return store.ProjectService{}, err
		}
	} else if cfg, err = runtime.NewDatabaseConfig(slug); err != nil {
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
	position, raw := 6, json.RawMessage(`{"hostPort":0}`)
	switch kind {
	case store.ServiceMailpit:
		position = 7
	case store.ServiceRabbitMQ:
		cfg, err := runtime.NewRabbitMQConfig()
		if err != nil {
			return store.ProjectService{}, err
		}
		if raw, err = json.Marshal(cfg); err != nil {
			return store.ProjectService{}, err
		}
		position = 9
	case store.ServiceMeilisearch, store.ServiceTypesense:
		cfg, err := runtime.NewSearchConfig()
		if err != nil {
			return store.ProjectService{}, err
		}
		if raw, err = json.Marshal(cfg); err != nil {
			return store.ProjectService{}, err
		}
		position = 9
	case store.ServiceOpenSearch, store.ServiceOpenSearchDashboards, store.ServiceOllama:
		position = 9
	}
	return store.ProjectService{Kind: kind, Variant: string(kind), Version: v.Version, Image: v.Image, Enabled: true, Config: raw, Position: position}, nil
}

// buildStorageService validates the object storage selection and generates its
// credentials and bucket name.
func (m *Manager) buildStorageService(slug, version string, publicRead *bool) (store.ProjectService, error) {
	v, err := m.catalog.Resolve("rustfs", version)
	if err != nil {
		return store.ProjectService{}, err
	}
	cfg := runtime.NewStorageConfig(runtime.StorageBucketName(slug))
	if publicRead != nil {
		cfg.PublicRead = *publicRead
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return store.ProjectService{}, err
	}
	return store.ProjectService{Kind: store.ServiceStorage, Variant: "rustfs", Version: v.Version, Image: v.Image, Enabled: true, Config: raw, Position: 8}, nil
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
		if strings.HasPrefix(key, "RUSTFS_") {
			return nil, fmt.Errorf("%w: %s is reserved for the object storage container", validate.ErrInvalid, key)
		}
		if err := validate.EnvValue(e.Value); err != nil {
			return nil, err
		}
		if strings.HasPrefix(key, "ENVORYX_") {
			return nil, fmt.Errorf("%w: environment variables starting with ENVORYX_ are reserved", validate.ErrInvalid)
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
	if cfg, ok := nodeServesApp(proj); ok && req.Template == "" && (req.Git == nil || req.Git.URL == "") {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("the dev server runs %q but nothing creates a package.json – pick a Node template, clone a repository or scaffold from the Node terminal; until then the container waits", cfg.PackageManager+" run "+cfg.Script))
	}
	if cfg, ok := pythonServesApp(proj); ok && req.Template == "" && (req.Git == nil || req.Git.URL == "") {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("the application server runs %q but nothing creates the application – pick a Python template, clone a repository or scaffold from the Python terminal; until then the container waits", strings.Join(cfg.Command(), " ")))
	}
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
		for _, db := range p.Databases() {
			var cfg runtime.DatabaseConfig
			if json.Unmarshal(db.Config, &cfg) == nil && cfg.HostPort > 0 {
				used[cfg.HostPort] = true
			}
		}
		for _, kind := range extraPortKinds {
			if svc := p.Service(kind); svc != nil {
				var cfg runtime.ServiceConfig
				if json.Unmarshal(svc.Config, &cfg) == nil {
					if cfg.HostPort > 0 {
						used[cfg.HostPort] = true
					}
					if cfg.WebUIPort > 0 {
						used[cfg.WebUIPort] = true
					}
				}
			}
		}
		if svc := p.Service(store.ServiceNode); svc != nil {
			var cfg runtime.NodeConfig
			if json.Unmarshal(svc.Config, &cfg) == nil {
				if cfg.HostPort > 0 {
					used[cfg.HostPort] = true
				}
				if cfg.InspectHostPort > 0 {
					used[cfg.InspectHostPort] = true
				}
			}
		}
		if svc := p.Service(store.ServicePython); svc != nil {
			var cfg runtime.PythonConfig
			if json.Unmarshal(svc.Config, &cfg) == nil {
				if cfg.HostPort > 0 {
					used[cfg.HostPort] = true
				}
				if cfg.DebugHostPort > 0 {
					used[cfg.DebugHostPort] = true
				}
			}
		}
		if svc := p.Service(store.ServiceStorage); svc != nil {
			var cfg runtime.StorageConfig
			if json.Unmarshal(svc.Config, &cfg) == nil {
				if cfg.HostPort > 0 {
					used[cfg.HostPort] = true
				}
				if cfg.ConsolePort > 0 {
					used[cfg.ConsolePort] = true
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
// (database, Redis, Memcached, RabbitMQ, Typesense, OpenSearch, Ollama) and always for the web UIs of Mailpit,
// RabbitMQ and Meilisearch. Ports already chosen for this project are excluded so the
// allocations do not collide with each other.
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
	for _, extra := range req.Databases {
		if extra.ExposePort {
			if err := assign(store.DatabaseKind(extra.Name)); err != nil {
				return err
			}
		}
	}
	if req.Redis != nil && req.Redis.ExposePort {
		if err := assign(store.ServiceRedis); err != nil {
			return err
		}
	}
	if req.Memcached != nil && req.Memcached.ExposePort {
		if err := assign(store.ServiceMemcached); err != nil {
			return err
		}
	}
	if req.Mailpit != nil {
		if err := assign(store.ServiceMailpit); err != nil {
			return err
		}
	}
	if req.RabbitMQ != nil {
		if req.RabbitMQ.ExposePort {
			if err := assign(store.ServiceRabbitMQ); err != nil {
				return err
			}
		}
		if svc := proj.Service(store.ServiceRabbitMQ); svc != nil {
			port, err := m.allocatePort(ctx, taken...)
			if err != nil {
				return err
			}
			taken = append(taken, port)
			if err := setWebUIPort(svc, port); err != nil {
				return err
			}
		}
	}
	// Meilisearch's dashboard is served on its API port.
	if req.Meilisearch != nil {
		if err := assign(store.ServiceMeilisearch); err != nil {
			return err
		}
	}
	if req.Typesense != nil && req.Typesense.ExposePort {
		if err := assign(store.ServiceTypesense); err != nil {
			return err
		}
	}
	if req.OpenSearch != nil && req.OpenSearch.ExposePort {
		if err := assign(store.ServiceOpenSearch); err != nil {
			return err
		}
	}
	if req.Ollama != nil && req.Ollama.ExposePort {
		if err := assign(store.ServiceOllama); err != nil {
			return err
		}
	}
	// Dashboards is a web UI: always published.
	if req.OpenSearch != nil && req.OpenSearch.Dashboards {
		if err := assign(store.ServiceOpenSearchDashboards); err != nil {
			return err
		}
	}
	if req.Storage != nil {
		// The S3 API and the console are always published: local tools and the browser
		// (presigned URLs) need to reach them; the console is a web UI like Mailpit's.
		if err := m.assignStoragePorts(ctx, proj, &taken); err != nil {
			return err
		}
	}
	if req.Node != nil && req.Node.Config.DevServer {
		if err := assign(store.ServiceNode); err != nil {
			return err
		}
		if req.Node.Config.Inspect {
			if err := m.assignDebugPort(ctx, proj, store.ServiceNode, &taken); err != nil {
				return err
			}
		}
	}
	if req.Python != nil {
		if req.Python.Config.Server {
			if err := assign(store.ServicePython); err != nil {
				return err
			}
		}
		// The debugger port is independent of the server: a tooling container gets one too.
		if req.Python.Config.Debug {
			if err := m.assignDebugPort(ctx, proj, store.ServicePython, &taken); err != nil {
				return err
			}
		}
	}
	return nil
}

// assignDebugPort publishes the Node inspector or Python's debugpy on a host port of its
// own.
func (m *Manager) assignDebugPort(ctx context.Context, proj *store.Project, kind store.ServiceKind, taken *[]int) error {
	svc := proj.Service(kind)
	if svc == nil {
		return nil
	}
	port, err := m.allocatePort(ctx, *taken...)
	if err != nil {
		return err
	}
	*taken = append(*taken, port)
	var raw []byte
	switch kind {
	case store.ServicePython:
		var cfg runtime.PythonConfig
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return err
		}
		cfg.DebugHostPort = port
		raw, err = json.Marshal(cfg)
	default:
		var cfg runtime.NodeConfig
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return err
		}
		cfg.InspectHostPort = port
		raw, err = json.Marshal(cfg)
	}
	if err != nil {
		return err
	}
	svc.Config = raw
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
	if svc.Kind == store.ServicePython {
		var cfg runtime.PythonConfig
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
	if svc.Kind.IsDatabase() {
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
// catalogue owns the version→image mapping, so a Envoryx update that ships a new runtime
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
		case store.ServicePython:
			key = "python"
		case store.ServiceRedis, store.ServiceMemcached, store.ServiceMailpit, store.ServiceRabbitMQ, store.ServiceMeilisearch, store.ServiceTypesense, store.ServiceOpenSearch, store.ServiceOpenSearchDashboards, store.ServiceOllama:
			key = string(svc.Kind)
		case store.ServiceWeb, store.ServiceDatabase, store.ServiceStorage:
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
		if w := m.venvWarning(p); w != "" {
			st.Warnings = append(st.Warnings, w)
		}
		st.Warnings = append(st.Warnings, m.oomWarnings(p.ID)...)
		m.addHealth(p, &st)
		st.Operation = m.progress.active(p.ID)
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
	if w := m.venvWarning(p); w != "" {
		st.Warnings = append(st.Warnings, w)
	}
	st.Warnings = append(st.Warnings, m.oomWarnings(p.ID)...)
	m.addHealth(p, &st)
	st.Operation = m.progress.active(p.ID)
	return View{Project: p, Status: st, HTTPPort: p.HTTPPort}, nil
}

// Operations lists the running and recently finished project operations.
func (m *Manager) Operations() []Operation { return m.progress.list() }

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
	for _, d := range plan.Dirs {
		if err := os.MkdirAll(d.Path, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", d.Path, err)
		}
		_ = os.Chown(d.Path, d.UID, d.GID)
	}
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
 * Envoryx starter page. Replace this file with your application.
 */
$project = getenv('ENVORYX_PROJECT') ?: 'project';
?>
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title><?= htmlspecialchars($project) ?> · Envoryx</title>
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
  <p>Your Envoryx project is served by PHP <?= PHP_VERSION ?> (<?= php_sapi_name() ?>).</p>
  <p>Document root: <code><?= htmlspecialchars($_SERVER['DOCUMENT_ROOT'] ?? '') ?></code></p>
  <p>Replace <code>index.php</code> to get started.</p>
</main>
</body>
</html>
`

// starterIndexHTML is the starter page of projects without PHP; "{{project}}" is replaced
// with the slug when the file is written since a static page cannot read the environment.
const starterIndexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{project}} · Envoryx</title>
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
  <h1>{{project}}</h1>
  <p>Your Envoryx project is served by the web server from its document root. Build your app into this directory, or enable a Python server or Node dev server in the Runtime tab.</p>
  <p>Replace <code>index.html</code> to get started.</p>
</main>
</body>
</html>
`

// starterPage returns the file name and content of the starter page for a project: PHP
// projects get index.php (the front controller convention), everything else index.html.
func starterPage(proj store.Project) (name, content string) {
	if proj.Service(store.ServicePHP) != nil {
		return "index.php", starterIndexPHP
	}
	return "index.html", strings.ReplaceAll(starterIndexHTML, "{{project}}", proj.Slug)
}

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
			name, content := starterPage(proj)
			target := filepath.Join(docroot, name)
			if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write starter page: %w", err)
			}
			_ = os.Chown(target, planner.paths.PUID, planner.paths.PGID)
		}
	}
	return nil
}

// chownTree changes ownership of dir and its direct children created by Envoryx. Errors are
// ignored: on hosts where Envoryx does not run as root the files already belong to the user.
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
