package project

import (
	"encoding/json"

	"github.com/envoryx/envoryx/internal/proxy"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// serviceUI is the web interface of a built-in service, reached through the proxy at
// <slug>-<name>.<base domain> like an addon's. Without it the interface would only be at
// its host port, which a hoster's machine doesn't open to the outside.
type serviceUI struct {
	name, title string
	kind        store.ServiceKind
	// port is the container's, hostPort the published one (the upstream on bare metal).
	port, hostPort int
	// open leaves the address without the Envoryx sign-in: Meilisearch serves its API on
	// the same port, which the application's pages may call with a search key, and it
	// asks for a key itself.
	open bool
}

// serviceUINames are the names serviceUIs uses; their host names are taken for every
// project, whether it has the service or not.
var serviceUINames = []string{"mailpit", "rabbitmq", "opensearch-dashboards", "meilisearch", "s3-console"}

// serviceUIs are the web interfaces of a project's built-in services.
func serviceUIs(p store.Project) []serviceUI {
	var out []serviceUI
	conf := func(kind store.ServiceKind) (runtime.ServiceConfig, bool) {
		svc := p.Service(kind)
		if svc == nil || !svc.Enabled {
			return runtime.ServiceConfig{}, false
		}
		var cfg runtime.ServiceConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		return cfg, !cfg.External()
	}
	if cfg, ok := conf(store.ServiceMailpit); ok {
		out = append(out, serviceUI{name: "mailpit", title: "Mailpit", kind: store.ServiceMailpit, port: 8025, hostPort: cfg.HostPort})
	}
	if cfg, ok := conf(store.ServiceRabbitMQ); ok {
		out = append(out, serviceUI{name: "rabbitmq", title: "RabbitMQ", kind: store.ServiceRabbitMQ, port: runtime.RabbitMQAdminPort, hostPort: cfg.WebUIPort})
	}
	if cfg, ok := conf(store.ServiceOpenSearchDashboards); ok {
		out = append(out, serviceUI{name: "opensearch-dashboards", title: "OpenSearch Dashboards", kind: store.ServiceOpenSearchDashboards, port: runtime.OpenSearchDashboardsPort, hostPort: cfg.HostPort})
	}
	if cfg, ok := conf(store.ServiceMeilisearch); ok {
		out = append(out, serviceUI{name: "meilisearch", title: "Meilisearch", kind: store.ServiceMeilisearch, port: runtime.MeilisearchPort, hostPort: cfg.HostPort, open: true})
	}
	if svc := p.Service(store.ServiceStorage); svc != nil && svc.Enabled {
		var cfg runtime.StorageConfig
		_ = json.Unmarshal(svc.Config, &cfg)
		out = append(out, serviceUI{name: "s3-console", title: "object storage console", kind: store.ServiceStorage, port: runtime.StorageConsolePort, hostPort: cfg.ConsolePort})
	}
	return out
}

// ServiceUIHostname is the host name of a built-in service's web interface.
func ServiceUIHostname(slug, name, base string) string { return slug + "-" + name + "." + base }

// serviceUIRoutes adds the web interfaces of a project's built-in services to the table.
func (m *Manager) serviceUIRoutes(t *proxy.Table, selfID string, p store.Project, base string, running map[string]map[string]bool) {
	for _, ui := range serviceUIs(p) {
		target := proxy.Target{
			ProjectID: p.ID, ProjectName: p.Name + " (" + ui.title + ")", Slug: p.Slug,
			Running: running[string(ui.kind)][p.ID], Dial: m.dialForApp(selfID, p, ui.kind, ui.hostPort, ui.port),
			Rules: accessRules(p), Login: LoginAddon,
		}
		if ui.open {
			target.Login = ""
		}
		t.Routes[ServiceUIHostname(p.Slug, ui.name, base)] = target
	}
}

// serviceUIURL is the browser address of a built-in service's web interface, "" without
// the proxy.
func (p *Planner) serviceUIURL(slug, name string) string {
	if p.paths.BaseDomain == "" {
		return ""
	}
	return p.proxyURL(ServiceUIHostname(slug, name, p.paths.BaseDomain))
}
