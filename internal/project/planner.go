package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Container mount targets shared by all project containers.
const (
	appMountTarget = "/var/www/html"
	phpIniTarget   = "/usr/local/etc/php/conf.d/zz-envoryx.ini"
	phpPoolTarget  = "/usr/local/etc/php-fpm.d/zz-envoryx.conf"
	stopTimeoutSec = 10
)

// Paths tells the planner where things live inside the Envoryx container and on the host.
type Paths struct {
	ConfigDir        string // e.g. /config
	ConfigHostDir    string // e.g. /mnt/user/appdata/envoryx
	ProjectsDir      string // e.g. /projects
	ProjectsHostDir  string // e.g. /mnt/user/development
	BackupsDir       string // e.g. /backups; "" = <ConfigDir>/backups
	PUID, PGID       int
	EnvoryxVersion   string
	PublishInterface string // host IP to bind ports to; "" = all
	// SelfContainerID is Envoryx's own container id ("" when running on bare metal). The
	// embedded proxy joins project networks through it.
	SelfContainerID string
	// BaseDomain is the proxy base domain (for dev-server host allow-lists).
	BaseDomain string
	// XdebugClientHost is the global fallback debugger host (developer machine).
	XdebugClientHost string
	// FolderViewFolder is the FolderView3 folder the containers are labelled for ("" = none).
	FolderViewFolder string
	// PublicHost and the proxy's host-side ports let the planner build URLs that a
	// browser on the LAN can reach (object storage public URL). Zero = unknown.
	PublicHost     string
	ProxyHTTPPort  int
	ProxyHTTPSPort int
}

// FilePlan is a generated configuration file.
type FilePlan struct {
	Path    string // absolute path inside the Envoryx container
	Content string
	Mode    uint32
}

// ContainerPlan is a planned container.
type ContainerPlan struct {
	Kind store.ServiceKind
	Spec docker.ContainerSpec
	// Order defines start order (lower first). Stop order is the reverse.
	Order int
}

// Plan is the complete set of Docker resources and files for a project.
type Plan struct {
	ProjectID   string
	Slug        string
	NetworkName string
	Labels      map[string]string
	Volumes     []string
	Containers  []ContainerPlan
	Files       []FilePlan
	Images      []string
	ConfigDir   string // per-project config dir inside the Envoryx container
	// Dirs are directories created (owned by PUID:PGID) before containers start.
	Dirs []DirPlan
}

// DirPlan is a directory Envoryx creates for a project (e.g. the tool home).
type DirPlan struct {
	Path     string
	UID, GID int
}

const (
	// homeMountTarget is the writable home of the project user inside php/node/worker
	// containers: tool caches (composer, npm) and IDE helpers persist there.
	homeMountTarget = "/home/envoryx"
	homeDirName     = "home"
)

// toolEnv are the variables that point tools at the persistent home. The package
// managers' download caches are shared by every project instead (packageCacheEnv).
var toolEnv = []string{"HOME=" + homeMountTarget, "COMPOSER_HOME=" + homeMountTarget + "/.composer", "COMPOSER_NO_INTERACTION=1"}

// The package cache is one directory for all projects (/config/cache on the Envoryx
// side), so a package is downloaded once whichever project asks for it next: Composer,
// npm, Yarn, pip and uv keep their caches below it. pnpm is left out: its store is only
// configurable as npm_config_store_dir, which makes every npm command warn. Every container a package
// manager runs in has it mounted – the application containers, the workers and the
// one-shots that scaffold a template.
const (
	packageCacheDir    = "cache"
	packageCacheTarget = "/var/cache/envoryx"
)

// packageCacheEnv points the package managers at the shared cache. uv links from its
// cache where it can; across file systems it copies, and it is told so up front instead
// of warning about it on every install.
var packageCacheEnv = []string{
	"COMPOSER_CACHE_DIR=" + packageCacheTarget + "/composer",
	"npm_config_cache=" + packageCacheTarget + "/npm",
	"YARN_CACHE_FOLDER=" + packageCacheTarget + "/yarn",
	"PIP_CACHE_DIR=" + packageCacheTarget + "/pip",
	"UV_CACHE_DIR=" + packageCacheTarget + "/uv",
	// Go's module cache is read-only once written (Go marks it so): shared it saves the
	// downloads; the build cache makes a rebuild after a container recreate incremental.
	"GOMODCACHE=" + packageCacheTarget + "/gomod",
	"GOCACHE=" + packageCacheTarget + "/gobuild",
	"UV_LINK_MODE=copy",
}

// PackageCacheDir is the shared package cache on the Envoryx side.
func (p *Planner) PackageCacheDir() string { return filepath.Join(p.paths.ConfigDir, packageCacheDir) }

// packageCacheMount is the bind mount of the shared package cache.
func (p *Planner) packageCacheMount() docker.MountSpec {
	return docker.MountSpec{Type: "bind", Source: filepath.Join(p.paths.ConfigHostDir, packageCacheDir), Target: packageCacheTarget}
}

// withPackageCache gives a container the shared package cache: the mount and the
// variables that point the package managers at it.
func (p *Planner) withPackageCache(spec *docker.ContainerSpec) {
	spec.Env = append(spec.Env, packageCacheEnv...)
	spec.Mounts = append(spec.Mounts, p.packageCacheMount())
}

// The Ollama model store is one directory for all projects (/config/ollama on the
// Envoryx side): a model is pulled once, whichever project runs it. Ollama writes blobs
// under a temporary name and renames them, so two projects pulling at once do not
// corrupt each other.
const (
	ollamaModelsDir    = "ollama"
	ollamaModelsTarget = "/models"
)

// OllamaModelsDir is the shared Ollama model store on the Envoryx side.
func (p *Planner) OllamaModelsDir() string { return filepath.Join(p.paths.ConfigDir, ollamaModelsDir) }

// pythonVenvPath is the project's virtual environment as every container sees it: the
// app mount plus runtime.PythonVenv. The container PATH, the Python actions and the
// Python templates all create and use this one path.
const pythonVenvPath = appMountTarget + "/" + runtime.PythonVenv

// pythonEnv makes the project's virtual environment the default interpreter: PATH starts
// with .venv/bin (python, pip, gunicorn … resolve there once it exists) and the user site
// (pip install --user lands in the persistent home), pip/uv caches persist in the home.
var pythonEnv = []string{
	"VIRTUAL_ENV=" + pythonVenvPath,
	"PATH=" + pythonVenvPath + "/bin:" + homeMountTarget + "/.local/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin",
	"PYTHONUNBUFFERED=1",
}

