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

	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/store"
)

// Container mount targets shared by all project containers.
const (
	appMountTarget = "/var/www/html"
	phpIniTarget   = "/usr/local/etc/php/conf.d/zz-staqio.ini"
	phpPoolTarget  = "/usr/local/etc/php-fpm.d/zz-staqio.conf"
	caddyfileTgt   = "/etc/caddy/Caddyfile"
	stopTimeoutSec = 10
)

// Paths tells the planner where things live inside the Staqio container and on the host.
type Paths struct {
	ConfigDir        string // e.g. /config
	ConfigHostDir    string // e.g. /mnt/user/appdata/staqio
	ProjectsDir      string // e.g. /projects
	ProjectsHostDir  string // e.g. /mnt/user/development
	PUID, PGID       int
	StaqioVersion    string
	PublishInterface string // host IP to bind ports to; "" = all
	// SelfContainerID is Staqio's own container id ("" when running on bare metal). The
	// embedded proxy joins project networks through it.
	SelfContainerID string
	// BaseDomain is the proxy base domain (for dev-server host allow-lists).
	BaseDomain string
	// XdebugClientHost is the global fallback debugger host (developer machine).
	XdebugClientHost string
}

// FilePlan is a generated configuration file.
type FilePlan struct {
	Path    string // absolute path inside the Staqio container
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
	ConfigDir   string // per-project config dir inside the Staqio container
	// Dirs are directories created (owned by PUID:PGID) before containers start.
	Dirs []DirPlan
}

// DirPlan is a directory Staqio creates for a project (e.g. the tool home).
type DirPlan struct {
	Path     string
	UID, GID int
}

const (
	// homeMountTarget is the writable home of the project user inside php/node/worker
	// containers: tool caches (composer, npm) and IDE helpers persist there.
	homeMountTarget = "/home/staqio"
	homeDirName     = "home"
)

// toolEnv are the variables that point tools at the persistent home.
var toolEnv = []string{"HOME=" + homeMountTarget, "COMPOSER_HOME=" + homeMountTarget + "/.composer", "npm_config_cache=" + homeMountTarget + "/.npm", "COMPOSER_NO_INTERACTION=1"}

// HomeMount is the bind mount of the project home for application containers.
func (p *Planner) HomeMount(proj store.Project) docker.MountSpec {
	return docker.MountSpec{Type: "bind", Source: filepath.Join(p.configHostDir(proj.ID), homeDirName), Target: homeMountTarget}
}

// HomeDir is the project home on the Staqio side (SFTP root for /home/staqio).
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
func NetworkName(slug string) string { return "staqio-" + slug }

// ContainerName returns the container name for a project service.
func ContainerName(slug string, kind store.ServiceKind) string {
	return fmt.Sprintf("staqio-%s-%s", slug, kind)
}

// ProjectConfigDir returns the per-project config directory inside the Staqio container.
func (p *Planner) ProjectConfigDir(projectID string) string {
	return filepath.Join(p.paths.ConfigDir, "projects", projectID)
}

