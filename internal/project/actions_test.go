package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
