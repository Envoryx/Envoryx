package project

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/plan"
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
	changed, err := m.changeSetting(ctx, SettingXdebugClientHost, host)
	if err != nil {
		return err
	}
	if changed {
		m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"xdebugClientHost": host})
	}
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
	changed, err := m.changeSetting(ctx, SettingBaseDomain, base)
	if err != nil {
		return err
	}
	if changed {
		m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"baseDomain": base})
		m.spreadProxyAliases(ctx)
	}
	return nil
}

// SetForceHTTPS stores the redirect preference.
func (m *Manager) SetForceHTTPS(ctx context.Context, on bool) error {
	changed, err := m.changeSetting(ctx, SettingForceHTTPS, strconv.FormatBool(on))
	if err != nil {
		return err
	}
	if changed {
		m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"forceHttps": on})
	}
	return nil
}

// DefaultHostname is the derived host name of a project.
func DefaultHostname(slug, base string) string { return slug + "." + base }

// UIHostname is the host name of Envoryx's UI under the base domain.
func UIHostname(base string) string { return UIHostLabel + "." + base }

// ProbeHostname is the name the diagnostics use to test wildcard DNS and the proxy from a
// browser; the proxy answers it with a small JSON document.
func ProbeHostname(base string) string { return "envoryx-diagnostics-probe." + base }

// probeToken identifies this Envoryx process in the probe answer (see proxy.Table).
var probeToken = rand.Text()

// ProbeToken returns the token the proxy sends in the diagnostics probe answer.
func ProbeToken() string { return probeToken }

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
		reserved := DevHostname(other.Slug, base) == hostname || StorageHostname(other.Slug, base) == hostname
		for _, name := range serviceUINames {
			reserved = reserved || ServiceUIHostname(other.Slug, name, base) == hostname
		}
		if reserved {
			return store.Domain{}, fmt.Errorf("%w: %s is reserved for a service of project %q", store.ErrConflict, hostname, other.Name)
		}
	}
	d, err := m.store.Domains.Add(ctx, p.ID, hostname)
	if err != nil {
		return store.Domain{}, err
	}
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", id, map[string]any{"name": p.Name, "changes": map[string]any{"domainAdded": hostname}})
	m.spreadProxyAliases(ctx)
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
	// HTTPSPort is the host-side HTTPS port; 0 means 443 or unknown.
	HTTPSPort int
	// EnvoryxURL links back to the UI on error pages.
	EnvoryxURL string
	// ExtraUIHosts are additional names served by the UI (public host).
	ExtraUIHosts []string
}

