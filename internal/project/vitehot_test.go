package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A Laravel project's public/hot goes when Envoryx stops the project, and a start without
// a dev server removes a leftover one. A file of that name that holds no address stays,
// and so does the hot file of a project that is not Laravel.
func TestViteHotFileIsRemovedWithTheDevServer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	dir := filepath.Join(e.projDir, view.Project.Path)
	hot := filepath.Join(dir, "public", "hot")
	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(hot), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hot, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exists := func() bool { _, err := os.Stat(hot); return err == nil }

	// Not Laravel: left alone.
	write("https://shop-dev.test")
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !exists() {
		t.Fatal("hot file of a project without artisan removed")
	}

	if err := os.WriteFile(filepath.Join(dir, "artisan"), []byte("<?php\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if exists() {
		t.Fatal("a start without dev server keeps the leftover hot file")
	}
	write("http://0.0.0.0:5173\n")
	if _, err := e.m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if exists() {
		t.Fatal("stop keeps the hot file")
	}
	write("not an address")
	if _, err := e.m.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !exists() {
		t.Fatal("a hot file without an address was removed")
	}
}
