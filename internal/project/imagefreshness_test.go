package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
)

func shopRequest() CreateRequest {
	req := phpRequest("Shop", true)
	req.Database = &DatabaseRequest{Type: "mariadb", Version: "11"}
	req.Mailpit = &ExtraRequest{}
	return req
}

// calledSince returns the engine calls recorded after the first n.
func calledSince(calls []string, n int) []string { return slices.Clone(calls[n:]) }

// A new project gets the image the registry has now, not an older build of the tag that
// happens to be on the host; offline, the local image still does.
func TestCreatePullsCurrentImages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	php := "ghcr.io/envoryx/envoryx-php:8.4"
	e.engine.AddImage(php) // an older build: php@v1
	e.engine.Remote[php] = php + "@v2"
	v, err := e.m.Create(ctx, phpRequest("Fresh", true))
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-fresh-php"); c.ImageID != php+"@v2" {
		t.Fatalf("new project must run the current image, got %s", c.ImageID)
	}

	// Switching the version pulls the other tag fresh as well.
	other := "ghcr.io/envoryx/envoryx-php:8.3"
	e.engine.AddImage(other)
	e.engine.Remote[other] = other + "@v2"
	if _, err := e.m.Update(ctx, v.Project.ID, UpdateRequest{PHP: &PHPUpdate{Enabled: true, Version: "8.3", Config: runtime.DefaultPHPConfig()}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-fresh-php"); c.ImageID != other+"@v2" {
		t.Fatalf("version switch must run the current image, got %s", c.ImageID)
	}

	// Registry down: the local image is used.
	e.engine.Remote[php] = php + "@v3"
	e.engine.FailPull[php] = errors.New("registry unreachable")
	if _, err := e.m.Create(ctx, phpRequest("Offline", true)); err != nil {
		t.Fatalf("create offline with a local image: %v", err)
	}
	if c, _ := e.engine.Container("envoryx-offline-php"); c.ImageID != php+"@v2" {
		t.Fatalf("offline create must use the local image, got %s", c.ImageID)
	}
}

// An image another project pulled is applied by a restart only: adding a worker or
// changing a setting leaves the containers it doesn't concern alone.
func TestChangesLeaveImageUpdatesPending(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v, err := e.m.Create(ctx, shopRequest())
	if err != nil {
		t.Fatal(err)
	}
	id := v.Project.ID
	db := v.Project.Service(store.ServiceDatabase).Image
	mailpit := v.Project.Service(store.ServiceMailpit).Image
	for _, img := range []string{db, mailpit} {
		e.engine.Remote[img] = img + "@v2"
		if err := e.engine.PullImage(ctx, img, nil); err != nil { // another project's restart
			t.Fatal(err)
		}
	}
	n := len(e.engine.Calls)
	if _, err := e.m.AddWorker(ctx, id, WorkerRequest{Name: "queue", Preset: "laravel:queue", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range calledSince(e.engine.Calls, n) {
		if strings.Contains(c, "database") || strings.Contains(c, "mailpit") || c == "remove:envoryx-shop-php" {
			t.Fatalf("adding a worker touched another container: %s", c)
		}
	}
	got, err := e.m.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Status.Warnings, func(w string) bool { return strings.Contains(w, "newer database image") }) {
		t.Fatalf("the pending update must still be reported: %v", got.Status.Warnings)
	}

	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.engine.Container("envoryx-shop-database"); c.ImageID != db+"@v2" {
		t.Fatalf("restart must apply the newer image, got %s", c.ImageID)
	}
}
