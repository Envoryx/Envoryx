package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/envoryx/envoryx/internal/audit"
)

// PackageCacheEntry is the part of the shared package cache one tool keeps.
type PackageCacheEntry struct {
	Tool  string `json:"tool"` // composer, npm, yarn, pnpm, pip, uv
	Bytes int64  `json:"bytes"`
}

// PackageCache describes the shared package cache.
type PackageCache struct {
	Path    string              `json:"path"`
	Bytes   int64               `json:"bytes"`
	Entries []PackageCacheEntry `json:"entries"`
}

// PackageCache measures the shared package cache: per tool and in total.
func (m *Manager) PackageCache(ctx context.Context) (PackageCache, error) {
	paths, err := m.paths()
	if err != nil {
		return PackageCache{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	dir := NewPlanner(paths, m.catalog).PackageCacheDir()
	out := PackageCache{Path: dir, Entries: []PackageCacheEntry{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
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
		n := dirSize(filepath.Join(dir, e.Name()))
		out.Entries = append(out.Entries, PackageCacheEntry{Tool: e.Name(), Bytes: n})
		out.Bytes += n
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Bytes > out.Entries[j].Bytes })
	return out, nil
}

// ClearPackageCache empties the shared package cache (tool "" = all of it). The next
// install downloads again; an install running right now may fail and has to be repeated.
func (m *Manager) ClearPackageCache(ctx context.Context, tool string) (PackageCache, error) {
	paths, err := m.paths()
	if err != nil {
		return PackageCache{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	before, err := m.PackageCache(ctx)
	if err != nil {
		return PackageCache{}, err
	}
	dir := NewPlanner(paths, m.catalog).PackageCacheDir()
	found := tool == ""
	for _, e := range before.Entries {
		if tool != "" && e.Tool != tool {
			continue
		}
		found = true
		// The tool's directory stays (owned by the project user); its contents go.
		if err := wipeDir(filepath.Join(dir, e.Tool)); err != nil {
			return PackageCache{}, fmt.Errorf("clear %s: %w", e.Tool, err)
		}
	}
	if !found {
		return PackageCache{}, fmt.Errorf("%w: the package cache holds nothing of %q", ErrNotFound, tool)
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
