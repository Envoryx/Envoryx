package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/addon"
	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Addons are services described in a YAML file (package addon) instead of Envoryx's code.
// Admins install the files; a project runs an installed addon as the service
// addon-<name>. The service keeps a copy of the definition, so a project still starts
// when the file changes or goes, and installing a new version of the file updates that
// copy in every project (the containers follow at their next start).

// addonsDir is the addons directory under /config.
const addonsDir = "addons"

// AddonConfig is the configuration of an addon service.
type AddonConfig struct {
	// HostPort publishes the addon's port on the host (0: not published).
	HostPort int `json:"hostPort,omitempty"`
	// Secrets are the generated values of the definition's secrets.
	Secrets map[string]string `json:"secrets,omitempty"`
	// Definition is the definition the service runs.
	Definition addon.Definition `json:"definition"`
}

// AddonUpdate adds (Enabled), changes or removes an addon of a project.
type AddonUpdate struct {
	Enabled bool   `json:"enabled"`
	Version string `json:"version,omitempty"`
	// ExposePort publishes the addon's port on the host (addons with publishPort).
	ExposePort bool `json:"exposePort,omitempty"`
	// RemoveData confirms that removing the addon deletes its volumes.
	RemoveData bool `json:"removeData,omitempty"`
}

// AddonInfo describes a project's addon for the UI.
type AddonInfo struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	Version     string   `json:"version"`
	Versions    []string `json:"versions"`
	Image       string   `json:"image"`
	Host        string   `json:"host"`
	Port        int      `json:"port,omitempty"`
	HostPort    int      `json:"hostPort,omitempty"`
	PublishPort bool     `json:"publishPort,omitempty"`
	// URL is the web UI through the proxy ("" without one).
	URL         string            `json:"url,omitempty"`
	InjectedEnv []string          `json:"injectedEnv"`
	Credentials []AddonCredential `json:"credentials"`
	Volumes     []string          `json:"volumes"`
	State       string            `json:"state"`
	Health      string            `json:"health,omitempty"`
	// Installed is false when the addon's file is gone; the project keeps its copy.
	Installed bool `json:"installed"`
}

// AddonCredential is a rendered credential of an addon.
type AddonCredential struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

// AddonHostname is the proxy host name of an addon's web UI.
func AddonHostname(slug, name, base string) string { return slug + "-" + name + "." + base }

// addonVolumeName is the Docker volume of an addon's volume.
func addonVolumeName(slug, name, volume string) string {
	return VolumeName(slug, store.AddonKind(name)) + "-" + volume
}

// addonConfig reads an addon service's configuration.
func addonConfig(svc *store.ProjectService) (AddonConfig, error) {
	var cfg AddonConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("addon %s config: %w", svc.Kind.AddonName(), err)
	}
	if cfg.Definition.Name == "" {
		return cfg, fmt.Errorf("addon %s config has no definition", svc.Kind.AddonName())
	}
	return cfg, nil
}

// Addons returns the addon registry (the files under /config/addons).
func (m *Manager) Addons() *addon.Registry { return m.addons }

func (m *Manager) addonsDir() (string, error) {
	p, err := m.paths()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	return filepath.Join(p.ConfigDir, addonsDir), nil
}

// ---- the plan ------------------------------------------------------------------------

// addonURL is the browser URL of an addon's web UI ("" without a base domain or proxy).
func (p *Planner) addonURL(slug, name string) string {
	if p.paths.BaseDomain == "" {
		return ""
	}
	host := AddonHostname(slug, name, p.paths.BaseDomain)
	switch {
	case p.paths.ProxyHTTPSPort == 443:
		return "https://" + host
	case p.paths.ProxyHTTPSPort > 0:
		return fmt.Sprintf("https://%s:%d", host, p.paths.ProxyHTTPSPort)
	case p.paths.ProxyHTTPPort == 80:
		return "http://" + host
	case p.paths.ProxyHTTPPort > 0:
		return fmt.Sprintf("http://%s:%d", host, p.paths.ProxyHTTPPort)
	}
	return ""
}

