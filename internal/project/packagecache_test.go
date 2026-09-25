package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
)

func hasCacheMount(spec docker.ContainerSpec) bool {
	return slices.ContainsFunc(spec.Mounts, func(m docker.MountSpec) bool {
		return m.Source == "/host/appdata/envoryx/cache" && m.Target == packageCacheTarget
	})
}

func TestEveryPackageManagerContainerSharesTheCache(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Shop", true)
	req.Node = &NodeRequest{Version: "24"}
	req.Python = &PythonRequest{Version: "3.13"}
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AddWorker(ctx, view.Project.ID, WorkerRequest{Name: "queue", Preset: "laravel:queue", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"envoryx-shop-php", "envoryx-shop-node", "envoryx-shop-python", "envoryx-shop-worker-queue"} {
		c, ok := e.engine.Container(name)
		if !ok {
			t.Fatalf("%s missing: %v", name, e.engine.ContainerNames())
		}
		env := strings.Join(c.Spec.Env, "\n")
		if !hasCacheMount(c.Spec) || !strings.Contains(env, "COMPOSER_CACHE_DIR="+packageCacheTarget+"/composer") || !strings.Contains(env, "npm_config_cache="+packageCacheTarget+"/npm") || !strings.Contains(env, "PIP_CACHE_DIR="+packageCacheTarget+"/pip") {
			t.Fatalf("%s: mounts %+v env %v", name, c.Spec.Mounts, c.Spec.Env)
		}
	}
	// The web server runs no package manager.
	if web, _ := e.engine.Container("envoryx-shop-web"); hasCacheMount(web.Spec) {
		t.Fatal("the web container must not get the cache")
	}
	// The directory exists and belongs to the project user's side.
	if info, err := os.Stat(filepath.Join(e.cfgDir, "cache")); err != nil || !info.IsDir() {
		t.Fatalf("cache dir: %v", err)
	}
	// Commands run with the tool environment must not point npm back at the project home.
	if slices.ContainsFunc(toolEnv, func(v string) bool { return strings.HasPrefix(v, "npm_config_cache=") }) {
		t.Fatal("toolEnv overrides the shared npm cache")
	}
}

func TestPackageCacheSizeAndClear(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, err := e.m.PackageCache(ctx)
	if err != nil || c.Bytes != 0 || len(c.Entries) != 0 {
		t.Fatalf("empty cache: %+v %v", c, err)
	}
	dir := filepath.Join(e.cfgDir, "cache")
	for tool, size := range map[string]int{"npm": 3000, "composer": 1000} {
		_ = os.MkdirAll(filepath.Join(dir, tool, "sub"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, tool, "sub", "blob"), make([]byte, size), 0o644)
	}
	c, err = e.m.PackageCache(ctx)
	if err != nil || c.Bytes != 4000 || len(c.Entries) != 2 || c.Entries[0].Tool != "npm" {
		t.Fatalf("cache: %+v %v", c, err)
	}
	c, err = e.m.ClearPackageCache(ctx, "npm")
	if err != nil || c.Bytes != 1000 {
		t.Fatalf("after clearing npm: %+v %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "npm")); err != nil {
		t.Fatal("the tool's directory itself stays")
	}
	if _, err := e.m.ClearPackageCache(ctx, "../etc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown tool: %v", err)
	}
	if c, err := e.m.ClearPackageCache(ctx, ""); err != nil || c.Bytes != 0 {
		t.Fatalf("after clearing all: %+v %v", c, err)
	}
}
