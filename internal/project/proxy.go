package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/proxy"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Settings keys for the proxy.
const (
	SettingBaseDomain = "base_domain"
	SettingForceHTTPS = "force_https"
	DefaultBaseDomain = "test"
	// UIHostLabel is the host label of Envoryx's own UI under the base domain (envoryx.test).
	UIHostLabel = "envoryx"
)

// BaseDomain returns the configured base domain.
func (m *Manager) BaseDomain(ctx context.Context) string {
	if v, err := m.store.Settings.Get(ctx, SettingBaseDomain); err == nil && v != "" {
		return v
	}
	return DefaultBaseDomain
}

// SettingXdebugClientHost is the settings key of the developer machine used by Xdebug.
const SettingXdebugClientHost = "xdebug_client_host"

// XdebugClientHost returns the global fallback host for Xdebug connections.
func (m *Manager) XdebugClientHost(ctx context.Context) string {
	v, err := m.store.Settings.Get(ctx, SettingXdebugClientHost)
	if err != nil {
		return ""
	}
	return v
}

// SetXdebugClientHost stores the developer machine host/IP ("" clears it).
func (m *Manager) SetXdebugClientHost(ctx context.Context, host string) error {
	host = strings.TrimSpace(host)
	if host != "" {
		if err := validate.Hostname(host); err != nil && net.ParseIP(host) == nil {
			return fmt.Errorf("%w: host name or IP expected", validate.ErrInvalid)
		}
	}
	if err := m.store.Settings.Set(ctx, SettingXdebugClientHost, host); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"xdebugClientHost": host})
	return nil
}

// ForceHTTPS reports whether plain HTTP is redirected to HTTPS.
func (m *Manager) ForceHTTPS(ctx context.Context) bool {
	v, err := m.store.Settings.Get(ctx, SettingForceHTTPS)
	return err == nil && v == "true"
}

// SetBaseDomain validates and stores the base domain.
func (m *Manager) SetBaseDomain(ctx context.Context, base string) error {
	base = validate.NormalizeHostname(base)
	if err := validate.Hostname(base); err != nil {
		return err
	}
	if err := m.store.Settings.Set(ctx, SettingBaseDomain, base); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"baseDomain": base})
	return nil
}

// SetForceHTTPS stores the redirect preference.
func (m *Manager) SetForceHTTPS(ctx context.Context, on bool) error {
	if err := m.store.Settings.Set(ctx, SettingForceHTTPS, strconv.FormatBool(on)); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"forceHttps": on})
	return nil
}

// DefaultHostname is the derived host name of a project.
func DefaultHostname(slug, base string) string { return slug + "." + base }

// UIHostname is the host name of Envoryx's UI under the base domain.
func UIHostname(base string) string { return UIHostLabel + "." + base }

// ProbeHostname is the name the diagnostics use to test wildcard DNS and the proxy from a
// browser; the proxy answers it with a small JSON document.
func ProbeHostname(base string) string { return "envoryx-diagnostics-probe." + base }

// DevHostname is the host name of a project's Node dev server (kept one label deep so a
// wildcard certificate for the base domain covers it).
func DevHostname(slug, base string) string { return slug + "-dev." + base }

// nodeDevConfig returns the dev-server configuration of a project's Node service, if any.
func nodeDevConfig(p store.Project) (runtime.NodeConfig, bool) {
	svc := p.Service(store.ServiceNode)
	if svc == nil || !svc.Enabled || len(svc.Config) == 0 {
		return runtime.NodeConfig{}, false
	}
	var cfg runtime.NodeConfig
	if err := json.Unmarshal(svc.Config, &cfg); err != nil || !cfg.DevServer {
		return runtime.NodeConfig{}, false
	}
	return cfg, true
}

// ProjectHostnames returns the default host name followed by the extra domains.
func (m *Manager) ProjectHostnames(ctx context.Context, p store.Project) ([]string, error) {
	base := m.BaseDomain(ctx)
	hosts := []string{DefaultHostname(p.Slug, base)}
	extra, err := m.store.Domains.ListByProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range extra {
		hosts = append(hosts, d.Hostname)
	}
	return hosts, nil
}

