package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

// A virtual environment lives in the project directory, so it survives a container
// recreate – but it is built for one Python minor version: the packages sit in
// .venv/lib/python<major>.<minor>/site-packages and an interpreter of another minor does
// not look there. After a version change the venv is therefore still present and still
// empty from the new interpreter's point of view, and an application server starts only
// to die on its first import. site-packages cannot be moved across minors (compiled
// extensions are built per version), so Envoryx does not try to migrate it: it says what
// happened and names the action that rebuilds it.

// venvPythonVersion returns the major.minor a virtual environment was built for, read
// from .venv/pyvenv.cfg. Missing, unreadable or without a version line = nothing to say.
func venvPythonVersion(projectDir string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(projectDir, runtime.PythonVenv, "pyvenv.cfg"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "version" {
			continue
		}
		return majorMinor(strings.TrimSpace(value)), true
	}
	return "", false
}

// majorMinor keeps the first two components of a version ("3.13.15" → "3.13").
func majorMinor(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// rebuildHint names the action that reinstalls the dependencies, picked by what the
// project carries: uv.lock/pyproject.toml → uv sync, else requirements.txt.
func rebuildHint(projectDir string) string {
	if exists(filepath.Join(projectDir, "uv.lock")) || exists(filepath.Join(projectDir, "pyproject.toml")) {
		return `run "uv sync" from the Actions tab`
	}
	if exists(filepath.Join(projectDir, "requirements.txt")) {
		return `run "pip install -r requirements.txt" from the Actions tab`
	}
	return `delete .venv and install the dependencies again from the Python terminal`
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// venvWarning reports a virtual environment left behind by a Python version change. It
// returns "" while the project has no Python service, no venv, or a matching one.
func (m *Manager) venvWarning(p store.Project) string {
	svc := p.Service(store.ServicePython)
	if svc == nil || !svc.Enabled || svc.Version == "" {
		return ""
	}
	planner, err := m.planner()
	if err != nil {
		return ""
	}
	dir := planner.ProjectDir(p)
	built, ok := venvPythonVersion(dir)
	if !ok || built == majorMinor(svc.Version) {
		return ""
	}
	return fmt.Sprintf("the virtual environment was built for Python %s but the container runs %s – its packages are invisible to the new interpreter; %s", built, majorMinor(svc.Version), rebuildHint(dir))
}
