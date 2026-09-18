package project

import (
	"context"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
)

func TestUnusedImagesOnlyTouchCatalogueImages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Foreign images and a foreign container must never be considered.
	e.engine.AddImage("plexinc/pms-docker:latest")
	e.engine.AddImage("traefik:latest")
	e.engine.AddForeignContainer("plex", "plexinc/pms-docker:latest", "running")
	view, err := e.m.Create(ctx, phpRequest("Img", true))
	if err != nil {
		t.Fatal(err)
	}
	// Switch PHP version: the old image stays behind unused.
	cfg := runtime.DefaultPHPConfig()
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{PHP: &PHPRequest{Version: "8.3", Config: cfg}}); err != nil {
		t.Fatal(err)
	}
	unused, err := e.m.UnusedImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unused) != 1 || unused[0].Tags[0] != "ghcr.io/envoryx/envoryx-php:8.4" {
		t.Fatalf("unused: %+v", unused)
	}
	res, err := e.m.PruneImages(ctx)
	if err != nil || len(res.Removed) != 1 || res.ReclaimedBytes == 0 || len(res.Errors) != 0 {
		t.Fatalf("prune: %+v %v", res, err)
	}
	if ok, _ := e.engine.ImageExists(ctx, "traefik:latest"); !ok {
		t.Fatal("foreign image must survive")
	}
	if ok, _ := e.engine.ImageExists(ctx, "ghcr.io/envoryx/envoryx-php:8.3"); !ok {
		t.Fatal("image in use must survive")
	}
	for _, c := range e.engine.Calls {
		if strings.HasPrefix(c, "image-remove:") && !strings.Contains(c, "envoryx-php:8.4") {
			t.Fatalf("unexpected removal: %s", c)
		}
	}
	again, _ := e.m.UnusedImages(ctx)
	if len(again) != 0 {
		t.Fatalf("nothing should be left: %+v", again)
	}
}