// RouteTable builds the proxy routing table from projects, domains and Docker state. A
// project's primary host name and its extra domains reach the web container, or the
// Python, Go, Ruby, Java or .NET server or Node dev server when it is the project's
// application; <slug>-dev.<base> always reaches the Node dev server and
// <slug>-storage.<base> the object storage.
func (m *Manager) RouteTable(ctx context.Context, opts ProxyOptions) (proxy.Table, error) {
	t := proxy.Table{Routes: map[string]proxy.Target{}, UIHosts: map[string]bool{}, Redirects: map[string]string{}, ForceHTTPS: m.ForceHTTPS(ctx), HTTPSPort: opts.HTTPSPort, EnvoryxURL: opts.EnvoryxURL}
	base := m.BaseDomain(ctx)
	t.BaseDomain = base
	t.UIHosts[UIHostname(base)] = true
	t.ProbeHost, t.ProbeToken = ProbeHostname(base), probeToken
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
		t.Routes[DefaultHostname(p.Slug, base)] = m.appRoute(paths.SelfContainerID, p, running, base)
		if cfg, ok := nodeDevConfig(p); ok {
			t.Routes[DevHostname(p.Slug, base)] = withLogin(proxy.Target{ProjectID: p.ID, ProjectName: p.Name + " (dev server)", Slug: p.Slug, Running: nodeRunning[p.ID], Started: p.DesiredState == store.DesiredRunning, Dial: m.dialForDev(paths.SelfContainerID, p, cfg), Rules: proxyRules(p)}, p, m.signsIn(p))
		}
		// A share is the deliberate exception from the sign-in: its address is random and
		// ends with the share, and the basic authentication still applies there.
		if host, ok := m.shareHosts.Load(p.ID); ok && m.Plan().Allows(plan.FeaturePublicShares) {
			target := m.appTarget(paths.SelfContainerID, p, running)
			target.Share = true
			t.Routes[host.(string)] = target
		}
		if _, cfg, err := storageConfig(p); err == nil {
			t.Routes[StorageHostname(p.Slug, base)] = proxy.Target{ProjectID: p.ID, ProjectName: p.Name + " (object storage)", Slug: p.Slug, Running: storageRunning[p.ID], Dial: m.storageDial(paths.SelfContainerID, p, cfg)}
		}
		m.serviceUIRoutes(&t, paths.SelfContainerID, p, base, running)
		for _, svc := range p.Addons() {
			cfg, err := addonConfig(svc)
			if err != nil || !cfg.Definition.WebUI || !svc.Enabled {
				continue
			}
			target := proxy.Target{
				ProjectID: p.ID, ProjectName: p.Name + " (" + cfg.Definition.Title + ")", Slug: p.Slug,
				Running: running[string(svc.Kind)][p.ID], Dial: m.dialForApp(paths.SelfContainerID, p, svc.Kind, cfg.HostPort, cfg.Definition.Port),
				Rules: accessRules(p), Login: LoginAddon,
			}
			if cfg.Definition.Public {
				target.Rules, target.Login = publicAddonRules(p), ""
			}
			t.Routes[AddonHostname(p.Slug, cfg.Definition.Name, base)] = target
		}
	}
	for _, d := range domains {
		if p, ok := byProject[d.ProjectID]; ok {
			t.Routes[d.Hostname] = m.appRoute(paths.SelfContainerID, p, running, base)
		}
	}
	// The bare base domain leads to the UI, unless a project took it as its domain or the UI
	// itself answers there. A single label like "test" never resolves, so it's left out.
	if strings.Contains(base, ".") && !t.UIHosts[base] {
		if _, taken := t.Routes[base]; !taken {
			t.Redirects[base] = UIHostname(base)
		}
	}
	return t, nil
}

// appRoute is the target of a project's primary host name and extra domains: the
// application, behind the Envoryx sign-in when the project asks for it. The dev server's
// host gets a pass at the same sign-in, since the pages load their scripts from it.
func (m *Manager) appRoute(selfID string, p store.Project, running map[string]map[string]bool, base string) proxy.Target {
	t := withLogin(m.appTarget(selfID, p, running), p, m.signsIn(p))
	if _, ok := nodeDevConfig(p); ok && t.Login != "" {
		t.LoginAlso = []string{DevHostname(p.Slug, base)}
	}
	return t
}