// ProjectDir returns the project directory inside the Staqio container.
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
		Labels:      docker.ManagedLabels(proj.ID, proj.Slug, "", p.paths.StaqioVersion),
		ConfigDir:   p.ProjectConfigDir(proj.ID),
	}
	appHost := p.projectHostDir(proj)
	cfgHost := p.configHostDir(proj.ID)
	plan.Dirs = append(plan.Dirs, DirPlan{Path: p.HomeDir(proj), UID: p.paths.PUID, GID: p.paths.PGID})
	env, err := envStrings(proj)
	if err != nil {
		return Plan{}, err
	}
	images := map[string]bool{}

	for _, svc := range proj.Services {
		if !svc.Enabled {
			continue
		}
		labels := docker.ManagedLabels(proj.ID, proj.Slug, string(svc.Kind), p.paths.StaqioVersion)
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
				FilePlan{Path: filepath.Join(plan.ConfigDir, "php", "zz-staqio.ini"), Content: cfg.INIWith(svc.Version, runtime.INIOptions{XdebugClientHost: p.paths.XdebugClientHost}), Mode: 0o644},
				FilePlan{Path: filepath.Join(plan.ConfigDir, "php", "zz-staqio.conf"), Content: runtime.FPMPool(p.paths.PUID, p.paths.PGID), Mode: 0o644},
			)
			plan.Containers = append(plan.Containers, ContainerPlan{
				Kind:  store.ServicePHP,
				Order: 10,
				Spec: docker.ContainerSpec{
					Name:         ContainerName(proj.Slug, store.ServicePHP),
					Image:        svc.Image,
					Labels:       labels,
					Env:          env,
					WorkingDir:   appMountTarget,
					Network:      plan.NetworkName,
					NetworkAlias: []string{"php"},
					Mounts: []docker.MountSpec{
						{Type: "bind", Source: appHost, Target: appMountTarget},
						{Type: "bind", Source: filepath.Join(cfgHost, "php", "zz-staqio.ini"), Target: phpIniTarget, ReadOnly: true},
						{Type: "bind", Source: filepath.Join(cfgHost, "php", "zz-staqio.conf"), Target: phpPoolTarget, ReadOnly: true},
						p.HomeMount(proj),
					},
					RestartPolicy: "unless-stopped",
					StopTimeout:   stopTimeoutSec,
				},
			})
			images[svc.Image] = true

		case store.ServiceWeb:
			plan.Files = append(plan.Files,
				FilePlan{Path: filepath.Join(plan.ConfigDir, "web", "Caddyfile"), Content: runtime.Caddyfile(proj.Docroot), Mode: 0o644},
			)
			spec := docker.ContainerSpec{
				Name:         ContainerName(proj.Slug, store.ServiceWeb),
				Image:        svc.Image,
				Labels:       labels,
				Network:      plan.NetworkName,
				NetworkAlias: []string{"web"},
				Mounts: []docker.MountSpec{
					{Type: "bind", Source: appHost, Target: appMountTarget, ReadOnly: true},
					{Type: "bind", Source: filepath.Join(cfgHost, "web", "Caddyfile"), Target: caddyfileTgt, ReadOnly: true},
				},
				RestartPolicy: "unless-stopped",
				StopTimeout:   stopTimeoutSec,
			}
			if proj.HTTPPort > 0 {
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
				Mounts:        []docker.MountSpec{{Type: "bind", Source: appHost, Target: appMountTarget}, p.HomeMount(proj)},
				RestartPolicy: "unless-stopped",
				StopTimeout:   5,
			}
			if ncfg.DevServer {
				// Dev-server mode: the script is the main process; the proxy routes
				// <slug>-dev.<base> to it and the host port publishes it directly.
				spec.Cmd = ncfg.Command()
				spec.Env = append(spec.Env, ncfg.Env(DevHostname(proj.Slug, p.paths.BaseDomain))...)
				if ncfg.HostPort > 0 {
					spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: ncfg.HostPort, ContainerPort: ncfg.Port, Protocol: "tcp"}}
				}
			}
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceNode, Order: 15, Spec: spec})
			images[svc.Image] = true

		case store.ServiceDatabase:
			dialect, ok := runtime.DialectFor(svc.Variant)
			if !ok {
				return Plan{}, fmt.Errorf("database variant %q is not supported", svc.Variant)
			}
			var cfg runtime.DatabaseConfig
			if err := json.Unmarshal(svc.Config, &cfg); err != nil {
				return Plan{}, fmt.Errorf("database config: %w", err)
			}
			if cfg.Password == "" || cfg.Database == "" || cfg.Username == "" || (dialect.HasRoot && cfg.RootPassword == "") {
				return Plan{}, fmt.Errorf("database config for %s is incomplete", proj.Slug)
			}
			volume := VolumeName(proj.Slug, store.ServiceDatabase)
			plan.Volumes = append(plan.Volumes, volume)
			spec := docker.ContainerSpec{
				Name:          ContainerName(proj.Slug, store.ServiceDatabase),
				Image:         svc.Image,
				Labels:        labels,
				Env:           dialect.ContainerEnv(cfg),
				Cmd:           dialect.Cmd,
				Network:       plan.NetworkName,
				NetworkAlias:  []string{"database", svc.Variant},
				Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: dialect.DataDir}},
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
			plan.Containers = append(plan.Containers, ContainerPlan{Kind: store.ServiceDatabase, Order: 5, Spec: spec})
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

		default:
			return Plan{}, fmt.Errorf("service kind %q is not supported yet", svc.Kind)
		}
	}

	// Workers: one container per definition from the PHP image, sharing env, ini and mounts.
	if php := proj.Service(store.ServicePHP); php != nil && php.Enabled {
		for _, w := range proj.Workers {
			if !w.Enabled {
				continue
			}
			cmd, err := WorkerCommand(w)
			if err != nil {
				return Plan{}, err
			}
			plan.Containers = append(plan.Containers, ContainerPlan{
				Kind:  WorkerKind(w),
				Order: 30,
				Spec: docker.ContainerSpec{
					Name:         WorkerContainerName(proj.Slug, w),
					Image:        php.Image,
					Labels:       docker.ManagedLabels(proj.ID, proj.Slug, string(WorkerKind(w)), p.paths.StaqioVersion),
					Cmd:          cmd,
					Env:          append(append([]string{}, env...), "HOME=/tmp", "COMPOSER_HOME=/tmp/composer"),
					User:         fmt.Sprintf("%d:%d", p.paths.PUID, p.paths.PGID),
					WorkingDir:   appMountTarget,
					Network:      plan.NetworkName,
					NetworkAlias: []string{"worker-" + w.Name},
					Mounts: []docker.MountSpec{
						{Type: "bind", Source: appHost, Target: appMountTarget},
						{Type: "bind", Source: filepath.Join(cfgHost, "php", "zz-staqio.ini"), Target: phpIniTarget, ReadOnly: true},
					},
					RestartPolicy: "unless-stopped",
					StopTimeout:   30,
				},
			})
		}
	}

	// Fingerprint the structural part of every spec so ensurePlan can recreate containers
	// whose command, mounts or ports changed (env is handled explicitly by callers).
	for i := range plan.Containers {
		plan.Containers[i].Spec.Labels[docker.LabelSpec] = specFingerprint(plan.Containers[i].Spec)
	}
	sort.SliceStable(plan.Containers, func(i, j int) bool { return plan.Containers[i].Order < plan.Containers[j].Order })
	for img := range images {
		plan.Images = append(plan.Images, img)
	}
	sort.Strings(plan.Images)
	return plan, nil
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
	return fmt.Sprintf("staqio-%s-%s", slug, kind)
}