// goPath is GOPATH in the persistent home: `go install`ed tools (golangci-lint, sqlc …)
// survive a container recreate, and its bin/ is on the PATH.
const goPath = homeMountTarget + "/go"

// goEnv gives the Go containers their GOPATH and a PATH that finds go, installed tools,
// air and dlv.
var goEnv = []string{
	"GOPATH=" + goPath,
	"PATH=" + goPath + "/bin:/usr/local/go/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin",
}

// jetbrainsCacheDir is the shared, host-wide cache for JetBrains Gateway IDE backends
// (~1.5 GB per IDE version) so it is downloaded once for all projects.
const jetbrainsCacheDir = "jetbrains"

// BackupsRoot is the directory that holds one sub-directory of backups per project.
func (p Paths) BackupsRoot() string {
	if p.BackupsDir != "" {
		return p.BackupsDir
	}
	return filepath.Join(p.ConfigDir, "backups")
}

// gatewayMounts returns the extra mounts for JetBrains Gateway sessions.
func (p *Planner) gatewayMounts(proj store.Project) []docker.MountSpec {
	if !proj.IDEGateway {
		return nil
	}
	return []docker.MountSpec{{Type: "bind", Source: filepath.Join(p.paths.ConfigHostDir, jetbrainsCacheDir), Target: homeMountTarget + "/.cache/JetBrains"}}
}

// HomeMount is the bind mount of the project home for application containers.
func (p *Planner) HomeMount(proj store.Project) docker.MountSpec {
	return docker.MountSpec{Type: "bind", Source: filepath.Join(p.configHostDir(proj.ID), homeDirName), Target: homeMountTarget}
}

// HomeDir is the project home on the Envoryx side (SFTP root for /home/envoryx).
func (p *Planner) HomeDir(proj store.Project) string {
	return filepath.Join(p.ProjectConfigDir(proj.ID), homeDirName)
}

// Planner turns a project's desired state into a Plan.
type Planner struct {
	paths   Paths
	catalog *runtime.Catalog
}

// NewPlanner creates a planner.
func NewPlanner(paths Paths, catalog *runtime.Catalog) *Planner {
	return &Planner{paths: paths, catalog: catalog}
}

// NetworkName returns the project network name.
func NetworkName(slug string) string { return "envoryx-" + slug }

// ContainerName returns the container name for a project service.
func ContainerName(slug string, kind store.ServiceKind) string {
	return fmt.Sprintf("envoryx-%s-%s", slug, kind)
}

// ProjectConfigDir returns the per-project config directory inside the Envoryx container.
func (p *Planner) ProjectConfigDir(projectID string) string {
	return filepath.Join(p.paths.ConfigDir, "projects", projectID)
}

// ProjectDir returns the project directory inside the Envoryx container.
func (p *Planner) ProjectDir(proj store.Project) string {
	return filepath.Join(p.paths.ProjectsDir, filepath.FromSlash(proj.Path))
}

func (p *Planner) projectHostDir(proj store.Project) string {
	return filepath.Join(p.paths.ProjectsHostDir, filepath.FromSlash(proj.Path))
}

func (p *Planner) configHostDir(projectID string) string {
	return filepath.Join(p.paths.ConfigHostDir, "projects", projectID)
}

