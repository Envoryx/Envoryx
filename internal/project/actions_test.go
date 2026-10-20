package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Shopware's build scripts switch to bin/ci under CI, which only loads Composer plugins:
// the plugins in custom/plugins would be left out of the build. Every other action keeps
// CI=1 so composer and npm never ask.
func TestActionEnvCI(t *testing.T) {
	for _, id := range []string{"shopware:build-administration", "shopware:build-storefront", "composer:install"} {
		a, ok := findAction(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		hasCI := false
		for _, kv := range a.env() {
			if strings.HasPrefix(kv, "CI=") {
				hasCI = true
			}
		}
		if want := !strings.HasPrefix(id, "shopware:"); hasCI != want {
			t.Fatalf("%s: CI set %v, want %v", id, hasCI, want)
		}
	}
}

// craft install exits 0 without installing anything when Craft is already there: the
// action shows a password only when install/check says the install happened. Run with
// a php stub that plays Craft.
func TestCraftInstallScriptShowsPasswordOnlyAfterInstall(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	bin := t.TempDir()
	// INSTALLED says whether install/check succeeds before; AFTER whether it does after.
	stub := `#!/bin/sh
case "$1 $2" in
  "-r "*) echo abc ;;
  "craft install/check") if [ -f "$STATE" ]; then exit 0; fi; exit 1 ;;
  "craft install") echo "site-url: $(printf '%s' "$*" | sed -n 's/.*--site-url=\([^ ]*\).*/\1/p')"; if [ "$AFTER" = 1 ]; then touch "$STATE"; fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "php"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(installed, after bool) (string, error) {
		work := t.TempDir()
		state := filepath.Join(work, "installed")
		if installed {
			_ = os.WriteFile(state, nil, 0o644)
		}
		_ = os.WriteFile(filepath.Join(work, ".env"), []byte("PRIMARY_SITE_URL=\nX=1\n"), 0o644)
		cmd := exec.Command("sh", "-c", craftInstallScript, "envoryx-craft")
		cmd.Dir = work
		a := "0"
		if after {
			a = "1"
		}
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "STATE="+state, "AFTER="+a, "ENVORYX_URL=https://blog.test", "ENVORYX_PROJECT=Blog")
		out, err := cmd.CombinedOutput()
		env, _ := os.ReadFile(filepath.Join(work, ".env"))
		return string(out) + "\n.env:\n" + string(env), err
	}
	out, err := run(false, true)
	if err != nil || !strings.Contains(out, "Password: Envoryx-abc!") || !strings.Contains(out, "site-url: $PRIMARY_SITE_URL") || !strings.Contains(out, "PRIMARY_SITE_URL=${ENVORYX_URL}\nX=1") {
		t.Fatalf("fresh install: %v\n%s", err, out)
	}
	for _, tc := range []struct{ installed, after bool }{{true, true}, {false, false}} {
		out, err := run(tc.installed, tc.after)
		if err == nil || strings.Contains(out, "Password:") {
			t.Fatalf("installed=%v after=%v: %v\n%s", tc.installed, tc.after, err, out)
		}
	}
}

// The catalogue is filtered to the services a project has: a Node-only project sees no
// composer/artisan/php entries, a PHP-only project no npm entries.
func TestListActionsOmitsAbsentServices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	front, err := e.m.Create(ctx, nodeRequest("Front", true))
	if err != nil {
		t.Fatal(err)
	}
	infos, err := e.m.ListActions(ctx, front.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ActionInfo{}
	for _, a := range infos {
		if a.Service != "node" {
			t.Fatalf("node-only project lists %s (%s)", a.ID, a.Service)
		}
		byID[a.ID] = a
	}
	if v, ok := byID["node:version"]; !ok || !v.Available {
		t.Fatalf("node:version: %+v", byID)
	}
	if v := byID["npm:install"]; v.Available || !strings.Contains(v.Reason, "package.json not found") {
		t.Fatalf("npm:install before package.json: %+v", v)
	}
	if err := os.WriteFile(filepath.Join(e.projDir, "front", "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	infos, _ = e.m.ListActions(ctx, front.Project.ID)
	for _, a := range infos {
		if a.ID == "npm:install" && !a.Available {
			t.Fatalf("npm:install with package.json: %+v", a)
		}
	}
	// pnpm and yarn need their lockfile, unless they are the project's package manager:
	// a fresh project has none yet.
	pn := nodeRequest("Pn", true)
	pn.Node.Config.PackageManager = "pnpm"
	pnv, err := e.m.Create(ctx, pn)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.projDir, "pn", "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	infos, _ = e.m.ListActions(ctx, pnv.Project.ID)
	for _, a := range infos {
		if want := a.ID == "pnpm:install" || a.ID == "pnpm:build" || strings.HasPrefix(a.ID, "npm:") && a.ID != "npm:ci" || a.ID == "node:version"; a.Available != want {
			t.Errorf("%s in a pnpm project without a lockfile: available %v", a.ID, a.Available)
		}
	}
	if _, _, _, err := e.m.RunAction(ctx, front.Project.ID, "composer:install", 0, 0); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "no php service") {
		t.Fatalf("action of an absent service: %v", err)
	}

	shop, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	infos, _ = e.m.ListActions(ctx, shop.Project.ID)
	for _, a := range infos {
		if a.Service != "php" {
			t.Fatalf("php-only project lists %s (%s)", a.ID, a.Service)
		}
	}
}
