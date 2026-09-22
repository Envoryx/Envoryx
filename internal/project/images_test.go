package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
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
	if _, err := e.m.Update(ctx, view.Project.ID, UpdateRequest{PHP: &PHPUpdate{Enabled: true, Version: "8.3", Config: cfg}}); err != nil {
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

// After an upstream rebuild of the same tag the previous image stays known, is protected
// from pruning and the project can be rolled back to it – and forward again.
func TestImageRollback(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Roll", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	img := view.Project.Service(store.ServicePHP).Image
	php := func() ServiceStatus {
		v, err := e.m.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range v.Status.Services {
			if s.Kind == store.ServicePHP {
				return s
			}
		}
		t.Fatal("no php status")
		return ServiceStatus{}
	}
	if s := php(); s.ImagePrevious || s.ImagePinned {
		t.Fatalf("fresh project must have no history: %+v", s)
	}
	if _, err := e.m.UseImage(ctx, id, img, ImagePrevious); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("rollback without history = %v", err)
	}

	// Upstream rebuilt the tag; Restart pulls and recreates.
	e.engine.Remote[img] = img + "@v2"
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	s := php()
	if !s.ImagePrevious || s.ImagePinned || s.ImageChangedAt == nil {
		t.Fatalf("history after rebuild: %+v", s)
	}
	c, _ := e.engine.Container("envoryx-roll-php")
	if c.ImageID != img+"@v2" {
		t.Fatalf("container image = %s", c.ImageID)
	}
	// The old image carries the rollback tag now, so `docker image prune` cannot take it,
	// and it must not be offered for pruning either.
	rollTag := rollbackRef("roll", img)
	if got, err := e.engine.ImageID(ctx, rollTag); err != nil || got != img+"@v1" {
		t.Fatalf("rollback tag %s -> %q, %v; want %s", rollTag, got, err, img+"@v1")
	}
	unused, err := e.m.UnusedImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range unused {
		if u.ID == img+"@v1" {
			t.Fatal("previous image must be protected from pruning")
		}
	}

	// Roll back: containers run the previous id, no misleading warnings.
	view, err = e.m.UseImage(ctx, id, img, ImagePrevious)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	c, _ = e.engine.Container("envoryx-roll-php")
	if c.ImageID != img+"@v1" || c.State != "running" {
		t.Fatalf("after rollback: %+v", c)
	}
	if len(view.Status.Warnings) != 0 {
		t.Fatalf("warnings after rollback: %v", view.Status.Warnings)
	}
	if s := php(); !s.ImagePinned || !s.ImagePrevious {
		t.Fatalf("status after rollback: %+v", s)
	}
	// A plain restart keeps the pin (the tag is pulled but not applied).
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if c, _ = e.engine.Container("envoryx-roll-php"); c.ImageID != img+"@v1" {
		t.Fatalf("restart must keep the rollback: %+v", c)
	}
	// Pinned: the current image is protected too.
	unused, _ = e.m.UnusedImages(ctx)
	for _, u := range unused {
		if u.ID == img+"@v2" {
			t.Fatal("current image must be protected while rolled back")
		}
	}

	// Forward again.
	if _, err := e.m.UseImage(ctx, id, img, ImageLatest); err != nil {
		t.Fatalf("latest: %v", err)
	}
	c, _ = e.engine.Container("envoryx-roll-php")
	if c.ImageID != img+"@v2" || c.State != "running" {
		t.Fatalf("after returning to latest: %+v", c)
	}
	s = php()
	if s.ImagePinned || !s.ImagePrevious {
		t.Fatalf("status after latest: %+v", s)
	}
	if _, err := e.m.UseImage(ctx, id, img, "sideways"); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("bad choice = %v", err)
	}

	// A second rebuild supersedes the rollback target: the tag moves to v2, v1 is
	// released and – unreferenced – gone.
	e.engine.Remote[img] = img + "@v3"
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.engine.ImageID(ctx, rollTag); got != img+"@v2" {
		t.Fatalf("rollback tag after second rebuild = %q, want v2", got)
	}
	if ok, _ := e.engine.ImageExists(ctx, img+"@v1"); ok {
		t.Fatal("superseded rollback target should have been released")
	}

	// Deleting the project releases the tag too.
	if err := e.m.Delete(ctx, id, DeleteOptions{Confirm: "roll", DeleteFiles: true}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.engine.ImageExists(ctx, rollTag); ok {
		t.Fatal("rollback tag must go with the project")
	}
}