// Plan builds the plan for a project. Disabled services are skipped.
func (p *Planner) Plan(proj store.Project) (Plan, error) {
	if p.paths.ProjectsHostDir == "" || p.paths.ConfigHostDir == "" {
		return Plan{}, fmt.Errorf("%w: host paths for /projects and /config are unknown", ErrNotConfigured)
	}
	plan := Plan{
		ProjectID:   proj.ID,
		Slug:        proj.Slug,
		NetworkName: NetworkName(proj.Slug),
		Labels:      docker.ManagedLabels(proj.ID, proj.Slug, "", p.paths.EnvoryxVersion),
		ConfigDir:   p.ProjectConfigDir(proj.ID),
	}
	appHost := p.projectHostDir(proj)
	cfgHost := p.configHostDir(proj.ID)
	plan.Dirs = append(plan.Dirs, DirPlan{Path: p.HomeDir(proj), UID: p.paths.PUID, GID: p.paths.PGID}, DirPlan{Path: p.PackageCacheDir(), UID: p.paths.PUID, GID: p.paths.PGID})
	if proj.IDEGateway {
		plan.Dirs = append(plan.Dirs, DirPlan{Path: filepath.Join(p.paths.ConfigDir, jetbrainsCacheDir), UID: p.paths.PUID, GID: p.paths.PGID})
	}
	env, err := p.envStrings(proj)
	if err != nil {
		return Plan{}, err
	}
	images := map[string]bool{}

	// An application server that opens its connections at boot (Django checks migrations,
	// many frameworks connect on start) dies in the seconds the database needs to
	// initialise. The restart policy covers most of them, but Django's runserver keeps its
	// autoreload parent alive after the failed child: the container stays "running" and
	// answers nothing until someone restarts it. The server therefore waits for the
	// database instead of racing it.
	dbGuard := ""
	if db := proj.Service(store.ServiceDatabase); db != nil && db.Enabled {
		if d, ok := runtime.DialectFor(db.Variant); ok {
			dbGuard = runtime.WaitForTCP("database", d.Port, d.Variant)
			var cfg runtime.DatabaseConfig
			if json.Unmarshal(db.Config, &cfg) == nil && cfg.External() {
				dbGuard = runtime.WaitForTCP(cfg.Host, cfg.Port, d.Variant)
			}
		}
	}
	external := false
	for _, svc := range proj.Services {
		if !svc.Enabled {
			continue
		}
		labels := docker.ManagedLabels(proj.ID, proj.Slug, string(svc.Kind), p.paths.EnvoryxVersion)
		if externalService(&svc) {
			// A server Envoryx does not run: no container, no volume – only the variables.
			external = true
			continue
		}
		if svc.Kind.IsDatabase() {
			c, volume, err := p.databaseContainer(proj, svc, plan.NetworkName, labels)
			if err != nil {
				return Plan{}, err
			}
			plan.Volumes = append(plan.Volumes, volume)
			plan.Containers = append(plan.Containers, c)
			images[svc.Image] = true
			continue
		}
		switch svc.Kind {
		case store.ServicePHP:
			var cfg runtime.PHPConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("php config: %w", err)
			}
			if err := cfg.Normalize(); err != nil {
				return Plan{}, err
			}
			plan.Files = append(plan.Files,
				FilePlan{Path: filepath.Join(plan.ConfigDir, "php", "zz-envoryx.ini"), Content: cfg.INIWith(svc.Version, runtime.INIOptions{XdebugClientHost: p.paths.XdebugClientHost}), Mode: 0o644},
				FilePlan{Path: filepath.Join(plan.ConfigDir, "php", "zz-envoryx.conf"), Content: runtime.FPMPool(p.paths.PUID, p.paths.PGID), Mode: 0o644},
			)
			phpSpec := docker.ContainerSpec{
				Name:         ContainerName(proj.Slug, store.ServicePHP),
				Image:        svc.Image,
				Labels:       labels,
				Env:          env,
				WorkingDir:   appMountTarget,
				Network:      plan.NetworkName,
				NetworkAlias: []string{"php"},
				Mounts: append([]docker.MountSpec{
					{Type: "bind", Source: appHost, Target: appMountTarget},
					{Type: "bind", Source: filepath.Join(cfgHost, "php", "zz-envoryx.ini"), Target: phpIniTarget, ReadOnly: true},
					{Type: "bind", Source: filepath.Join(cfgHost, "php", "zz-envoryx.conf"), Target: phpPoolTarget, ReadOnly: true},
					p.HomeMount(proj),
				}, p.gatewayMounts(proj)...),
				RestartPolicy: "unless-stopped",
				StopTimeout:   stopTimeoutSec,
			}
			p.withPackageCache(&phpSpec)
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServicePHP, Order: 10, Spec: phpSpec})
			images[svc.Image] = true

		case store.ServiceWeb:
			php := proj.Service(store.ServicePHP)
			hasPHP := php != nil && php.Enabled
			wcfg, err := webServiceConfig(svc)
			if err != nil {
				return Plan{}, err
			}
			cfg, err := runtime.WebServerConfig(svc.Variant, proj.Docroot, runtime.WebOptions{PHP: hasPHP, SPAFallback: wcfg.SPAFallback && !hasPHP})
			if err != nil {
				return Plan{}, err
			}
			plan.Files = append(plan.Files,
				FilePlan{Path: filepath.Join(plan.ConfigDir, "web", cfg.FileName), Content: cfg.Content, Mode: 0o644},
			)
			spec := docker.ContainerSpec{
				Name:         ContainerName(proj.Slug, store.ServiceWeb),
				Image:        svc.Image,
				Labels:       labels,
				Network:      plan.NetworkName,
				NetworkAlias: []string{"web"},
				Mounts: []docker.MountSpec{
					{Type: "bind", Source: appHost, Target: appMountTarget, ReadOnly: true},
					{Type: "bind", Source: filepath.Join(cfgHost, "web", cfg.FileName), Target: cfg.Target, ReadOnly: true},
				},
				RestartPolicy: "unless-stopped",
				StopTimeout:   stopTimeoutSec,
			}
			// While a Python server or Node dev server serves the app the proxy bypasses
			// this container and its port stays unpublished so the docroot (often the
			// project root with .env and sources) is not exposed on the LAN. The port stays
			// allocated so turning the server off publishes it again.
			if proj.HTTPPort > 0 && !appServesDirectly(proj) {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: proj.HTTPPort, ContainerPort: 80, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceWeb, Order: 20, Spec: spec})
			images[svc.Image] = true

		case store.ServiceNode:
			var ncfg runtime.NodeConfig
			if len(svc.Config) > 0 {
				if err := json.Unmarshal(svc.Config, &ncfg); err != nil {
					return Plan{}, fmt.Errorf("node config: %w", err)
				}
			}
			if err := ncfg.Normalize(); err != nil {
				return Plan{}, err
			}
			spec := docker.ContainerSpec{
				Name:   ContainerName(proj.Slug, store.ServiceNode),
				Image:  svc.Image,
				Labels: labels,
				// Tooling container: idles until actions or the terminal run commands.
				Cmd:           []string{"sleep", "infinity"},
				Env:           append(append(append([]string{}, env...), toolEnv...), "NODE_ENV=development"),
				User:          fmt.Sprintf("%d:%d", p.paths.PUID, p.paths.PGID),
				WorkingDir:    appMountTarget,
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"node"},
				Mounts:        append([]docker.MountSpec{{Type: "bind", Source: appHost, Target: appMountTarget}, p.HomeMount(proj)}, p.gatewayMounts(proj)...),
				RestartPolicy: "unless-stopped",
				StopTimeout:   5,
			}
			p.withPackageCache(&spec)
			if ncfg.DevServer {
				// Dev-server mode: the script is the main process; the proxy routes
				// <slug>-dev.<base> to it and the host port publishes it directly.
				spec.Cmd = runtime.Guarded(ncfg.Command(), "envoryx-dev", dbGuard)
				if _, ok := nodeServesApp(proj); ok {
					// The project URL points here too and a blank project has no
					// package.json yet: wait for it instead of crash-looping.
					spec.Cmd = ncfg.WrappedCommand(dbGuard)
				}
				// One leading-dot entry: Vite suffix-matches it, so <slug>.<base>,
				// <slug>-dev.<base> and every extra domain under the base domain pass
				// without the planner knowing the domain table. Vite < 8.3 reads the
				// variable as a single host, so nothing is comma-joined here.
				spec.Env = append(spec.Env, ncfg.Env("."+p.paths.BaseDomain)...)
				if ncfg.HostPort > 0 {
					spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: ncfg.HostPort, ContainerPort: ncfg.Port, Protocol: "tcp"}}
				}
				if ncfg.Inspect && ncfg.InspectHostPort > 0 {
					// The inspector itself is started by the script (see NodeConfig.Inspect).
					spec.Ports = append(spec.Ports, docker.PortSpec{HostIP: p.paths.PublishInterface, HostPort: ncfg.InspectHostPort, ContainerPort: ncfg.InspectPort, Protocol: "tcp"})
				}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceNode, Order: 15, Spec: spec})
			images[svc.Image] = true

		case store.ServicePython:
			var pcfg runtime.PythonConfig
			if len(svc.Config) > 0 {
				if err := json.Unmarshal(svc.Config, &pcfg); err != nil {
					return Plan{}, fmt.Errorf("python config: %w", err)
				}
			}
			if err := pcfg.Normalize(); err != nil {
				return Plan{}, err
			}
			spec := docker.ContainerSpec{
				Name:   ContainerName(proj.Slug, store.ServicePython),
				Image:  svc.Image,
				Labels: labels,
				// Tooling container: idles until actions or the terminal run commands.
				Cmd:           []string{"sleep", "infinity"},
				Env:           append(append(append([]string{}, env...), toolEnv...), pythonEnv...),
				User:          fmt.Sprintf("%d:%d", p.paths.PUID, p.paths.PGID),
				WorkingDir:    appMountTarget,
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"python"},
				Mounts:        append([]docker.MountSpec{{Type: "bind", Source: appHost, Target: appMountTarget}, p.HomeMount(proj)}, p.gatewayMounts(proj)...),
				RestartPolicy: "unless-stopped",
				StopTimeout:   stopTimeoutSec,
			}
			p.withPackageCache(&spec)
			if pcfg.Server {
				// Server mode: the application server is the main process, published on a
				// host port; without PHP the proxy routes the project URL to it.
				spec.Cmd = runtime.Guarded(pcfg.Command(), "envoryx-serve", dbGuard)
				if _, ok := pythonServesApp(proj); ok {
					// A blank project has nothing to run yet: wait for the entry file
					// instead of crash-looping.
					spec.Cmd = pcfg.WrappedCommand(dbGuard)
				}
				spec.Env = append(spec.Env, pcfg.Env()...)
				if pcfg.HostPort > 0 {
					spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: pcfg.HostPort, ContainerPort: pcfg.Port, Protocol: "tcp"}}
				}
			}
			if pcfg.Debug && pcfg.DebugHostPort > 0 {
				// debugpy itself is started by whatever process the developer launches (see
				// PythonConfig.Debug) – in the application server, or by hand in the
				// terminal, which is why the port does not depend on the server.
				spec.Ports = append(spec.Ports, docker.PortSpec{HostIP: p.paths.PublishInterface, HostPort: pcfg.DebugHostPort, ContainerPort: pcfg.DebugPort, Protocol: "tcp"})
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServicePython, Order: 12, Spec: spec})
			images[svc.Image] = true

		case store.ServiceGo:
			var gcfg runtime.GoConfig
			if len(svc.Config) > 0 {
				if err := json.Unmarshal(svc.Config, &gcfg); err != nil {
					return Plan{}, fmt.Errorf("go config: %w", err)
				}
			}
			if err := gcfg.Normalize(); err != nil {
				return Plan{}, err
			}
			spec := docker.ContainerSpec{
				Name:   ContainerName(proj.Slug, store.ServiceGo),
				Image:  svc.Image,
				Labels: labels,
				// Tooling container: idles until actions or the terminal run commands.
				Cmd:           []string{"sleep", "infinity"},
				Env:           append(append(append([]string{}, env...), toolEnv...), goEnv...),
				User:          fmt.Sprintf("%d:%d", p.paths.PUID, p.paths.PGID),
				WorkingDir:    appMountTarget,
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"go"},
				Mounts:        append([]docker.MountSpec{{Type: "bind", Source: appHost, Target: appMountTarget}, p.HomeMount(proj)}, p.gatewayMounts(proj)...),
				RestartPolicy: "unless-stopped",
				StopTimeout:   stopTimeoutSec,
			}
			p.withPackageCache(&spec)
			if gcfg.Server {
				// Server mode: the build and the binary (under air in dev mode) are the main
				// process, published on a host port; without PHP or a Python server the proxy
				// routes the project URL to it.
				spec.Cmd = runtime.Guarded(gcfg.Command(), "envoryx-serve", dbGuard)
				if _, ok := goServesApp(proj); ok {
					// A blank project has nothing to build yet: wait for go.mod instead of
					// crash-looping.
					spec.Cmd = gcfg.WrappedCommand(dbGuard)
				}
				spec.Env = append(spec.Env, gcfg.Env()...)
				if gcfg.HostPort > 0 {
					spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: gcfg.HostPort, ContainerPort: gcfg.Port, Protocol: "tcp"}}
				}
			}
			if gcfg.Debug && gcfg.DebugHostPort > 0 {
				// With the server Delve runs it; without, the port waits for a dlv started in
				// the terminal (dlv debug/test --headless --listen=:2345).
				spec.Ports = append(spec.Ports, docker.PortSpec{HostIP: p.paths.PublishInterface, HostPort: gcfg.DebugHostPort, ContainerPort: gcfg.DebugPort, Protocol: "tcp"})
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceGo, Order: 12, Spec: spec})
			images[svc.Image] = true

		case store.ServiceRedis:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("redis config: %w", err)
			}
			volume := VolumeName(proj.Slug, store.ServiceRedis)
			plan.Volumes = append(plan.Volumes, volume)
			spec := docker.ContainerSpec{
				Name:          ContainerName(proj.Slug, store.ServiceRedis),
				Image:         svc.Image,
				Labels:        labels,
				Cmd:           []string{"redis-server", "--appendonly", "yes", "--save", "60", "1"},
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"redis"},
				Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: "/data"}},
				RestartPolicy: "unless-stopped",
				StopTimeout:   10,
				Healthcheck:   &docker.HealthSpec{Test: []string{"redis-cli", "ping"}, Interval: 10 * time.Second, Timeout: 3 * time.Second, StartPeriod: 5 * time.Second, Retries: 3},
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: 6379, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceRedis, Order: 6, Spec: spec})
			images[svc.Image] = true

		case store.ServiceMemcached:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("memcached config: %w", err)
			}
			// No volume: Memcached keeps everything in memory, a restart empties the cache.
			spec := docker.ContainerSpec{
				Name:          ContainerName(proj.Slug, store.ServiceMemcached),
				Image:         svc.Image,
				Labels:        labels,
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"memcached"},
				RestartPolicy: "unless-stopped",
				StopTimeout:   5,
				Healthcheck:   &docker.HealthSpec{Test: []string{"sh", "-c", `printf 'version\r\n' | nc -w 2 127.0.0.1 11211 | grep -q VERSION`}, Interval: 10 * time.Second, Timeout: 3 * time.Second, StartPeriod: 5 * time.Second, Retries: 3},
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.MemcachedPort, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceMemcached, Order: 6, Spec: spec})
			images[svc.Image] = true

		case store.ServiceMailpit:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("mailpit config: %w", err)
			}
			spec := docker.ContainerSpec{
				Name:          ContainerName(proj.Slug, store.ServiceMailpit),
				Image:         svc.Image,
				Labels:        labels,
				Env:           []string{"MP_SMTP_AUTH_ACCEPT_ANY=1", "MP_SMTP_AUTH_ALLOW_INSECURE=1"},
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"mailpit", "mail"},
				RestartPolicy: "unless-stopped",
				StopTimeout:   5,
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: 8025, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceMailpit, Order: 7, Spec: spec})
			images[svc.Image] = true

		case store.ServiceRabbitMQ:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("rabbitmq config: %w", err)
			}
			if cfg.Username == "" || cfg.Password == "" {
				return Plan{}, fmt.Errorf("rabbitmq config for %s is incomplete", proj.Slug)
			}
			volume := VolumeName(proj.Slug, store.ServiceRabbitMQ)
			plan.Volumes = append(plan.Volumes, volume)
			spec := docker.ContainerSpec{
				Name:   ContainerName(proj.Slug, store.ServiceRabbitMQ),
				Image:  svc.Image,
				Labels: labels,
				// The node name decides the data directory inside the volume; the default
				// (rabbit@<container hostname>) changes with every recreated container and
				// would leave queues and users behind.
				Env:           []string{"RABBITMQ_NODENAME=rabbit@localhost", "RABBITMQ_DEFAULT_USER=" + cfg.Username, "RABBITMQ_DEFAULT_PASS=" + cfg.Password},
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"rabbitmq", "amqp"},
				Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: "/var/lib/rabbitmq"}},
				RestartPolicy: "unless-stopped",
				StopTimeout:   30,
				Healthcheck:   &docker.HealthSpec{Test: []string{"rabbitmq-diagnostics", "-q", "ping"}, Interval: 10 * time.Second, Timeout: 10 * time.Second, StartPeriod: 30 * time.Second, Retries: 5},
			}
			if cfg.HostPort > 0 {
				spec.Ports = append(spec.Ports, docker.PortSpec{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.RabbitMQPort, Protocol: "tcp"})
			}
			if cfg.WebUIPort > 0 {
				spec.Ports = append(spec.Ports, docker.PortSpec{HostIP: p.paths.PublishInterface, HostPort: cfg.WebUIPort, ContainerPort: runtime.RabbitMQAdminPort, Protocol: "tcp"})
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceRabbitMQ, Order: 9, Spec: spec})
			images[svc.Image] = true

		case store.ServiceMeilisearch:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("meilisearch config: %w", err)
			}
			if cfg.APIKey == "" {
				return Plan{}, fmt.Errorf("meilisearch config for %s is incomplete", proj.Slug)
			}
			volume := VolumeName(proj.Slug, store.ServiceMeilisearch)
			plan.Volumes = append(plan.Volumes, volume)
			spec := docker.ContainerSpec{
				Name:   ContainerName(proj.Slug, store.ServiceMeilisearch),
				Image:  svc.Image,
				Labels: labels,
				// development keeps the web dashboard on the API port. MEILI_UPGRADE_DB lets a
				// newer image take over the volume of an older one: without it Meilisearch
				// refuses to start on a database another version wrote.
				Env:           []string{"MEILI_ENV=development", "MEILI_MASTER_KEY=" + cfg.APIKey, "MEILI_NO_ANALYTICS=true", "MEILI_UPGRADE_DB=true"},
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"meilisearch"},
				Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: "/meili_data"}},
				RestartPolicy: "unless-stopped",
				StopTimeout:   10,
				Healthcheck:   &docker.HealthSpec{Test: []string{"curl", "-fsS", "-o", "/dev/null", fmt.Sprintf("http://127.0.0.1:%d/health", runtime.MeilisearchPort)}, Interval: 10 * time.Second, Timeout: 3 * time.Second, StartPeriod: 10 * time.Second, Retries: 3},
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.MeilisearchPort, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceMeilisearch, Order: 9, Spec: spec})
			images[svc.Image] = true

		case store.ServiceTypesense:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("typesense config: %w", err)
			}
			if cfg.APIKey == "" {
				return Plan{}, fmt.Errorf("typesense config for %s is incomplete", proj.Slug)
			}
			volume := VolumeName(proj.Slug, store.ServiceTypesense)
			plan.Volumes = append(plan.Volumes, volume)
			spec := docker.ContainerSpec{
				Name:   ContainerName(proj.Slug, store.ServiceTypesense),
				Image:  svc.Image,
				Labels: labels,
				// Typesense keeps its (single-node) Raft peer list in the data directory,
				// keyed by the peering address. The default is the container's IP, which
				// changes with every recreate; loopback stays the same.
				Env:           []string{"TYPESENSE_DATA_DIR=/data", "TYPESENSE_API_KEY=" + cfg.APIKey, "TYPESENSE_PEERING_ADDRESS=127.0.0.1"},
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"typesense"},
				Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: "/data"}},
				RestartPolicy: "unless-stopped",
				StopTimeout:   30,
				// The image has bash but neither curl nor wget.
				Healthcheck: &docker.HealthSpec{Test: []string{"bash", "-c", fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/%d && printf 'GET /health HTTP/1.0\r\n\r\n' >&3 && grep -q '"ok":true' <&3`, runtime.TypesensePort)}, Interval: 10 * time.Second, Timeout: 3 * time.Second, StartPeriod: 10 * time.Second, Retries: 3},
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.TypesensePort, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceTypesense, Order: 9, Spec: spec})
			images[svc.Image] = true

		case store.ServiceOllama:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("ollama config: %w", err)
			}
			plan.Dirs = append(plan.Dirs, DirPlan{Path: p.OllamaModelsDir(), UID: p.paths.PUID, GID: p.paths.PGID})
			// As the project user, so the shared store belongs to one owner whichever
			// project pulled a model. Ollama keeps its key pair in $HOME/.ollama; a new
			// one per container is fine, it only signs pushes to ollama.com.
			spec := docker.ContainerSpec{
				Name:          ContainerName(proj.Slug, store.ServiceOllama),
				Image:         svc.Image,
				Labels:        labels,
				Env:           []string{"HOME=/tmp", "OLLAMA_MODELS=" + ollamaModelsTarget},
				User:          fmt.Sprintf("%d:%d", p.paths.PUID, p.paths.PGID),
				Mounts:        []docker.MountSpec{{Type: "bind", Source: filepath.Join(p.paths.ConfigHostDir, ollamaModelsDir), Target: ollamaModelsTarget}},
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"ollama"},
				RestartPolicy: "unless-stopped",
				StopTimeout:   10,
				GPUs:          cfg.GPU,
				// The image has neither curl nor wget; bash talks HTTP itself.
				Healthcheck: &docker.HealthSpec{Test: []string{"bash", "-c", fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/%d && printf 'GET / HTTP/1.0\r\n\r\n' >&3 && grep -q 'Ollama is running' <&3`, runtime.OllamaPort)}, Interval: 10 * time.Second, Timeout: 3 * time.Second, StartPeriod: 10 * time.Second, Retries: 3},
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.OllamaPort, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceOllama, Order: 9, Spec: spec})
			images[svc.Image] = true

		case store.ServiceOpenSearch:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("opensearch config: %w", err)
			}
			volume := VolumeName(proj.Slug, store.ServiceOpenSearch)
			plan.Volumes = append(plan.Volumes, volume)
			spec := docker.ContainerSpec{
				Name:   ContainerName(proj.Slug, store.ServiceOpenSearch),
				Image:  svc.Image,
				Labels: labels,
				// A development node: one node (which also skips the production bootstrap
				// checks such as vm.max_map_count), plain HTTP without the security plugin
				// and its demo certificates, and a heap that leaves room for other projects.
				Env: []string{
					"discovery.type=single-node",
					"DISABLE_SECURITY_PLUGIN=true",
					"DISABLE_INSTALL_DEMO_CONFIG=true",
					"OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m",
				},
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"opensearch"},
				Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: "/usr/share/opensearch/data"}},
				RestartPolicy: "unless-stopped",
				StopTimeout:   30,
				// Yellow is the normal state of a single node: replicas have nowhere to go.
				Healthcheck: &docker.HealthSpec{Test: []string{"curl", "-fsS", "-o", "/dev/null", fmt.Sprintf("http://127.0.0.1:%d/_cluster/health?wait_for_status=yellow&timeout=2s", runtime.OpenSearchPort)}, Interval: 10 * time.Second, Timeout: 5 * time.Second, StartPeriod: 60 * time.Second, Retries: 5},
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.OpenSearchPort, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceOpenSearch, Order: 9, Spec: spec})
			images[svc.Image] = true

		case store.ServiceOpenSearchDashboards:
			var cfg runtime.ServiceConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("opensearch-dashboards config: %w", err)
			}
			// Saved objects live in OpenSearch's .kibana index, so no volume. The security
			// plugin is off like OpenSearch's.
			spec := docker.ContainerSpec{
				Name:          ContainerName(proj.Slug, store.ServiceOpenSearchDashboards),
				Image:         svc.Image,
				Labels:        labels,
				Env:           []string{fmt.Sprintf(`OPENSEARCH_HOSTS=["http://opensearch:%d"]`, runtime.OpenSearchPort), "DISABLE_SECURITY_DASHBOARDS_PLUGIN=true"},
				Network:       plan.NetworkName,
				RestartPolicy: "unless-stopped",
				StopTimeout:   10,
				Healthcheck:   &docker.HealthSpec{Test: []string{"curl", "-fsS", "-o", "/dev/null", fmt.Sprintf("http://127.0.0.1:%d/api/status", runtime.OpenSearchDashboardsPort)}, Interval: 10 * time.Second, Timeout: 5 * time.Second, StartPeriod: 60 * time.Second, Retries: 5},
			}
			if cfg.HostPort > 0 {
				spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.OpenSearchDashboardsPort, Protocol: "tcp"}}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceOpenSearchDashboards, Order: 9, Spec: spec})
			images[svc.Image] = true

		case store.ServiceStorage:
			var cfg runtime.StorageConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("storage config: %w", err)
			}
			volume := VolumeName(proj.Slug, store.ServiceStorage)
			plan.Volumes = append(plan.Volumes, volume)
			spec := docker.ContainerSpec{
				Name:          ContainerName(proj.Slug, store.ServiceStorage),
				Image:         svc.Image,
				Labels:        labels,
				Env:           cfg.ContainerEnv(),
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"s3", "storage"},
				Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: "/data"}},
				RestartPolicy: "unless-stopped",
				StopTimeout:   10,
				// Any HTTP answer means the API is up (an unsigned request gets 403).
				Healthcheck: &docker.HealthSpec{Test: []string{"curl", "-s", "-o", "/dev/null", "http://127.0.0.1:9000/"}, Interval: 10 * time.Second, Timeout: 3 * time.Second, StartPeriod: 10 * time.Second, Retries: 3},
			}
			if cfg.HostPort > 0 {
				spec.Ports = append(spec.Ports, docker.PortSpec{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: runtime.StoragePort, Protocol: "tcp"})
			}
			if cfg.ConsolePort > 0 {
				spec.Ports = append(spec.Ports, docker.PortSpec{HostIP: p.paths.PublishInterface, HostPort: cfg.ConsolePort, ContainerPort: runtime.StorageConsolePort, Protocol: "tcp"})
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceStorage, Order: 8, Spec: spec})
			images[svc.Image] = true

		default:
			return Plan{}, fmt.Errorf("service kind %q is not supported yet", svc.Kind)
		}
	}

	// Workers: one container per definition from the image of the preset's runtime (PHP
	// presets from the PHP image with its ini, Node and Python presets from their image
	// with the project home), sharing env and the project mount. A worker whose runtime
	// the project does not have is skipped – it comes back when the runtime is added.
	php, node, python, golang := proj.Service(store.ServicePHP), proj.Service(store.ServiceNode), proj.Service(store.ServicePython), proj.Service(store.ServiceGo)
	for _, w := range proj.Workers {
		if !w.Enabled {
			continue
		}
		preset, ok := workerPreset(w.Preset)
		if !ok {
			return Plan{}, fmt.Errorf("%w: unknown worker preset %q", validate.ErrInvalid, w.Preset)
		}
		cmd, err := WorkerCommand(w)
		if err != nil {
			return Plan{}, err
		}
		spec := docker.ContainerSpec{
			Name:          WorkerContainerName(proj.Slug, w),
			Labels:        docker.ManagedLabels(proj.ID, proj.Slug, string(WorkerKind(w)), p.paths.EnvoryxVersion),
			Cmd:           cmd,
			User:          fmt.Sprintf("%d:%d", p.paths.PUID, p.paths.PGID),
			WorkingDir:    appMountTarget,
			Network:       plan.NetworkName,
			NetworkAlias:  []string{"worker-" + w.Name},
			Mounts:        []docker.MountSpec{{Type: "bind", Source: appHost, Target: appMountTarget}},
			RestartPolicy: "unless-stopped",
			StopTimeout:   30,
		}
		switch preset.Runtime {
		case WorkerRuntimeNode:
			if node == nil || !node.Enabled {
				continue
			}
			spec.Image = node.Image
			spec.Env = append(append(append([]string{}, env...), toolEnv...), "NODE_ENV=development")
			spec.Mounts = append(spec.Mounts, p.HomeMount(proj))
		case WorkerRuntimePython:
			if python == nil || !python.Enabled {
				continue
			}
			spec.Image = python.Image
			spec.Env = append(append(append([]string{}, env...), toolEnv...), pythonEnv...)
			spec.Mounts = append(spec.Mounts, p.HomeMount(proj))
		case WorkerRuntimeGo:
			if golang == nil || !golang.Enabled {
				continue
			}
			spec.Image = golang.Image
			spec.Env = append(append(append([]string{}, env...), toolEnv...), goEnv...)
			spec.Mounts = append(spec.Mounts, p.HomeMount(proj))
			p.withPackageCache(&spec)
		default:
			if php == nil || !php.Enabled {
				continue
			}
			spec.Image = php.Image
			spec.Env = append(append([]string{}, env...), "HOME=/tmp", "COMPOSER_HOME=/tmp/composer")
			spec.Mounts = append(spec.Mounts, docker.MountSpec{Type: "bind", Source: filepath.Join(cfgHost, "php", "zz-envoryx.ini"), Target: phpIniTarget, ReadOnly: true})
		}
		p.withPackageCache(&spec)
		plan.Containers = append(plan.Containers, ContainerPlan{Kind: WorkerKind(w), Order: 30, Spec: spec})
	}

	// An external server may run on the Docker host itself; Linux only resolves
	// host.docker.internal when the container is told the gateway.
	if external {
		for i := range plan.Containers {
			plan.Containers[i].Spec.ExtraHosts = append(plan.Containers[i].Spec.ExtraHosts, hostGatewayEntry)
		}
	}
	// Fingerprint the structural part of every spec so ensurePlan can recreate containers
	// whose command, mounts or ports changed (env is handled explicitly by callers).
	for i := range plan.Containers {
		docker.AddUnraidLabels(plan.Containers[i].Spec.Labels, p.paths.FolderViewFolder)
		plan.Containers[i].Spec.Labels[docker.LabelSpec] = specFingerprint(plan.Containers[i].Spec)
		plan.Containers[i].Spec.Resources = containerResources(proj.Limits, plan.Containers[i].Kind)
	}
	sort.SliceStable(plan.Containers, func(i, j int) bool { return plan.Containers[i].Order < plan.Containers[j].Order })
	for img := range images {
		plan.Images = append(plan.Images, img)
	}
	sort.Strings(plan.Images)
	return plan, nil
}