// addonVars are the values an addon's templates can use.
func (p *Planner) addonVars(proj store.Project, cfg AddonConfig) map[string]string {
	d := cfg.Definition
	vars := map[string]string{
		"host": d.HostnameOrName(), "project.slug": proj.Slug, "project.name": proj.Name,
		"project.url": p.ProjectURL(proj),
	}
	if d.Port > 0 {
		vars["port"] = strconv.Itoa(d.Port)
	}
	if d.WebUI {
		vars["url"] = p.addonURL(proj.Slug, d.Name)
	}
	for k, v := range cfg.Secrets {
		vars["secret."+k] = v
	}
	if db := proj.Service(store.ServiceDatabase); db != nil && db.Enabled {
		var dbCfg runtime.DatabaseConfig
		if json.Unmarshal(db.Config, &dbCfg) == nil {
			env := databaseEnv(db, dbCfg)
			vars["database.type"] = db.Variant
			vars["database.host"] = env["DB_HOST"]
			vars["database.port"] = env["DB_PORT"]
			vars["database.name"] = env["DB_DATABASE"]
			vars["database.user"] = env["DB_USERNAME"]
			vars["database.password"] = env["DB_PASSWORD"]
		}
	}
	return vars
}

// addonContainer plans an addon's container and returns its volumes.
func (p *Planner) addonContainer(proj store.Project, svc store.ProjectService, network string, labels map[string]string) (ContainerPlan, []string, error) {
	cfg, err := addonConfig(&svc)
	if err != nil {
		return ContainerPlan{}, nil, err
	}
	d := cfg.Definition
	vars := p.addonVars(proj, cfg)
	spec := docker.ContainerSpec{
		Name:          ContainerName(proj.Slug, svc.Kind),
		Image:         svc.Image,
		Labels:        labels,
		Env:           addon.RenderMap(d.Env, vars),
		Network:       network,
		NetworkAlias:  []string{d.HostnameOrName()},
		User:          d.User,
		RestartPolicy: "unless-stopped",
		StopTimeout:   20,
	}
	for _, c := range d.Command {
		spec.Cmd = append(spec.Cmd, addon.Render(c, vars))
	}
	var volumes []string
	for _, v := range d.Volumes {
		name := addonVolumeName(proj.Slug, d.Name, v.Name)
		volumes = append(volumes, name)
		spec.Mounts = append(spec.Mounts, docker.MountSpec{Type: "volume", Source: name, Target: v.Path})
	}
	if h := d.Healthcheck; h != nil {
		interval, timeout, start := h.Durations()
		spec.Healthcheck = &docker.HealthSpec{Test: h.Test, Interval: interval, Timeout: timeout, StartPeriod: start, Retries: h.Retries}
	}
	if cfg.HostPort > 0 && d.Port > 0 {
		spec.Ports = []docker.PortSpec{{HostIP: p.paths.PublishInterface, HostPort: cfg.HostPort, ContainerPort: d.Port, Protocol: "tcp"}}
	}
	return ContainerPlan{Kind: svc.Kind, Order: 9, Spec: spec}, volumes, nil
}

// addonEnv adds what the project's addons hand the application. has reports variables
// Envoryx set already; an addon does not override them (the user's variables still
// override everything).
func (p *Planner) addonEnv(proj store.Project, has func(k string) bool, set func(k, v string)) error {
	for _, svc := range proj.Addons() {
		if !svc.Enabled {
			continue
		}
		cfg, err := addonConfig(svc)
		if err != nil {
			return err
		}
		vars := p.addonVars(proj, cfg)
		for _, kv := range addon.RenderMap(cfg.Definition.Inject, vars) {
			k, v, _ := strings.Cut(kv, "=")
			if !has(k) {
				set(k, v)
			}
		}
	}
	return nil
}

// labelAddonDefinition carries a hash of the addon definitions a container depends on:
// its own for an addon container, the injected variables' for an application container.
// It is part of the spec fingerprint, so a new version of an addon file recreates the
// containers at the next start although environment changes alone do not.
const labelAddonDefinition = "envoryx.addon.definition"