// AddDomain attaches an extra host name to a project. Names derived for other projects
// or reserved for Envoryx are refused.
func (m *Manager) AddDomain(ctx context.Context, id, hostname string) (store.Domain, error) {
	if err := validate.UUID(id); err != nil {
		return store.Domain{}, ErrNotFound
	}
	hostname = validate.NormalizeHostname(hostname)
	if err := validate.Hostname(hostname); err != nil {
		return store.Domain{}, err
	}
	p, err := m.store.Projects.Get(ctx, id)
	if err != nil {
		return store.Domain{}, err
	}
	base := m.BaseDomain(ctx)
	if hostname == UIHostname(base) {
		return store.Domain{}, fmt.Errorf("%w: %s is reserved for Envoryx", validate.ErrInvalid, hostname)
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return store.Domain{}, err
	}
	for _, other := range projects {
		if DefaultHostname(other.Slug, base) == hostname {
			if other.ID == p.ID {
				return store.Domain{}, fmt.Errorf("%w: %s is already the default host name of this project", validate.ErrInvalid, hostname)
			}
			return store.Domain{}, fmt.Errorf("%w: %s is the default host name of project %q", store.ErrConflict, hostname, other.Name)
		}
		if DevHostname(other.Slug, base) == hostname || StorageHostname(other.Slug, base) == hostname {
			return store.Domain{}, fmt.Errorf("%w: %s is reserved for a service of project %q", store.ErrConflict, hostname, other.Name)
		}
	}
	d, err := m.store.Domains.Add(ctx, p.ID, hostname)
	if err != nil {
		return store.Domain{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"domainAdded": hostname}})
	return d, nil
}

// RemoveDomain detaches an extra host name.
func (m *Manager) RemoveDomain(ctx context.Context, id, domainID string) error {
	if err := validate.UUID(id); err != nil {
		return ErrNotFound
	}
	if err := validate.UUID(domainID); err != nil {
		return ErrNotFound
	}
	if err := m.store.Domains.Delete(ctx, id, domainID); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"changes": map[string]any{"domainRemoved": domainID}})
	return nil
}

// ProxyOptions describe the proxy environment for building the routing table.
type ProxyOptions struct {
	// HTTPSPort is the host-side HTTPS port (0 = 443 / unknown).
	HTTPSPort int
	// EnvoryxURL links back to the UI on error pages.
	EnvoryxURL string
	// ExtraUIHosts are additional names served by the UI (public host).
	ExtraUIHosts []string
}

// RouteTable builds the proxy routing table from projects, domains and Docker state. A
// project's primary host name and its extra domains reach the web container, or the
// Python server or Node dev server when it is the project's application (no PHP);
// <slug>-dev.<base> always reaches the Node dev server and <slug>-storage.<base> the
// object storage.
func (m *Manager) RouteTable(ctx context.Context, opts ProxyOptions) (proxy.Table, error) {
	t := proxy.Table{Routes: map[string]proxy.Target{}, UIHosts: map[string]bool{}, ForceHTTPS: m.ForceHTTPS(ctx), HTTPSPort: opts.HTTPSPort, EnvoryxURL: opts.EnvoryxURL}
	base := m.BaseDomain(ctx)
	t.UIHosts[UIHostname(base)] = true
	t.ProbeHost = ProbeHostname(base)
	for _, h := range opts.ExtraUIHosts {
		if h = validate.NormalizeHostname(h); h != "" {
			t.UIHosts[h] = true
		}
	}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return t, err
	}
	domains, err := m.store.Domains.ListAll(ctx)
	if err != nil {
		return t, err
	}
	containers, err := m.engine.ListContainers(ctx, true, "")
	if err != nil {
		return t, err
	}
	running := map[string]map[string]bool{} // service kind → project id → running
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		if running[c.Service()] == nil {
			running[c.Service()] = map[string]bool{}
		}
		running[c.Service()][c.ProjectID()] = true
	}
	nodeRunning, storageRunning := running[string(store.ServiceNode)], running[string(store.ServiceStorage)]
	paths, _ := m.paths()
	byProject := map[string]store.Project{}
	for _, p := range projects {
		byProject[p.ID] = p
		t.Routes[DefaultHostname(p.Slug, base)] = m.appTarget(paths.SelfContainerID, p, running)
		if cfg, ok := nodeDevConfig(p); ok {
			t.Routes[DevHostname(p.Slug, base)] = proxy.Target{ProjectID: p.ID, ProjectName: p.Name + " (dev server)", Slug: p.Slug, Running: nodeRunning[p.ID], Dial: m.dialForDev(paths.SelfContainerID, p, cfg), Rules: proxyRules(p)}
		}
		if host, ok := m.shareHosts.Load(p.ID); ok {
			target := m.appTarget(paths.SelfContainerID, p, running)
			target.Share = true
			t.Routes[host.(string)] = target
		}
		if _, cfg, err := storageConfig(p); err == nil {
			t.Routes[StorageHostname(p.Slug, base)] = proxy.Target{ProjectID: p.ID, ProjectName: p.Name + " (object storage)", Slug: p.Slug, Running: storageRunning[p.ID], Dial: m.storageDial(paths.SelfContainerID, p, cfg)}
		}
	}
	for _, d := range domains {
		if p, ok := byProject[d.ProjectID]; ok {
			t.Routes[d.Hostname] = m.appTarget(paths.SelfContainerID, p, running)
		}
	}
	return t, nil
}

