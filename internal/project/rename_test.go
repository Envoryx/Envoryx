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
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestRenameProjectMovesEverythingDerivedFromTheIdentifier(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var sql []string
	e.engine.ExecHandler = func(container string, cmd []string, env []string) (docker.ExecResult, error) {
		if len(cmd) > 0 && cmd[0] == "mariadb" {
			sql = append(sql, cmd[len(cmd)-1])
		}
		return docker.ExecResult{ExitCode: 0}, nil
	}
	var dumpDB, restoreDB string
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		switch cmd[0] {
		case "mariadb-dump":
			dumpDB = cmd[len(cmd)-1]
			return "CREATE TABLE orders (id int);\n", 0, nil
		case "mariadb":
			restoreDB = cmd[len(cmd)-1]
		}
		return "", 0, nil
	}
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	_ = os.WriteFile(filepath.Join(e.projDir, "shop", "public", "index.php"), []byte("v1"), 0o644)
	if _, err := e.m.CreateBackup(ctx, id, BackupOptions{Files: true}); err != nil {
		t.Fatal(err)
	}

	res, err := e.m.Rename(ctx, id, RenameRequest{Name: "Acme Blog", Confirm: "shop"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	p := res.View.Project
	if p.ID != id || p.Name != "Acme Blog" || p.Slug != "acme-blog" || p.Path != "acme-blog" {
		t.Fatalf("renamed project: %+v", p)
	}
	if res.From != "shop" || res.To != "acme-blog" || res.Database != "acme_blog" || res.Username != "acme_blog" {
		t.Fatalf("result: %+v", res)
	}
	// Containers, network and volumes carry the new identifier and nothing of the old one.
	names := strings.Join(e.engine.ContainerNames(), ",")
	if !strings.Contains(names, "envoryx-acme-blog-php") || strings.Contains(names, "envoryx-shop-") {
		t.Fatalf("containers: %s", names)
	}
	if got := e.engine.NetworkNames(); len(got) != 1 || got[0] != "envoryx-acme-blog" {
		t.Fatalf("networks: %v", got)
	}
	if vols := e.engine.VolumeNames(); !slices.Contains(vols, "envoryx-acme-blog-database") || slices.Contains(vols, "envoryx-shop-database") {
		t.Fatalf("volumes: %v", vols)
	}
	if len(e.engine.OneShots) == 0 || e.engine.OneShots[0].Mounts[0].Source != "envoryx-shop-database" {
		t.Fatalf("the volume must be copied: %+v", e.engine.OneShots)
	}
	// Files and backups moved with it.
	if b, err := os.ReadFile(filepath.Join(e.projDir, "acme-blog", "public", "index.php")); err != nil || string(b) != "v1" {
		t.Fatalf("project directory not moved: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old project directory must be gone")
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "backups", "acme-blog")); err != nil {
		t.Fatalf("backups not moved: %v", err)
	}
	backups, err := e.m.ListBackups(ctx, id)
	if err != nil || len(backups) != 1 || backups[0].Missing {
		t.Fatalf("the backup must still be readable: %+v %v", backups, err)
	}
	// The database moved through a dump into the new name, and the login was renamed.
	creds, err := e.m.DatabaseCredentials(ctx, id, "")
	if err != nil || creds.Database != "acme_blog" || creds.Username != "acme_blog" {
		t.Fatalf("credentials: %+v %v", creds, err)
	}
	if dumpDB != "shop" || restoreDB != "acme_blog" {
		t.Fatalf("dump %q must be restored into %q", dumpDB, restoreDB)
	}
	joined := strings.Join(sql, " | ")
	if !strings.Contains(joined, "RENAME USER 'shop'@'%' TO 'acme_blog'@'%'") || !strings.Contains(joined, "CREATE DATABASE `acme_blog`") || !strings.Contains(joined, "DROP DATABASE `shop`") {
		t.Fatalf("statements: %s", joined)
	}
	// The application containers see the new connection details.
	php, _ := e.engine.Container("envoryx-acme-blog-php")
	env := strings.Join(php.Spec.Env, "\n")
	if !strings.Contains(env, "DB_DATABASE=acme_blog") || !strings.Contains(env, "ENVORYX_PROJECT=acme-blog") {
		t.Fatalf("injected env: %v", php.Spec.Env)
	}
}