// databaseContainer plans the container of a database – the primary or an additional
// one – and returns it with its volume. The primary answers as "database" (and by its
// flavour, as it always did), an additional one by its name.
func (p *Planner) databaseContainer(proj store.Project, svc store.ProjectService, network string, labels map[string]string) (ContainerPlan, string, error) {
	dialect, ok := runtime.DialectFor(svc.Variant)
	if !ok {
		return ContainerPlan{}, "", fmt.Errorf("database variant %q is not supported", svc.Variant)
	}
	var cfg runtime.DatabaseConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return ContainerPlan{}, "", fmt.Errorf("database config: %w", err)
	}
	if cfg.Password == "" || cfg.Database == "" || cfg.Username == "" || (dialect.HasRoot && cfg.RootPassword == "") {
		return ContainerPlan{}, "", fmt.Errorf("database config for %s is incomplete", proj.Slug)
	}
	aliases := []string{runtime.PrimaryDatabaseHost, svc.Variant}
	if name := svc.Kind.DatabaseName(); name != "" {
		aliases = []string{name}
	}
	volume := VolumeName(proj.Slug, svc.Kind)
	spec := docker.ContainerSpec{
		Name:          ContainerName(proj.Slug, svc.Kind),
		Image:         svc.Image,
		Labels:        labels,
		Env:           dialect.ContainerEnv(cfg),
		Cmd:           dialect.Cmd,
		Network:       network,
		NetworkAlias:  aliases,
		Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: dialect.DataDirTarget(svc.Version)}},
		RestartPolicy: "unless-stopped",
		StopTimeout:   30,
		Healthcheck: &docker.HealthSpec{
			Test:        dialect.Health,
			Interval:    10 * time.Second,
			Timeout:     5 * time.Second,
			StartPeriod: 30 * time.Second,
			Retries:     5,
		},
	}
	if cfg.HostPort > 0 {
		spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: dialect.Port, Protocol: "tcp"}}
	}
	return ContainerPlan{Kind: svc.Kind, Order: 5, Spec: spec}, volume, nil
}