// labelAddonDefinitions sets labelAddonDefinition on the containers that depend on an
// addon definition. Projects without addons keep their fingerprints.
func (p *Planner) labelAddonDefinitions(proj store.Project, containers []ContainerPlan) {
	defs := map[store.ServiceKind]string{}
	inject := map[string]map[string]string{}
	keys := map[string]bool{}
	for _, svc := range proj.Addons() {
		cfg, err := addonConfig(svc)
		if err != nil || !svc.Enabled {
			continue
		}
		defs[svc.Kind] = hashJSON(cfg.Definition)
		if len(cfg.Definition.Inject) > 0 {
			inject[cfg.Definition.Name] = cfg.Definition.Inject
			for k := range cfg.Definition.Inject {
				keys[k] = true
			}
		}
	}
	injectHash := ""
	if len(inject) > 0 {
		injectHash = hashJSON(inject)
	}
	for i := range containers {
		c := &containers[i]
		h, ok := defs[c.Kind]
		if !ok && injectHash != "" && slices.ContainsFunc(c.Spec.Env, func(kv string) bool {
			k, _, _ := strings.Cut(kv, "=")
			return keys[k]
		}) {
			h, ok = injectHash, true
		}
		if ok {
			c.Spec.Labels = maps.Clone(c.Spec.Labels)
			c.Spec.Labels[labelAddonDefinition] = h
		}
	}
}

func hashJSON(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

// ---- adding, changing, removing ----------------------------------------------------------

// newAddonSecrets generates the values of the secrets a definition declares that are not
// there yet.
func newAddonSecrets(d addon.Definition, have map[string]string) (map[string]string, error) {
	out := maps.Clone(have)
	if out == nil {
		out = map[string]string{}
	}
	for _, s := range d.Secrets {
		if out[s] != "" {
			continue
		}
		v, err := runtime.GeneratePassword(24)
		if err != nil {
			return nil, err
		}
		out[s] = v
	}
	return out, nil
}

// applyAddonUpdate adds, changes or removes an addon. Callers hold the lock. It reports
// whether the application's environment changed.
func (m *Manager) applyAddonUpdate(ctx context.Context, p store.Project, name string, upd AddonUpdate, changes map[string]any) (bool, error) {
	if err := addon.ValidName(name); err != nil {
		return false, err
	}
	kind := store.AddonKind(name)
	svc := p.Service(kind)
	key := "addon:" + name
	if !upd.Enabled {
		if svc == nil {
			return false, nil
		}
		cfg, err := addonConfig(svc)
		if err != nil {
			return false, err
		}
		if len(cfg.Definition.Volumes) > 0 && !upd.RemoveData {
			return false, fmt.Errorf("%w: removing %s deletes its data volumes; confirm with removeData", validate.ErrInvalid, cfg.Definition.Title)
		}
		containers, err := m.engine.ListContainers(ctx, true, p.ID)
		if err != nil {
			return false, err
		}
		for _, c := range containers {
			if c.Service() == string(kind) {
				if err := m.engine.RemoveContainer(ctx, c.ID); err != nil {
					return false, fmt.Errorf("remove %s container: %w", name, err)
				}
			}
		}
		for _, v := range cfg.Definition.Volumes {
			if err := m.engine.RemoveVolume(ctx, addonVolumeName(p.Slug, name, v.Name)); err != nil {
				return false, fmt.Errorf("remove %s volume: %w", name, err)
			}
		}
		if err := m.store.Projects.DeleteService(ctx, p.ID, kind); err != nil {
			return false, err
		}
		changes[key] = "removed"
		return len(cfg.Definition.Inject) > 0, nil
	}

	// Adding or changing takes the installed definition; a project whose addon file is
	// gone keeps running its copy but cannot change it.
	d, _, err := m.addons.Get(name)
	if err != nil {
		if errors.Is(err, addon.ErrNotFound) {
			return false, fmt.Errorf("%w: the addon %s is not installed", validate.ErrInvalid, name)
		}
		return false, err
	}
	var cfg AddonConfig
	version := upd.Version
	if svc != nil {
		if cfg, err = addonConfig(svc); err != nil {
			return false, err
		}
		if version == "" {
			version = svc.Version
		}
	}
	v, err := d.Resolve(version)
	if err != nil {
		return false, err
	}
	injectBefore := cfg.Definition.Inject
	cfg.Definition = d
	if cfg.Secrets, err = newAddonSecrets(d, cfg.Secrets); err != nil {
		return false, err
	}
	expose := d.WebUI || (d.PublishPort && upd.ExposePort)
	switch {
	case expose && cfg.HostPort == 0:
		taken := []int{p.HTTPPort}
		port, err := m.allocatePort(ctx, taken...)
		if err != nil {
			return false, err
		}
		cfg.HostPort = port
		changes[key+"HostPort"] = port
	case !expose && cfg.HostPort > 0:
		cfg.HostPort = 0
		changes[key+"HostPort"] = 0
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return false, err
	}
	if svc == nil {
		newSvc := store.ProjectService{ProjectID: p.ID, Kind: kind, Variant: name, Version: v.Version, Image: v.Image, Enabled: true, Config: raw, Position: 40}
		if err := m.store.Projects.AddService(ctx, newSvc); err != nil {
			return false, err
		}
		changes[key] = v.Version
		return len(d.Inject) > 0, nil
	}
	if v.Version != svc.Version {
		changes[key] = v.Version
	}
	if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, kind, v.Version, v.Image, raw); err != nil {
		return false, err
	}
	return !maps.Equal(injectBefore, d.Inject), nil
}

