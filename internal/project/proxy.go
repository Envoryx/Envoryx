package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/seramos/staqio/internal/audit"
	"github.com/seramos/staqio/internal/proxy"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/store"
	"github.com/seramos/staqio/internal/validate"
)

// Settings keys for the proxy.
const (
	SettingBaseDomain = "base_domain"
	SettingForceHTTPS = "force_https"
	DefaultBaseDomain = "test"
	// UIHostLabel is the host label of Staqio's own UI under the base domain (staqio.test).
	UIHostLabel = "staqio"
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

// UIHostname is the host name of Staqio's UI under the base domain.
func UIHostname(base string) string { return UIHostLabel + "." + base }

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
// or reserved for Staqio are refused.
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
		return store.Domain{}, fmt.Errorf("%w: %s is reserved for Staqio", validate.ErrInvalid, hostname)
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
	// StaqioURL links back to the UI on error pages.
	StaqioURL string
	// ExtraUIHosts are additional names served by the UI (public host).
	ExtraUIHosts []string
}

// RouteTable builds the proxy routing table from projects, domains and Docker state.
func (m *Manager) RouteTable(ctx context.Context, opts ProxyOptions) (proxy.Table, error) {
	t := proxy.Table{Routes: map[string]proxy.Target{}, UIHosts: map[string]bool{}, ForceHTTPS: m.ForceHTTPS(ctx), HTTPSPort: opts.HTTPSPort, StaqioURL: opts.StaqioURL}
	base := m.BaseDomain(ctx)
	t.UIHosts[UIHostname(base)] = true
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
	webRunning, nodeRunning := map[string]bool{}, map[string]bool{}
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		switch c.Service() {
		case string(store.ServiceWeb):
			webRunning[c.ProjectID()] = true
		case string(store.ServiceNode):
			nodeRunning[c.ProjectID()] = true
		}
	}
	paths, _ := m.paths()
	byProject := map[string]store.Project{}
	for _, p := range projects {
		byProject[p.ID] = p
		target := proxy.Target{ProjectID: p.ID, ProjectName: p.Name, Slug: p.Slug, Running: webRunning[p.ID], Dial: m.dialFor(paths.SelfContainerID, p)}
		t.Routes[DefaultHostname(p.Slug, base)] = target
		if cfg, ok := nodeDevConfig(p); ok {
			t.Routes[DevHostname(p.Slug, base)] = proxy.Target{ProjectID: p.ID, ProjectName: p.Name + " (dev server)", Slug: p.Slug, Running: nodeRunning[p.ID], Dial: m.dialForDev(paths.SelfContainerID, p, cfg)}
		}
	}
	for _, d := range domains {
		if p, ok := byProject[d.ProjectID]; ok {
			t.Routes[d.Hostname] = proxy.Target{ProjectID: p.ID, ProjectName: p.Name, Slug: p.Slug, Running: webRunning[p.ID], Dial: m.dialFor(paths.SelfContainerID, p)}
		}
	}
	return t, nil
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
	if selfID == "" {
		if cfg.HostPort == 0 {
			return ""
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.HostPort))
	}
	return net.JoinHostPort(ContainerName(p.Slug, store.ServiceNode), strconv.Itoa(cfg.Port))
}

// attachProxy connects Staqio's own container to a project network so the embedded proxy
// can reach the web container by name. No-op on bare metal.
func (m *Manager) attachProxy(ctx context.Context, network string) error {
	paths, err := m.paths()
	if err != nil || paths.SelfContainerID == "" {
		return nil
	}
	nets, err := m.engine.ContainerNetworks(ctx, paths.SelfContainerID)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n == network {
			return nil
		}
	}
	if err := m.engine.ConnectNetwork(ctx, network, paths.SelfContainerID); err != nil {
		return fmt.Errorf("attach proxy to %s: %w", network, err)
	}
	return nil
}

// detachProxy disconnects Staqio's container from a project network (before removal).
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
	for _, n := range networks {
		if err := m.attachProxy(ctx, n.Name); err != nil {
			m.log.Warn("proxy attach failed", "network", n.Name, "err", err)
		}
	}
}
