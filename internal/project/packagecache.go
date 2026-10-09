package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/envoryx/envoryx/internal/audit"
)

// SettingSharedPackageCache is the settings key of the opt-in that gives all projects one
// package cache.
const SettingSharedPackageCache = "shared_package_cache"

// SharedPackageCache reports whether all projects share one package cache. Off by
// default: a shared cache is writable from every project, and most package managers take
// a cached package without checking it against the lock file.
func (m *Manager) SharedPackageCache(ctx context.Context) bool {
	v, err := m.store.Settings.Get(ctx, SettingSharedPackageCache)
	return err == nil && v == "true"
}

// SetSharedPackageCache stores the preference. Projects pick it up when their containers
// are recreated.
func (m *Manager) SetSharedPackageCache(ctx context.Context, on bool) error {
	if err := m.store.Settings.Set(ctx, SettingSharedPackageCache, strconv.FormatBool(on)); err != nil {
		return err
	}
	m.audit.Log(ctx, audit.ActionSettingsChanged, "settings", "", map[string]any{"sharedPackageCache": on})
	return nil
}

// PackageCacheEntry is the part of the package caches one tool keeps.
type PackageCacheEntry struct {
	Tool  string `json:"tool"` // composer, npm, yarn, pip, uv, gomod, gobuild, bundler
	Bytes int64  `json:"bytes"`
}

// PackageCache describes the package caches: the shared one, or every project's own
// summed up per tool.
type PackageCache struct {
	Path    string              `json:"path"`
	Shared  bool                `json:"shared"`
	Bytes   int64               `json:"bytes"`
	Entries []PackageCacheEntry `json:"entries"`
}

// packageCacheDirs are the package caches on disk: the shared one and each project's
// own, whichever exist (the shared one stays around when sharing is switched off).
func (m *Manager) packageCacheDirs(ctx context.Context) (Paths, []string, error) {
	paths, err := m.paths()
	if err != nil {
		return Paths{}, nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	planner := NewPlanner(paths, m.catalog)
	dirs := []string{planner.SharedPackageCacheDir()}
	projects, err := m.store.Projects.List(ctx)
	if err != nil {
		return Paths{}, nil, err
	}
	for _, p := range projects {
		dirs = append(dirs, planner.ProjectPackageCacheDir(p.ID))
	}
	return paths, dirs, nil
}

// PackageCache measures the package caches: per tool and in total.
func (m *Manager) PackageCache(ctx context.Context) (PackageCache, error) {
	paths, dirs, err := m.packageCacheDirs(ctx)
	if err != nil {
		return PackageCache{}, err
	}
	planner := NewPlanner(paths, m.catalog)
	out := PackageCache{Shared: m.SharedPackageCache(ctx), Entries: []PackageCacheEntry{}}
	out.Path = planner.SharedPackageCacheDir()
	if !out.Shared {
		out.Path = filepath.Join(paths.ConfigDir, "projects", "<project>", packageCacheDir)
	}
	sizes := map[string]int64{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return PackageCache{}, err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return PackageCache{}, err
			}
			if !e.IsDir() {
				continue
			}
			sizes[e.Name()] += dirSize(filepath.Join(dir, e.Name()))
		}
	}
	for tool, n := range sizes {
		out.Entries = append(out.Entries, PackageCacheEntry{Tool: tool, Bytes: n})
		out.Bytes += n
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Bytes != out.Entries[j].Bytes {
			return out.Entries[i].Bytes > out.Entries[j].Bytes
		}
		return out.Entries[i].Tool < out.Entries[j].Tool
	})
	return out, nil
}

// ClearPackageCache empties the package caches (tool "" for all of it). The next
// install downloads again; an install running right now may fail and has to be repeated.
func (m *Manager) ClearPackageCache(ctx context.Context, tool string) (PackageCache, error) {
	before, err := m.PackageCache(ctx)
	if err != nil {
		return PackageCache{}, err
	}
	found := tool == ""
	for _, e := range before.Entries {
		if tool != "" && e.Tool != tool {
			continue
		}
		found = true
	}
	if !found {
		return PackageCache{}, fmt.Errorf("%w: the package cache holds nothing of %q", ErrNotFound, tool)
	}
	_, dirs, err := m.packageCacheDirs(ctx)
	if err != nil {
		return PackageCache{}, err
	}
	for _, dir := range dirs {
		for _, e := range before.Entries {
			if tool != "" && e.Tool != tool {
				continue
			}
			sub := filepath.Join(dir, e.Tool)
			if info, err := os.Lstat(sub); err != nil || !info.IsDir() {
				continue
			}
			// The tool's directory stays (owned by the project user); its contents go.
			if err := wipeDir(sub); err != nil {
				return PackageCache{}, fmt.Errorf("clear %s: %w", e.Tool, err)
			}
		}
	}
	after, err := m.PackageCache(ctx)
	if err != nil {
		return PackageCache{}, err
	}
	details := map[string]any{"freed": before.Bytes - after.Bytes}
	if tool != "" {
		details["tool"] = tool
	}
	m.audit.Log(ctx, audit.ActionPackageCacheCleared, "system", "package-cache", details)
	return after, nil
}