// webServiceConfig reads the web service's options; an empty or "{}" config (every project
// created before the SPA fallback existed) means defaults.
func webServiceConfig(svc store.ProjectService) (runtime.WebServiceConfig, error) {
	var cfg runtime.WebServiceConfig
	if len(svc.Config) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("web config: %w", err)
	}
	return cfg, nil
}

// Container returns the planned container for a kind, or nil.
func (pl *Plan) Container(kind store.ServiceKind) *ContainerPlan {
	for i := range pl.Containers {
		if pl.Containers[i].Kind == kind {
			return &pl.Containers[i]
		}
	}
	return nil
}

// Preview renders a human readable summary of a plan.
func (p *Planner) Preview(proj store.Project, plan Plan) Preview {
	pv := Preview{
		Slug:     proj.Slug,
		Path:     path.Join(p.paths.ProjectsDir, proj.Path),
		HostPath: p.projectHostDir(proj),
		HTTPPort: proj.HTTPPort,
		Network:  plan.NetworkName,
		Volumes:  append([]string{}, plan.Volumes...),
		Images:   append([]string{}, plan.Images...),
		Warnings: []string{},
		Serves:   Serves(proj),
	}
	if kind, ok := AppKind(proj); ok {
		pv.AppService = string(kind)
	}
	if _, ok := nodeDevConfig(proj); ok {
		pv.DevHostname = DevHostname(proj.Slug, p.paths.BaseDomain)
	}
	for _, c := range plan.Containers {
		pc := PreviewContainer{Service: string(c.Kind), Name: c.Spec.Name, Image: c.Spec.Image, Ports: []string{}, Mounts: []string{}}
		for _, port := range c.Spec.Ports {
			pc.Ports = append(pc.Ports, fmt.Sprintf("%d → %d/%s", port.HostPort, port.ContainerPort, port.Protocol))
		}
		for _, m := range c.Spec.Mounts {
			ro := ""
			if m.ReadOnly {
				ro = " (ro)"
			}
			pc.Mounts = append(pc.Mounts, fmt.Sprintf("%s → %s%s", m.Source, m.Target, ro))
		}
		pv.Containers = append(pv.Containers, pc)
	}
	if pv.Containers == nil {
		pv.Containers = []PreviewContainer{}
	}
	return pv
}

