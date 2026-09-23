package project

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestFolderViewLabels(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	php, _ := e.engine.Container("envoryx-shop-php")
	if php.Spec.Labels[docker.LabelUnraidIcon] != docker.UnraidIcon {
		t.Fatalf("icon label missing: %v", php.Spec.Labels)
	}
	if _, ok := php.Spec.Labels[docker.LabelFolderView]; ok {
		t.Fatalf("no folder label without the setting: %v", php.Spec.Labels)
	}
	fingerprint := php.Spec.Labels[docker.LabelSpec]

	// A restart without the setting keeps the containers.
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.engine.Container("envoryx-shop-php"); again.ID != php.ID {
		t.Fatal("unchanged container must not be recreated")
	}

	// Setting the folder recreates the containers on the next start with the label.
	if err := e.m.SetFolderViewFolder(ctx, "  Envoryx "); err != nil {
		t.Fatal(err)
	}
	if got := e.m.FolderViewFolder(ctx); got != "Envoryx" {
		t.Fatalf("folder = %q", got)
	}
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"envoryx-shop-php", "envoryx-shop-web"} {
		c, _ := e.engine.Container(name)
		if c.Spec.Labels[docker.LabelFolderView] != "Envoryx" || c.State != "running" {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	if c, _ := e.engine.Container("envoryx-shop-php"); c.ID == php.ID || c.Spec.Labels[docker.LabelSpec] == fingerprint {
		t.Fatal("a new folder must recreate the container")
	}

	// Clearing it takes the label away again and restores the original fingerprint.
	if err := e.m.SetFolderViewFolder(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Restart(ctx, id); err != nil {
		t.Fatal(err)
	}
	c, _ := e.engine.Container("envoryx-shop-php")
	if _, ok := c.Spec.Labels[docker.LabelFolderView]; ok || c.Spec.Labels[docker.LabelSpec] != fingerprint {
		t.Fatalf("cleared folder: %v", c.Spec.Labels)
	}
}

func TestSetFolderViewFolderValidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, bad := range []string{strings.Repeat("x", 65), "Dev\nTools", "a\x00b"} {
		if err := e.m.SetFolderViewFolder(ctx, bad); !errors.Is(err, validate.ErrInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if err := e.m.SetFolderViewFolder(ctx, "Entwicklung – Projekte"); err != nil {
		t.Fatal(err)
	}
}

func TestDBToolFollowsFolderView(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.selfID = e.engine.AddManagedContainer(docker.ContainerSpec{Name: "envoryx", Labels: map[string]string{"x": "y"}}, "running")
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.SetDBToolEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.OpenDBTool(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := e.engine.Container(DBToolContainer)
	if before.Spec.Labels[docker.LabelUnraidIcon] != docker.UnraidIcon {
		t.Fatalf("icon label missing: %v", before.Spec.Labels)
	}

	if err := e.m.SetFolderViewFolder(ctx, "Envoryx"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.OpenDBTool(ctx, view.Project.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.engine.Container(DBToolContainer)
	if after.ID == before.ID || after.Spec.Labels[docker.LabelFolderView] != "Envoryx" {
		t.Fatalf("database browser must be recreated with the folder: %+v", after.Spec.Labels)
	}
}
