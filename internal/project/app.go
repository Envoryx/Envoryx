package project

import (
	"encoding/json"
	"fmt"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// A project's "application" is the container that runs its code: the PHP-FPM container
// when the project has PHP, else the Python container, else the Node container. Projects
// with none are static sites served by the web container alone. Every runtime-dependent
// decision (routing, starter page, one-shot image, SSH user) goes through the helpers in
// this file so the shapes stay consistent.

// appService returns the project's application container: the enabled PHP service,
// else the enabled Python service, else the enabled Node service, else nil.
func appService(p store.Project) *store.ProjectService {
	for _, kind := range []store.ServiceKind{store.ServicePHP, store.ServicePython, store.ServiceNode} {
		if svc := p.Service(kind); svc != nil && svc.Enabled {
			return svc
		}
	}
	return nil
}

// AppKind is appService's kind for API/MCP consumers (php, python, node) and false when
// none.
func AppKind(p store.Project) (store.ServiceKind, bool) {
	svc := appService(p)
	if svc == nil {
		return "", false
	}
	return svc.Kind, true
}

// hasPHP reports whether the project has an enabled PHP service.
func hasPHP(p store.Project) bool {
	php := p.Service(store.ServicePHP)
	return php != nil && php.Enabled
}

// pythonConfig returns the configuration of a project's enabled Python service, if any.
func pythonConfig(p store.Project) (runtime.PythonConfig, bool) {
	svc := p.Service(store.ServicePython)
	if svc == nil || !svc.Enabled {
		return runtime.PythonConfig{}, false
	}
	var cfg runtime.PythonConfig
	if len(svc.Config) > 0 {
		if err := json.Unmarshal(svc.Config, &cfg); err != nil {
			return runtime.PythonConfig{}, false
		}
	}
	return cfg, true
}

// pythonServesApp reports whether the Python server is the project's application (no
// enabled PHP service and PythonConfig.Server). The config is returned normalised.
func pythonServesApp(p store.Project) (runtime.PythonConfig, bool) {
	if hasPHP(p) {
		return runtime.PythonConfig{}, false
	}
	cfg, ok := pythonConfig(p)
	if !ok || !cfg.Server {
		return runtime.PythonConfig{}, false
	}
	if err := cfg.Normalize(); err != nil {
		return runtime.PythonConfig{}, false
	}
	return cfg, true
}

// nodeServesApp reports whether the Node dev server is the project's application (no
// enabled PHP service, no Python server, NodeConfig.DevServer). The config is returned
// normalised. With a Python server the Node dev server is the frontend toolchain: it keeps
// its <slug>-dev.<base> route and host port, the project URL reaches Python.
func nodeServesApp(p store.Project) (runtime.NodeConfig, bool) {
	if hasPHP(p) {
		return runtime.NodeConfig{}, false
	}
	if _, ok := pythonServesApp(p); ok {
		return runtime.NodeConfig{}, false
	}
	cfg, ok := nodeDevConfig(p)
	if !ok {
		return runtime.NodeConfig{}, false
	}
	if err := cfg.Normalize(); err != nil {
		return runtime.NodeConfig{}, false
	}
	return cfg, true
}

// appServesDirectly reports whether an application container answers the project URL
// itself (Python server or Node dev server) – the web container's port then stays
// unpublished so the docroot (often the project root with .env and sources) is not
// exposed on the LAN.
func appServesDirectly(p store.Project) bool {
	if _, ok := pythonServesApp(p); ok {
		return true
	}
	_, ok := nodeServesApp(p)
	return ok
}

// Serves classifies what the project's primary hostname serves: "php", "python", "node"
// or "static".
func Serves(p store.Project) string {
	if hasPHP(p) {
		return "php"
	}
	if _, ok := pythonServesApp(p); ok {
		return "python"
	}
	if _, ok := nodeServesApp(p); ok {
		return "node"
	}
	return "static"
}

// toolImage returns the image for one-shot containers (git, templates): the application
// container's image (PHP, Python or Node – git and ssh ship in every Envoryx image), else
// the catalogue's default Node image. Fails only when the catalogue has no Node image.
func (m *Manager) toolImage(p store.Project) (string, error) {
	if svc := appService(p); svc != nil && svc.Image != "" {
		return svc.Image, nil
	}
	v, err := m.catalog.Resolve("node", "")
	if err != nil {
		return "", fmt.Errorf("no runtime image for one-shot commands: %w", err)
	}
	return v.Image, nil
}