// VolumeName returns the volume name for a project service.
func VolumeName(slug string, kind store.ServiceKind) string {
	return fmt.Sprintf("envoryx-%s-%s", slug, kind)
}

// envStrings builds the environment for application containers: Envoryx defaults, then
// database connection variables, then the user's variables (which override everything).
func (p *Planner) envStrings(proj store.Project) ([]string, error) {
	vars := map[string]string{"ENVORYX_PROJECT": proj.Slug}
	var order []string
	set := func(k, v string) {
		if _, ok := vars[k]; !ok {
			order = append(order, k)
		}
		vars[k] = v
	}
	order = append(order, "ENVORYX_PROJECT")
	// The address the project answers at, for what an application has to know about
	// itself (APP_URL=${ENVORYX_URL} in a .env); it follows a rename with the next plan.
	if u := p.ProjectURL(proj); u != "" {
		set("ENVORYX_URL", u)
	}
	for _, db := range proj.Databases() {
		var cfg runtime.DatabaseConfig
		if err := json.Unmarshal(db.Config, &cfg); err != nil {
			return nil, fmt.Errorf("database config: %w", err)
		}
		dbEnv := databaseEnv(db, cfg)
		// The primary's standard keys first, in the order frameworks document them; the
		// rest (flavour extras such as MONGODB_URI, every key of an additional database)
		// in a stable order.
		name := db.Kind.DatabaseName()
		std := map[string]bool{}
		for _, k := range []string{"DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD", "DATABASE_URL"} {
			k = envKey(name, k)
			std[k] = true
			set(k, dbEnv[k])
		}
		extra := make([]string, 0, len(dbEnv))
		for k := range dbEnv {
			if !std[k] {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		for _, k := range extra {
			set(k, dbEnv[k])
		}
	}
	if r := proj.Service(store.ServiceRedis); r != nil && r.Enabled {
		var cfg runtime.ServiceConfig
		if err := json.Unmarshal(r.Config, &cfg); err != nil {
			return nil, fmt.Errorf("redis config: %w", err)
		}
		env := runtime.RedisEnv(cfg)
		for _, k := range runtime.RedisEnvKeys {
			if v, ok := env[k]; ok {
				set(k, v)
			}
		}
	}
	if mc := proj.Service(store.ServiceMemcached); mc != nil && mc.Enabled {
		env := runtime.MemcachedEnv()
		for _, k := range runtime.MemcachedEnvKeys {
			set(k, env[k])
		}
	}
	if mp := proj.Service(store.ServiceMailpit); mp != nil && mp.Enabled {
		env := runtime.MailpitEnv()
		for _, k := range []string{"MAIL_MAILER", "MAIL_HOST", "MAIL_PORT", "MAIL_ENCRYPTION", "MAILER_DSN", "SMTP_HOST", "SMTP_PORT"} {
			set(k, env[k])
		}
	}
	if rq := proj.Service(store.ServiceRabbitMQ); rq != nil && rq.Enabled {
		var cfg runtime.ServiceConfig
		if err := json.Unmarshal(rq.Config, &cfg); err != nil {
			return nil, fmt.Errorf("rabbitmq config: %w", err)
		}
		env := runtime.RabbitMQEnv(cfg)
		for _, k := range runtime.RabbitMQEnvKeys {
			set(k, env[k])
		}
	}
	for _, search := range []struct {
		kind store.ServiceKind
		env  func(runtime.ServiceConfig) map[string]string
		keys []string
	}{
		{store.ServiceMeilisearch, runtime.MeilisearchEnv, runtime.MeilisearchEnvKeys},
		{store.ServiceTypesense, runtime.TypesenseEnv, runtime.TypesenseEnvKeys},
	} {
		svc := proj.Service(search.kind)
		if svc == nil || !svc.Enabled {
			continue
		}
		var cfg runtime.ServiceConfig
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return nil, fmt.Errorf("%s config: %w", search.kind, err)
		}
		env := search.env(cfg)
		for _, k := range search.keys {
			set(k, env[k])
		}
	}
	if ol := proj.Service(store.ServiceOllama); ol != nil && ol.Enabled {
		env := runtime.OllamaEnv()
		for _, k := range runtime.OllamaEnvKeys {
			set(k, env[k])
		}
	}
	if search := proj.Service(store.ServiceOpenSearch); search != nil && search.Enabled {
		env := runtime.OpenSearchEnv()
		for _, k := range runtime.OpenSearchEnvKeys {
			set(k, env[k])
		}
	}
	if st := proj.Service(store.ServiceStorage); st != nil && st.Enabled {
		var cfg runtime.StorageConfig
		if err := json.Unmarshal(st.Config, &cfg); err != nil {
			return nil, fmt.Errorf("storage config: %w", err)
		}
		env := runtime.StorageEnv(cfg, p.storagePublicURL(proj.Slug, cfg))
		for _, k := range runtime.StorageEnvKeys {
			set(k, env[k])
		}
	}
	for _, e := range proj.Env {
		set(e.Key, e.Value)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out, nil
}

// specFingerprint hashes the parts of a spec that are baked into a container and are not
// secrets: command, working dir, user, mounts, ports, aliases and healthcheck.
func specFingerprint(spec docker.ContainerSpec) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	fields := map[string]any{"cmd": spec.Cmd, "wd": spec.WorkingDir, "user": spec.User, "mounts": spec.Mounts, "ports": spec.Ports, "alias": spec.NetworkAlias, "health": spec.Healthcheck, "restart": spec.RestartPolicy}
	// Labels are fixed at creation, so a changed FolderView3 folder needs a recreate too.
	// Only a set folder counts: containers from before the setting keep their fingerprint.
	if f := spec.Labels[docker.LabelFolderView]; f != "" {
		fields["folder"] = f
	}
	if spec.GPUs {
		fields["gpu"] = true
	}
	if len(spec.ExtraHosts) > 0 {
		fields["hosts"] = spec.ExtraHosts
	}
	_ = enc.Encode(fields)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