func TestRenameProjectKeepsTheDataNamesWhenAsked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, dbRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	res, err := e.m.Rename(ctx, id, RenameRequest{Name: "Blog", Confirm: "shop", KeepDataNames: true})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if res.Database != "" || res.Bucket != "" {
		t.Fatalf("nothing data-side may be renamed: %+v", res)
	}
	creds, _ := e.m.DatabaseCredentials(ctx, id, "")
	if creds.Database != "shop" || creds.Username != "shop" {
		t.Fatalf("the database keeps its name: %+v", creds)
	}
	if got := e.engine.NetworkNames(); len(got) != 1 || got[0] != "envoryx-blog" {
		t.Fatalf("the rest still moves: %v", got)
	}
}

func TestRenameProjectKeepsARunningProjectRunning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.m.Rename(ctx, view.Project.ID, RenameRequest{Name: "Blog", Confirm: "shop"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if res.View.Status.State != StateRunning {
		t.Fatalf("state after rename: %s (%v)", res.View.Status.State, res.View.Status.Warnings)
	}
	web, ok := e.engine.Container("envoryx-blog-web")
	if !ok || web.State != "running" {
		t.Fatalf("web container: %+v", web)
	}
}

func TestRenameProjectRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	shop, err := e.m.Create(ctx, phpRequest("Shop", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Create(ctx, phpRequest("Blog", false)); err != nil {
		t.Fatal(err)
	}
	id := shop.Project.ID
	for name, req := range map[string]RenameRequest{
		"no confirmation":    {Name: "Whatever"},
		"wrong confirmation": {Name: "Whatever", Confirm: "blog"},
		"empty name":         {Name: "   ", Confirm: "shop"},
	} {
		if _, err := e.m.Rename(ctx, id, req); !errors.Is(err, validate.ErrInvalid) {
			t.Fatalf("%s must be rejected: %v", name, err)
		}
	}
	if _, err := e.m.Rename(ctx, id, RenameRequest{Name: "Blog", Confirm: "shop"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an existing name must conflict: %v", err)
	}
	if _, err := e.m.Rename(ctx, store.NewID(), RenameRequest{Name: "Ghost", Confirm: "ghost"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	// Nothing moved: the refused rename left the project exactly as it was.
	after, err := e.m.Get(ctx, id)
	if err != nil || after.Project.Slug != "shop" || after.Project.Path != "shop" {
		t.Fatalf("project after refusals: %+v", after.Project)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop")); err != nil {
		t.Fatalf("project directory: %v", err)
	}
	if names := e.engine.ContainerNames(); !slices.Contains(names, "envoryx-shop-web") {
		t.Fatalf("containers: %v", names)
	}
}

// A rename that fails before anything has moved leaves the project exactly as it was –
// running, under its old name.
func TestRenameProjectPutsEverythingBackWhenItFailsEarly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	view, err := e.m.Create(ctx, phpRequest("Shop", true))
	if err != nil {
		t.Fatal(err)
	}
	id := view.Project.ID
	// The project directory cannot move because the target is already there.
	if err := os.MkdirAll(filepath.Join(e.projDir, "blog"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Rename(ctx, id, RenameRequest{Name: "Blog", Confirm: "shop"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the rename must fail: %v", err)
	}
	after, err := e.m.Get(ctx, id)
	if err != nil || after.Project.Slug != "shop" || after.Project.Name != "Shop" || after.Project.Path != "shop" {
		t.Fatalf("the project must keep its old identity: %+v %v", after.Project, err)
	}
	if after.Status.State != StateRunning {
		t.Fatalf("a running project must run again: %s", after.Status.State)
	}
	if names := e.engine.ContainerNames(); !slices.Contains(names, "envoryx-shop-web") || slices.Contains(names, "envoryx-blog-web") {
		t.Fatalf("containers: %v", names)
	}
}

func TestRenameProjectKeepsADeliberateDirectory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := phpRequest("Shop", false)
	req.Path = "customers/shop"
	view, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.m.Rename(ctx, view.Project.ID, RenameRequest{Name: "Blog", Confirm: "shop"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if res.View.Project.Path != "customers/shop" {
		t.Fatalf("a directory the user chose stays: %s", res.View.Project.Path)
	}
	// Unless the rename says where to.
	res, err = e.m.Rename(ctx, view.Project.ID, RenameRequest{Name: "Blog", Path: "customers/blog", Confirm: "blog"})
	if err != nil {
		t.Fatalf("rename with path: %v", err)
	}
	if res.View.Project.Path != "customers/blog" {
		t.Fatalf("path: %s", res.View.Project.Path)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "customers", "blog")); err != nil {
		t.Fatalf("directory not moved: %v", err)
	}
}