// ---- the definitions ------------------------------------------------------------------

// InstallAddon validates and stores an addon file (a new one or a new version of an
// installed one), then updates the copy every project using it keeps. Their containers
// follow at the next start.
func (m *Manager) InstallAddon(ctx context.Context, raw []byte) (addon.Definition, error) {
	d, err := m.addons.Save(raw)
	if err != nil {
		return addon.Definition{}, err
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return d, err
	}
	kind := store.AddonKind(d.Name)
	var updated []string
	for _, p := range projects {
		svc := p.Service(kind)
		if svc == nil {
			continue
		}
		cfg, err := addonConfig(svc)
		if err != nil {
			return d, err
		}
		v, err := d.Resolve(svc.Version)
		if err != nil {
			// The version is gone from the file: the project moves to the default.
			v, _ = d.Resolve("")
		}
		cfg.Definition = d
		if cfg.Secrets, err = newAddonSecrets(d, cfg.Secrets); err != nil {
			return d, err
		}
		switch {
		case !d.WebUI && !d.PublishPort:
			cfg.HostPort = 0
		case d.WebUI && cfg.HostPort == 0:
			// A web UI the new version adds needs its port (the proxy's way in on bare metal).
			port, err := m.allocatePort(ctx, p.HTTPPort)
			if err != nil {
				return d, err
			}
			cfg.HostPort = port
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return d, err
		}
		if err := m.store.Projects.UpdateServiceConfig(ctx, p.ID, kind, v.Version, v.Image, raw); err != nil {
			return d, err
		}
		updated = append(updated, p.Slug)
	}
	m.audit.Log(ctx, audit.ActionAddonInstalled, "addon", d.Name, map[string]any{"name": d.Name, "title": d.Title, "projects": updated})
	return d, nil
}

// InstallAddonFromURL downloads an addon file and installs it.
func (m *Manager) InstallAddonFromURL(ctx context.Context, url string) (addon.Definition, error) {
	raw, err := m.addons.Fetch(ctx, url)
	if err != nil {
		return addon.Definition{}, err
	}
	return m.InstallAddon(ctx, raw)
}

// DeleteAddon removes an addon file. An addon a project still runs cannot be removed.
func (m *Manager) DeleteAddon(ctx context.Context, name string) error {
	if err := addon.ValidName(name); err != nil {
		return err
	}
	users, err := m.addonUsers(ctx, name)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("%w: %s is used by %s; remove it from those projects first", ErrConflict, name, strings.Join(users, ", "))
	}
	if err := m.addons.Delete(name); err != nil {
		if errors.Is(err, addon.ErrNotFound) {
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		}
		return err
	}
	m.audit.Log(ctx, audit.ActionAddonRemoved, "addon", name, map[string]any{"name": name})
	return nil
}

