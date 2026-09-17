package project

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"

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
	env := envStrings(proj)
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
				FilePlan{Path: filepath.Join(plan.ConfigDir, "php", "zz-staqio.ini"), Content: cfg.INI(), Mode: 0o644},
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

		default:
			return Plan{}, fmt.Errorf("service kind %q is not supported yet", svc.Kind)
		}
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

func envStrings(proj store.Project) []string {
	out := make([]string, 0, len(proj.Env)+2)
	out = append(out, "STAQIO_PROJECT="+proj.Slug)
	for _, e := range proj.Env {
		out = append(out, e.Key+"="+e.Value)
	}
	return out
}