// envStrings builds the environment for application containers: Staqio defaults, then
// database connection variables, then the user's variables (which override everything).
func envStrings(proj store.Project) ([]string, error) {
	vars := map[string]string{"STAQIO_PROJECT": proj.Slug}
	var order []string
	set := func(k, v string) {
		if _, ok := vars[k]; !ok {
			order = append(order, k)
		}
		vars[k] = v
	}
	order = append(order, "STAQIO_PROJECT")
	if db := proj.Service(store.ServiceDatabase); db != nil && db.Enabled {
		var cfg runtime.DatabaseConfig
		if err := json.Unmarshal(db.Config, &cfg); err != nil {
			return nil, fmt.Errorf("database config: %w", err)
		}
		dbEnv := runtime.DatabaseEnv(cfg, db.Variant)
		for _, k := range []string{"DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD", "DATABASE_URL"} {
			set(k, dbEnv[k])
		}
		// Flavour-specific extras (e.g. MONGODB_URI) in a stable order.
		extra := make([]string, 0, len(dbEnv))
		for k := range dbEnv {
			if _, std := map[string]bool{"DB_CONNECTION": true, "DB_HOST": true, "DB_PORT": true, "DB_DATABASE": true, "DB_USERNAME": true, "DB_PASSWORD": true, "DATABASE_URL": true}[k]; !std {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		for _, k := range extra {
			set(k, dbEnv[k])
		}
	}
	if r := proj.Service(store.ServiceRedis); r != nil && r.Enabled {
		env := runtime.RedisEnv()
		for _, k := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_URL"} {
			set(k, env[k])
		}
	}
	if mp := proj.Service(store.ServiceMailpit); mp != nil && mp.Enabled {
		env := runtime.MailpitEnv()
		for _, k := range []string{"MAIL_MAILER", "MAIL_HOST", "MAIL_PORT", "MAIL_ENCRYPTION", "MAILER_DSN"} {
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
	_ = enc.Encode(map[string]any{"cmd": spec.Cmd, "wd": spec.WorkingDir, "user": spec.User, "mounts": spec.Mounts, "ports": spec.Ports, "alias": spec.NetworkAlias, "health": spec.Healthcheck, "restart": spec.RestartPolicy})
	return hex.EncodeToString(h.Sum(nil))[:16]
}