// addonUsers returns the projects (by name) running an addon.
func (m *Manager) addonUsers(ctx context.Context, name string) ([]string, error) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range projects {
		if p.Service(store.AddonKind(name)) != nil {
			out = append(out, p.Name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// AddonUsage maps every addon to the projects running it.
func (m *Manager) AddonUsage(ctx context.Context) (map[string][]string, error) {
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, p := range projects {
		for _, svc := range p.Addons() {
			out[svc.Kind.AddonName()] = append(out[svc.Kind.AddonName()], p.Name)
		}
	}
	return out, nil
}

// ProjectAddons describes a project's addons.
func (m *Manager) ProjectAddons(ctx context.Context, id string) ([]AddonInfo, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	p := view.Project
	planner, err := m.planner()
	if err != nil {
		return nil, err
	}
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil && !errors.Is(err, docker.ErrUnavailable) {
		return nil, err
	}
	installed := map[string]bool{}
	if list, err := m.addons.List(); err == nil {
		for _, a := range list {
			installed[a.Name] = a.Error == ""
		}
	}
	out := []AddonInfo{}
	for _, svc := range p.Addons() {
		cfg, err := addonConfig(svc)
		if err != nil {
			return nil, err
		}
		d := cfg.Definition
		vars := planner.addonVars(p, cfg)
		info := AddonInfo{
			Name: d.Name, Title: d.Title, Description: d.Description, Homepage: d.Homepage,
			Version: svc.Version, Image: svc.Image, Host: d.HostnameOrName(), Port: d.Port,
			HostPort: cfg.HostPort, PublishPort: d.PublishPort, State: "missing",
			InjectedEnv: slices.Sorted(maps.Keys(d.Inject)), Credentials: []AddonCredential{}, Volumes: []string{},
			Installed: installed[d.Name],
		}
		for _, v := range d.Versions {
			info.Versions = append(info.Versions, v.Version)
		}
		if d.WebUI {
			info.URL = planner.addonURL(p.Slug, d.Name)
		}
		for _, c := range d.Credentials {
			info.Credentials = append(info.Credentials, AddonCredential{Label: c.Label, Value: addon.Render(c.Value, vars), Secret: c.Secret})
		}
		for _, v := range d.Volumes {
			info.Volumes = append(info.Volumes, addonVolumeName(p.Slug, d.Name, v.Name))
		}
		for _, c := range containers {
			if c.Service() == string(svc.Kind) {
				info.State, info.Health = c.State, c.Health
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// ---- backups -----------------------------------------------------------------------------

// hasAddonVolumes reports whether a project runs an addon with volumes to back up.
func hasAddonVolumes(p store.Project) bool {
	for _, svc := range p.Addons() {
		cfg, err := addonConfig(svc)
		if err != nil {
			continue
		}
		for _, v := range cfg.Definition.Volumes {
			if !v.NoBackup {
				return true
			}
		}
	}
	return false
}

// addonBackupFile is the archive of one addon volume in a backup.
func addonBackupFile(name, volume string) string {
	return "addon-" + name + "-" + volume + ".tar.gz"
}

// addonVolumeTool is the small image that reads and writes addon volumes for backups.
const addonVolumeTool = "busybox:1.37"

// backupAddonVolumes archives the project's addon volumes into dir. A running addon
// container stops while its volumes are read, so the archive is consistent, and starts
// again afterwards.
func (m *Manager) backupAddonVolumes(ctx context.Context, p store.Project, dir string) ([]string, error) {
	var files []string
	for _, svc := range p.Addons() {
		cfg, err := addonConfig(svc)
		if err != nil {
			return files, err
		}
		var vols []addon.Volume
		for _, v := range cfg.Definition.Volumes {
			if !v.NoBackup {
				vols = append(vols, v)
			}
		}
		if len(vols) == 0 {
			continue
		}
		if err := m.engine.EnsureImage(ctx, addonVolumeTool, m.pullProgress(ctx, p.Slug, addonVolumeTool)); err != nil {
			return files, err
		}
		restart, err := m.stopAddonContainer(ctx, p, svc.Kind)
		if err != nil {
			return files, err
		}
		for _, v := range vols {
			step(ctx, "Backing up the volume {{name}}", "name", addonVolumeName(p.Slug, cfg.Definition.Name, v.Name))
			file := addonBackupFile(cfg.Definition.Name, v.Name)
			if err := m.archiveVolume(ctx, p, addonVolumeName(p.Slug, cfg.Definition.Name, v.Name), filepath.Join(dir, file)); err != nil {
				restart()
				return files, err
			}
			files = append(files, file)
		}
		restart()
	}
	return files, nil
}

// restoreAddonVolumes restores the addon volumes a backup holds, for the addons the
// project still has. It returns what it restored.
func (m *Manager) restoreAddonVolumes(ctx context.Context, p store.Project, dir string, files []string) ([]string, error) {
	var restored []string
	for _, svc := range p.Addons() {
		cfg, err := addonConfig(svc)
		if err != nil {
			return restored, err
		}
		var todo []addon.Volume
		for _, v := range cfg.Definition.Volumes {
			if slices.Contains(files, addonBackupFile(cfg.Definition.Name, v.Name)) {
				todo = append(todo, v)
			}
		}
		if len(todo) == 0 {
			continue
		}
		if err := m.engine.EnsureImage(ctx, addonVolumeTool, m.pullProgress(ctx, p.Slug, addonVolumeTool)); err != nil {
			return restored, err
		}
		restart, err := m.stopAddonContainer(ctx, p, svc.Kind)
		if err != nil {
			return restored, err
		}
		for _, v := range todo {
			volume := addonVolumeName(p.Slug, cfg.Definition.Name, v.Name)
			step(ctx, "Restoring the volume {{name}}", "name", volume)
			if err := m.engine.CreateVolume(ctx, volume, docker.ManagedLabels(p.ID, p.Slug, "", "")); err != nil && !errors.Is(err, store.ErrConflict) && !strings.Contains(err.Error(), "already exists") {
				restart()
				return restored, err
			}
			if err := m.unarchiveVolume(ctx, p, volume, filepath.Join(dir, addonBackupFile(cfg.Definition.Name, v.Name))); err != nil {
				restart()
				return restored, err
			}
			restored = append(restored, cfg.Definition.Name+"/"+v.Name)
		}
		restart()
	}
	return restored, nil
}

// stopAddonContainer stops a running addon container and returns what starts it again.
func (m *Manager) stopAddonContainer(ctx context.Context, p store.Project, kind store.ServiceKind) (func(), error) {
	containers, err := m.engine.ListContainers(ctx, true, p.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range containers {
		if c.Service() != string(kind) || c.State != "running" {
			continue
		}
		if err := m.engine.StopContainer(ctx, c.ID, 30*time.Second); err != nil {
			return nil, err
		}
		id := c.ID
		return func() {
			if err := m.engine.StartContainer(context.WithoutCancel(ctx), id); err != nil {
				m.log.Warn("starting the addon again failed", "project", p.Slug, "addon", kind.AddonName(), "err", err)
			}
		}, nil
	}
	return func() {}, nil
}

// volumeSpec is a throwaway container of the volume tool with volume at /v.
func volumeSpec(p store.Project, volume string, cmd ...string) docker.ContainerSpec {
	return docker.ContainerSpec{
		Name:          fmt.Sprintf("envoryx-%s-volume-%d", p.Slug, time.Now().UnixNano()%1_000_000),
		Image:         addonVolumeTool,
		Labels:        docker.ManagedLabels(p.ID, p.Slug, "volume", ""),
		Cmd:           cmd,
		Mounts:        []docker.MountSpec{{Type: "volume", Source: volume, Target: "/v"}},
		RestartPolicy: "no",
	}
}

// archiveVolume writes a volume's content as a gzipped tar to target.
func (m *Manager) archiveVolume(ctx context.Context, p store.Project, volume, target string) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	var stderr limitedBuilder
	stderr.b = &strings.Builder{}
	code, err := m.engine.RunOneShotStream(ctx, volumeSpec(p, volume, "tar", "-C", "/v", "-czf", "-", "."), docker.ExecStreamOptions{Stdout: f, Stderr: &stderr})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && code != 0 {
		err = fmt.Errorf("archive the volume %s: tar exited with %d: %s", volume, code, strings.TrimSpace(stderr.b.String()))
	}
	if err != nil {
		_ = os.Remove(target)
	}
	return err
}

// unarchiveVolume empties a volume and unpacks a gzipped tar into it.
func (m *Manager) unarchiveVolume(ctx context.Context, p store.Project, volume, source string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	var stderr limitedBuilder
	stderr.b = &strings.Builder{}
	script := "find /v -mindepth 1 -maxdepth 1 -exec rm -rf {} + && tar -C /v -xzf -"
	code, err := m.engine.RunOneShotStream(ctx, volumeSpec(p, volume, "sh", "-c", script), docker.ExecStreamOptions{Stdin: f, Stderr: &stderr})
	if err == nil && code != 0 {
		err = fmt.Errorf("restore the volume %s: exit %d: %s", volume, code, strings.TrimSpace(stderr.b.String()))
	}
	return err
}
