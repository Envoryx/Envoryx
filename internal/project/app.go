package project

import (
	"fmt"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// A project's "application" is the container that runs its code: the PHP-FPM container
// when the project has PHP, else the Node container. Projects with neither are static
// sites served by the web container alone. Every runtime-dependent decision (routing,
// starter page, one-shot image, SSH user) goes through the helpers in this file so the
// three shapes stay consistent.

// appService returns the project's application container: the enabled PHP service,
// else the enabled Node service, else nil.
func appService(p store.Project) *store.ProjectService {
	for _, kind := range []store.ServiceKind{store.ServicePHP, store.ServiceNode} {
		if svc := p.Service(kind); svc != nil && svc.Enabled {
			return svc
		}
	}
	return nil
}

// AppKind is appService's kind for API/MCP consumers (php, node) and false when none.
func AppKind(p store.Project) (store.ServiceKind, bool) {
	svc := appService(p)
	if svc == nil {
		return "", false
	}
	return svc.Kind, true
}

// nodeServesApp reports whether the Node dev server is the project's application
// (no enabled PHP service and NodeConfig.DevServer). The config is returned normalised.
func nodeServesApp(p store.Project) (runtime.NodeConfig, bool) {
	if php := p.Service(store.ServicePHP); php != nil && php.Enabled {
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

// Serves classifies what the project's primary hostname serves: "php", "node" or "static".
func Serves(p store.Project) string {
	if php := p.Service(store.ServicePHP); php != nil && php.Enabled {
		return "php"
	}
	if _, ok := nodeServesApp(p); ok {
		return "node"
	}
	return "static"
}

// toolImage returns the image for one-shot containers (git, templates): the PHP image,
// else the Node image, else the catalogue's default Node image (git and ssh ship in
// both Envoryx images). Fails only when the catalogue has no Node image.
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
