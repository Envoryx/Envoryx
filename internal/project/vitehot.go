package project

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/envoryx/envoryx/internal/store"
)

// laravel-vite-plugin writes public/hot while the Vite dev server runs, and Laravel's
// @vite then loads every asset from the address in it. The plugin deletes the file when
// Vite exits on a signal, but not when the container is killed at the end of the stop
// timeout, and a hot file that outlives the dev server leaves the pages without styles
// and scripts. So Envoryx removes it whenever it takes the dev server down, and before a
// start without one.

// viteHotFile is the hot file of a Laravel project, "" for any other project.
func (m *Manager) viteHotFile(p store.Project) string {
	planner, err := m.planner()
	if err != nil {
		return ""
	}
	dir := planner.ProjectDir(p)
	if _, err := os.Stat(filepath.Join(dir, "artisan")); err != nil {
		return ""
	}
	return filepath.Join(dir, "public", "hot")
}

// clearViteHot removes a Laravel project's public/hot. Only a file that holds an address
// is taken, so nothing else of that name is lost.
func (m *Manager) clearViteHot(p store.Project) {
	path := m.viteHotFile(p)
	if path == "" {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if s := strings.TrimSpace(string(b)); !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.log.Warn("remove the Vite hot file", "project", p.Slug, "path", path, "err", err)
	}
}