// appTarget is the upstream of a project's primary host name and extra domains: the web
// container, or the Python or Go server or Node dev server when it serves the application.
func (m *Manager) appTarget(selfID string, p store.Project, running map[string]map[string]bool) proxy.Target {
	target := proxy.Target{ProjectID: p.ID, ProjectName: p.Name, Slug: p.Slug, Running: running[string(store.ServiceWeb)][p.ID], Dial: m.dialFor(selfID, p), Rules: proxyRules(p)}
	if cfg, ok := pythonServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServicePython)][p.ID], m.dialForApp(selfID, p, store.ServicePython, cfg.HostPort, cfg.Port)
	} else if cfg, ok := goServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServiceGo)][p.ID], m.dialForApp(selfID, p, store.ServiceGo, cfg.HostPort, cfg.Port)
	} else if cfg, ok := nodeServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServiceNode)][p.ID], m.dialForDev(selfID, p, cfg)
	}
	return target
}

// dialFor returns the upstream address of a project's web server. Inside Docker the proxy
// is attached to the project network and uses the container name; on bare metal the
// published host port is used.
func (m *Manager) dialFor(selfID string, p store.Project) string {
	if selfID == "" {
		if p.HTTPPort == 0 {
			return ""
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.HTTPPort))
	}
	return ContainerName(p.Slug, store.ServiceWeb) + ":80"
}

// dialForDev returns the upstream address of a project's Node dev server.
func (m *Manager) dialForDev(selfID string, p store.Project, cfg runtime.NodeConfig) string {
	return m.dialForApp(selfID, p, store.ServiceNode, cfg.HostPort, cfg.Port)
}

// dialForApp returns the upstream address of an application container that listens
// itself: its container name and port inside Docker, its published host port on bare
// metal.
func (m *Manager) dialForApp(selfID string, p store.Project, kind store.ServiceKind, hostPort, port int) string {
	if selfID == "" {
		if hostPort == 0 {
			return ""
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort))
	}
	return net.JoinHostPort(ContainerName(p.Slug, kind), strconv.Itoa(port))
}

// attachProxy connects Envoryx's own container to a project network so the embedded proxy
// can reach the web container by name. The aliases make the proxy's host names resolve to
// Envoryx on that network, so a project container reaches http://<slug>.<base> without any
// DNS entry and without a detour through the host. Aliases are fixed at connect time: with
// refresh, an attachment whose aliases differ is reconnected (a start, where cutting the
// proxy's open connections to the project does not matter). No-op on bare metal.
func (m *Manager) attachProxy(ctx context.Context, network string, aliases []string, refresh bool) error {
	paths, err := m.paths()
	if err != nil || paths.SelfContainerID == "" {
		return nil
	}
	nets, err := m.engine.ContainerNetworks(ctx, paths.SelfContainerID)
	if err != nil {
		return err
	}
	if slices.Contains(nets, network) {
		if !refresh {
			return nil
		}
		have, err := m.engine.NetworkAliases(ctx, network, paths.SelfContainerID)
		if err != nil {
			return err
		}
		if slices.Equal(sortedCopy(have), sortedCopy(aliases)) {
			return nil
		}
		if err := m.engine.DisconnectNetwork(ctx, network, paths.SelfContainerID); err != nil {
			return fmt.Errorf("detach proxy from %s: %w", network, err)
		}
	}
	if err := m.engine.ConnectNetwork(ctx, network, paths.SelfContainerID, aliases...); err != nil {
		return fmt.Errorf("attach proxy to %s: %w", network, err)
	}
	return nil
}

// proxyAliases lists every host name the proxy routes (all projects, their dev server
// and object storage names, extra domains and the UI), so projects also reach each other.
func (m *Manager) proxyAliases(ctx context.Context) []string {
	t, err := m.RouteTable(ctx, ProxyOptions{})
	if err != nil {
		m.log.Warn("proxy aliases incomplete", "err", err)
	}
	var out []string
	for h, target := range t.Routes {
		if target.Share {
			continue
		}
		out = append(out, h)
	}
	for h := range t.UIHosts {
		out = append(out, h)
	}
	slices.Sort(out)
	return out
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// detachProxy disconnects Envoryx's container from a project network (before removal).
func (m *Manager) detachProxy(ctx context.Context, network string) error {
	paths, err := m.paths()
	if err != nil || paths.SelfContainerID == "" {
		return nil
	}
	if err := m.engine.DisconnectNetwork(ctx, network, paths.SelfContainerID); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

// AttachProxyToAll attaches the proxy to every existing project network (startup and
// reconciliation). Failures are logged, not fatal.
func (m *Manager) AttachProxyToAll(ctx context.Context) {
	paths, err := m.paths()
	if err != nil || paths.SelfContainerID == "" {
		return
	}
	networks, err := m.engine.ListNetworks(ctx, true)
	if err != nil {
		return
	}
	aliases := m.proxyAliases(ctx)
	for _, n := range networks {
		var names []string
		if n.Labels[docker.LabelSystem] == "" {
			names = aliases
		}
		if err := m.attachProxy(ctx, n.Name, names, false); err != nil {
			m.log.Warn("proxy attach failed", "network", n.Name, "err", err)
		}
	}
}
