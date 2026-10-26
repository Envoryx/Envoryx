package project

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

func TestRepoPackageManager(t *testing.T) {
	for name, c := range map[string]struct {
		files       map[string]string
		want, which string
	}{
		"nothing":         {map[string]string{"package.json": `{}`}, "", ""},
		"pnpm lockfile":   {map[string]string{"package.json": `{}`, "pnpm-lock.yaml": ""}, "pnpm", "pnpm-lock.yaml"},
		"yarn lockfile":   {map[string]string{"package.json": `{}`, "yarn.lock": ""}, "yarn", "yarn.lock"},
		"npm lockfile":    {map[string]string{"package.json": `{}`, "package-lock.json": "{}"}, "npm", "package-lock.json"},
		"corepack field":  {map[string]string{"package.json": `{"packageManager": "pnpm@9.15.0+sha512.abc"}`, "package-lock.json": "{}"}, "pnpm", "package.json"},
		"unknown manager": {map[string]string{"package.json": `{"packageManager": "bun@1.2.0"}`, "yarn.lock": ""}, "yarn", "yarn.lock"},
	} {
		dir := t.TempDir()
		writeProjectFiles(t, dir, c.files)
		if got, from := repoPackageManager(dir); got != c.want || from != c.which {
			t.Errorf("%s: %q from %q, want %q from %q", name, got, from, c.want, c.which)
		}
	}
}

// A project cloned from a pnpm repository gets pnpm in its Node settings, whatever the
// wizard picked before it could see the repository: the dev server installs by the
// lockfile, and the actions and the warnings have to say the same.
func TestClonedRepositorySetsThePackageManager(t *testing.T) {
	for _, dev := range []bool{true, false} {
		e := newEnv(t)
		ctx := context.Background()
		e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
			if len(spec.Cmd) > 3 && spec.Cmd[3] == "clone" {
				writeProjectFiles(t, filepath.Join(e.projDir, "app"), map[string]string{
					".git/HEAD":      "ref: refs/heads/main\n",
					"package.json":   `{"scripts": {"dev": "vite"}, "devDependencies": {"vite": "^8.0.0"}}`,
					"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
				})
			}
			return docker.ExecResult{}, nil
		}
		req := nodeRequest("App", false)
		req.Node.Config.DevServer = dev
		req.Git = &GitRequest{URL: "https://example.test/app.git"}
		v, err := e.m.Create(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var cfg runtime.NodeConfig
		_ = json.Unmarshal(v.Project.Service(store.ServiceNode).Config, &cfg)
		if cfg.PackageManager != "pnpm" {
			t.Errorf("dev server %v: package manager %q", dev, cfg.PackageManager)
		}
	}
}
