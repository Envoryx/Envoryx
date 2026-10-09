package project

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// packageJSON is the part of a package.json Envoryx looks at.
type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func readPackageJSON(dir string) (packageJSON, bool) {
	raw, err := readProjectFile(dir, "package.json", maxProjectFile)
	if err != nil {
		return packageJSON{}, false
	}
	var pkg packageJSON
	if json.Unmarshal(raw, &pkg) != nil {
		return packageJSON{}, false
	}
	return pkg, true
}

func (p packageJSON) has(dep string) bool {
	_, a := p.Dependencies[dep]
	_, b := p.DevDependencies[dep]
	return a || b
}

// nodeLockfiles maps each lockfile to the install the dev server runs from it
// (runtime.NodeDepsGuard), in the guard's order.
var nodeLockfiles = []struct{ file, install string }{
	{"package-lock.json", "npm ci"},
	{"pnpm-lock.yaml", "pnpm install --frozen-lockfile"},
	{"yarn.lock", "yarn install --frozen-lockfile"},
}

// nodeModulesWarning reports a dev server that waits for its dependencies: package.json
// lists some and nothing has installed them (runtime.NodeDepsGuard).
func (m *Manager) nodeModulesWarning(p store.Project) string {
	if _, ok := nodeDevConfig(p); !ok {
		return ""
	}
	planner, err := m.planner()
	if err != nil {
		return ""
	}
	dir := planner.ProjectDir(p)
	if exists(filepath.Join(dir, "node_modules")) || exists(filepath.Join(dir, ".pnp.cjs")) {
		return ""
	}
	pkg, ok := readPackageJSON(dir)
	if !ok || len(pkg.Dependencies)+len(pkg.DevDependencies) == 0 {
		return ""
	}
	for _, l := range nodeLockfiles {
		if exists(filepath.Join(dir, l.file)) {
			return fmt.Sprintf(`node_modules is missing - the dev server runs "%s" before it starts; if that fails, it waits until you run "npm install" (or pnpm / yarn install) from Actions`, l.install)
		}
	}
	return `node_modules is missing and there is no lockfile - the dev server waits until you run "npm install" (or pnpm / yarn install) from Actions`
}

// fitNodeToRepo matches the dev server configuration to a freshly cloned repository: the
// wizard picks the Vite preset and the "dev" script before it can see the repository, and
// a script package.json doesn't have only crash-loops ("Missing script"). A script the
// repository has is kept, whatever the preset. Otherwise the preset follows the framework
// in the dependencies and the script becomes the first of dev, start (production mode:
// start, preview) that exists; a port still on the old preset's default moves to the new
// one's. ok is false when nothing changes or package.json offers nothing better.
func fitNodeToRepo(dir string, cfg runtime.NodeConfig) (runtime.NodeConfig, bool) {
	pkg, ok := readPackageJSON(dir)
	if !ok {
		return cfg, false
	}
	if _, ok := pkg.Scripts[cfg.Script]; ok {
		return cfg, false
	}
	candidates := []string{"dev", "start"}
	if cfg.Production() {
		candidates = []string{"start", "preview"}
	}
	script := ""
	for _, s := range candidates {
		if _, ok := pkg.Scripts[s]; ok {
			script = s
			break
		}
	}
	if script == "" {
		return cfg, false
	}
	preset := "generic"
	switch {
	case pkg.has("next"):
		preset = "next"
	case pkg.has("nuxt"):
		preset = "nuxt"
	case pkg.has("vite"):
		preset = "vite"
	}
	out := cfg
	out.Script = script
	if preset != cfg.Preset {
		out.Preset = preset
		oldDefault, _ := runtime.NodePresetByKey(cfg.Preset)
		if cfg.Port == 0 || cfg.Port == oldDefault.Port {
			newDefault, _ := runtime.NodePresetByKey(preset)
			out.Port = newDefault.Port
		}
	}
	return out, true
}

// fitClonedNode applies fitNodeToRepo to a project created from a repository and stores
// the result. It reports whether the configuration changed, so the caller plans again.
func (m *Manager) fitClonedNode(ctx context.Context, planner *Planner, proj *store.Project) (bool, error) {
	svc := proj.Service(store.ServiceNode)
	if svc == nil || !svc.Enabled {
		return false, nil
	}
	cfg, ok := nodeDevConfig(*proj)
	if !ok || cfg.Normalize() != nil {
		return false, nil
	}
	fitted, changed := fitNodeToRepo(planner.ProjectDir(*proj), cfg)
	if !changed {
		return false, nil
	}
	if err := fitted.Normalize(); err != nil {
		return false, nil
	}
	raw, err := json.Marshal(fitted)
	if err != nil {
		return false, err
	}
	if err := m.store.Projects.UpdateServiceConfig(ctx, proj.ID, svc.Kind, svc.Version, svc.Image, raw); err != nil {
		return false, err
	}
	svc.Config = raw
	step(ctx, "Dev server: script {{script}} with the {{preset}} preset, from the repository's package.json", "script", fitted.Script, "preset", fitted.Preset)
	return true, nil
}