func TestRollbackRef(t *testing.T) {
	cases := map[string]string{
		"mariadb:11.4":                    "envoryx-rollback/shop:mariadb-11.4",
		"ghcr.io/envoryx/envoryx-php:8.4": "envoryx-rollback/shop:ghcr.io-envoryx-envoryx-php-8.4",
		"Caddy:2-Alpine":                  "envoryx-rollback/shop:caddy-2-alpine",
		"img@sha256:abc":                  "envoryx-rollback/shop:img-sha256-abc",
	}
	for in, want := range cases {
		if got := rollbackRef("shop", in); got != want {
			t.Errorf("rollbackRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// A history row whose tag was lost (older installation, manual `docker rmi`) is tagged
// again on reconcile; a stale rollback tag nobody's history refers to becomes prunable.
func TestRollbackTagsReconciled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Roll", true))
	if err != nil {
		t.Fatal(err)
	}
	img := view.Project.Service(store.ServicePHP).Image
	e.engine.Remote[img] = img + "@v2"
	if _, err := e.m.Restart(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	rollTag := rollbackRef("roll", img)
	e.engine.Dangle(rollTag)
	if ok, _ := e.engine.ImageExists(ctx, rollTag); ok {
		t.Fatal("tag should be gone")
	}
	e.m.Reconcile(ctx)
	if got, _ := e.engine.ImageID(ctx, rollTag); got != img+"@v1" {
		t.Fatalf("tag not restored by reconcile: %q", got)
	}

	e.engine.AddImage("ghcr.io/envoryx/envoryx-php:7.4")
	if err := e.engine.TagImage(ctx, "ghcr.io/envoryx/envoryx-php:7.4", "envoryx-rollback/gone:ghcr.io-envoryx-envoryx-php-7.4"); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.UntagImage(ctx, "ghcr.io/envoryx/envoryx-php:7.4"); err != nil {
		t.Fatal(err)
	}
	unused, err := e.m.UnusedImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range unused {
		if u.ID == "ghcr.io/envoryx/envoryx-php:7.4@v1" {
			found = true
		}
		if u.ID == img+"@v1" {
			t.Fatal("live rollback target offered for pruning")
		}
	}
	if !found {
		t.Fatalf("stale rollback tag not offered for pruning: %+v", unused)
	}
}

// A rollback target that has left the host for good (`docker rmi`, a prune before the
// tag existed) is forgotten on the next reconcile: the periodic reconcile must not retry
// the tag and warn every 30 seconds, and the UI must stop offering a rollback that
// cannot work.
func TestVanishedRollbackTargetForgotten(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Roll", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	img := view.Project.Service(store.ServicePHP).Image
	e.engine.Remote[img] = img + "@v2"
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.RemoveImage(ctx, img+"@v1"); err != nil {
		t.Fatal(err)
	}

	e.m.Reconcile(ctx)
	rec, err := e.m.store.Images.Get(ctx, id, img)
	if err != nil {
		t.Fatal(err)
	}
	if rec.PreviousID != "" || rec.Pinned {
		t.Fatalf("vanished target still recorded: %+v", rec)
	}
	v, err := e.m.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Status.Services {
		if s.Kind == store.ServicePHP && s.ImagePrevious {
			t.Fatalf("rollback still offered: %+v", s)
		}
	}
	// The next reconcile has nothing left to tag.
	e.engine.Calls = nil
	e.m.Reconcile(ctx)
	for _, c := range e.engine.Calls {
		if strings.HasPrefix(c, "tag:") {
			t.Fatalf("reconcile still retries the rollback tag: %v", e.engine.Calls)
		}
	}
}