// appTarget is the upstream of a project's primary host name and extra domains: the web
// container, or the Python, Go, Ruby, Java or .NET server or Node dev server when it
// serves the application.
func (m *Manager) appTarget(selfID string, p store.Project, running map[string]map[string]bool) proxy.Target {
	target := proxy.Target{ProjectID: p.ID, ProjectName: p.Name, Slug: p.Slug, Running: running[string(store.ServiceWeb)][p.ID], Started: p.DesiredState == store.DesiredRunning, Dial: m.dialFor(selfID, p), Rules: proxyRules(p)}
	if cfg, ok := pythonServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServicePython)][p.ID], m.dialForApp(selfID, p, store.ServicePython, cfg.HostPort, cfg.Port)
	} else if cfg, ok := goServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServiceGo)][p.ID], m.dialForApp(selfID, p, store.ServiceGo, cfg.HostPort, cfg.Port)
	} else if cfg, ok := rubyServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServiceRuby)][p.ID], m.dialForApp(selfID, p, store.ServiceRuby, cfg.HostPort, cfg.Port)
	} else if cfg, ok := javaServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServiceJava)][p.ID], m.dialForApp(selfID, p, store.ServiceJava, cfg.HostPort, cfg.Port)
	} else if cfg, ok := dotnetServesApp(p); ok {
		target.Running, target.Dial = running[string(store.ServiceDotnet)][p.ID], m.dialForApp(selfID, p, store.ServiceDotnet, cfg.HostPort, cfg.Port)
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

// aliasMode says what attachProxy does with an attachment that already exists.
type aliasMode int

const (
	// aliasesKeep leaves an existing attachment as it is.
	aliasesKeep aliasMode = iota
	// aliasesExact reconnects an attachment whose aliases differ (a start, where cutting
	// the proxy's open connections to the project does not matter).
	aliasesExact
	// aliasesAdd reconnects an attachment that lacks one of the aliases (a project or a
	// domain was added while the project ran). Names that went away stay until the next
	// start: they only lead to the proxy, which no longer routes them.
	aliasesAdd
)

// attachProxy connects Envoryx's own container to a project network so the embedded proxy
// can reach the web container by name. The aliases make the proxy's host names resolve to
// Envoryx on that network, so a project container reaches http://<slug>.<base> without any
// DNS entry and without a detour through the host. Docker fixes aliases at connect time,
// so mode decides when an existing attachment is reconnected to change them; that cuts
// the proxy's open connections into the project for a moment. No-op on bare metal.
func (m *Manager) attachProxy(ctx context.Context, network string, aliases []string, mode aliasMode) error {
	paths, err := m.paths()
	if err != nil || paths.SelfContainerID == "" {
		return nil
	}
	// A start and the alias refresh may both reconnect the same network.
	m.proxyAttachMu.Lock()
	defer m.proxyAttachMu.Unlock()
	nets, err := m.engine.ContainerNetworks(ctx, paths.SelfContainerID)
	if err != nil {
		return err
	}
	if slices.Contains(nets, network) {
		if mode == aliasesKeep {
			return nil
		}
		have, err := m.engine.NetworkAliases(ctx, network, paths.SelfContainerID)
		if err != nil {
			return err
		}
		switch mode {
		case aliasesExact:
			if slices.Equal(sortedCopy(have), sortedCopy(aliases)) {
				return nil
			}
		case aliasesAdd:
			if !slices.ContainsFunc(aliases, func(a string) bool { return !slices.Contains(have, a) }) {
				return nil
			}
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

// spreadProxyAliases hands host names that were just added (a new project, a domain) to
// the projects already running, so they resolve there right away and not only after the
// next reconciliation.
func (m *Manager) spreadProxyAliases(ctx context.Context) {
	m.AttachProxyToAll(context.WithoutCancel(ctx))
}

// detachProxy disconnects Envoryx's container from a project network (before removal).
func (m *Manager) detachProxy(ctx context.Context, network string) error {
	paths, err := m.paths()
	if err != nil || paths.SelfContainerID == "" {
		return nil
	}
	m.proxyAttachMu.Lock()
	defer m.proxyAttachMu.Unlock()
	if err := m.engine.DisconnectNetwork(ctx, network, paths.SelfContainerID); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

// AttachProxyToAll attaches the proxy to every existing project network (startup and
// reconciliation) and gives the running attachments the host names added since they
// were made, so a project reaches a project or domain created after its start. Failures
// are logged, not fatal.
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
	known, err := m.knownProjects(ctx)
	if err != nil {
		return
	}
	for _, n := range networks {
		if unclaimed(n.Labels, known) {
			continue // perhaps an older instance's next door
		}
		var names []string
		mode := aliasesKeep
		if n.Labels[docker.LabelSystem] == "" {
			names, mode = aliases, aliasesAdd
		}
		if err := m.attachProxy(ctx, n.Name, names, mode); err != nil {
			m.log.Warn("proxy attach failed", "network", n.Name, "err", err)
		}
	}
}
