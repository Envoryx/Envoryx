package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/store"
)

// A long-running composer script must not hit Composer's 300 s process timeout.
func TestComposerScriptWorkerHasNoTimeout(t *testing.T) {
	cmd, err := WorkerCommand(store.Worker{Preset: "composer:script", Args: []string{"queue"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmd, " "); got != "composer run-script --timeout=0 --no-interaction -- queue" {
		t.Fatalf("cmd: %s", got)
	}
}

func TestFrameworkWorkerPresets(t *testing.T) {
	for id, want := range map[string]string{
		"shopware:queue":           "php bin/console messenger:consume async low_priority --time-limit=3600 --memory-limit=512M -v",
		"shopware:scheduled-tasks": "php bin/console scheduled-task:run --time-limit=3600 --memory-limit=512M",
		"craft:queue":              "php craft queue/listen --verbose",
	} {
		cmd, err := WorkerCommand(store.Worker{Preset: id})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(cmd, " "); got != want {
			t.Errorf("%s: %s", id, got)
		}
		if _, err := WorkerCommand(store.Worker{Preset: id, Args: []string{"x"}}); err == nil {
			t.Errorf("%s takes no argument", id)
		}
	}
	p, _ := workerPreset("shopware:queue")
	if g := p.guard(""); !strings.Contains(g, "setup-transports --no-interaction async;") || !strings.Contains(g, "setup-transports --no-interaction low_priority;") {
		t.Errorf("shopware guard: %s", g)
	}
	// The select shows labels only, so they must tell the presets of one runtime apart.
	seen := map[string]string{}
	for _, p := range WorkerPresets() {
		key := p.Runtime + "/" + p.Label
		if other, ok := seen[key]; ok {
			t.Errorf("presets %s and %s share the label %q", other, p.ID, p.Label)
		}
		seen[key] = p.ID
	}
}

func TestSuggestedWorkerPresetFollowsTheFramework(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	p := view.Project
	dir := filepath.Join(e.projDir, p.Path)
	touch := func(rel string) {
		t.Helper()
		f := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.m.SuggestedWorkerPreset(p); got != "" {
		t.Fatalf("a plain PHP project: %q", got)
	}
	touch("composer.json")
	if got := e.m.SuggestedWorkerPreset(p); got != "composer:script" {
		t.Fatalf("with composer.json only: %q", got)
	}
	touch("bin/console")
	touch("vendor/symfony/messenger/composer.json")
	if got := e.m.SuggestedWorkerPreset(p); got != "symfony:messenger" {
		t.Fatalf("a Symfony project: %q", got)
	}
	touch("vendor/shopware/core/composer.json")
	if got := e.m.SuggestedWorkerPreset(p); got != "shopware:queue" {
		t.Fatalf("a Shopware project: %q", got)
	}
	front, err := e.m.Create(ctx, nodeRequest("Front", false))
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(e.projDir, front.Project.Path)
	touch("artisan")
	touch("package.json")
	if got := e.m.SuggestedWorkerPreset(front.Project); got != "node:script" {
		t.Fatalf("a Node project must not get a PHP preset: %q", got)
	}
}
