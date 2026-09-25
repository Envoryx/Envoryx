package project

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// stageSite uploads a ZIP of files (name → content) and optionally a dump.
func stageSite(t *testing.T, e *env, files map[string]string, dump string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	up, err := e.m.BeginSiteImport()
	if err != nil {
		t.Fatal(err)
	}
	if err := up.Site(&buf, "site.zip"); err != nil {
		t.Fatal(err)
	}
	if dump != "" {
		if err := up.Dump(strings.NewReader(dump), "dump.sql"); err != nil {
			t.Fatal(err)
		}
	}
	staged, err := up.Finish(e.m.SiteImportOptions())
	if err != nil {
		t.Fatal(err)
	}
	return staged.ID
}

var wordpressSite = map[string]string{
	"public_html/wp-config.php":           "<?php\ndefine('DB_NAME', 'old');\ndefine('DB_USER', 'old');\ndefine('DB_PASSWORD', 'old');\ndefine('DB_HOST', 'old.example.com');\nrequire_once ABSPATH . 'wp-settings.php';\n",
	"public_html/wp-includes/version.php": "<?php\n$wp_version = '6.6.1';\n",
	"public_html/index.php":               "<?php require __DIR__ . '/wp-blog-header.php';",
}

const wordpressDump = "-- MariaDB dump 10.19  Distrib 10.11.6-MariaDB\nCREATE DATABASE `old`;\nUSE `old`;\nCREATE TABLE wp_options (option_name varchar(191));\n"

func importRequest(id string, adapt bool) CreateRequest {
	return CreateRequest{
		Name:     "Blog",
		PHP:      &PHPRequest{Version: "8.4", Config: runtime.DefaultPHPConfig()},
		Database: &DatabaseRequest{Type: "mariadb", Version: "11"},
		Import:   &ImportRequest{ID: id, AdaptConfig: adapt},
	}
}

func TestCreateFromImportUnpacksAdaptsAndImports(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var restored string
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		if container == "envoryx-blog-database" && cmd[0] == "mariadb" {
			restored = string(stdin)
		}
		return "", 0, nil
	}
	id := stageSite(t, e, wordpressSite, wordpressDump)
	st, _ := e.m.SiteImport(id)
	if st.Analysis.Framework.ID != "wordpress" || st.Analysis.Root != "public_html/" || st.Analysis.Database != "mariadb" {
		t.Fatalf("analysis: %+v", st.Analysis)
	}

	view, res, err := e.m.CreateFromImport(ctx, importRequest(id, true))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.Project.Lifecycle != store.LifecycleReady || view.Project.DesiredState != store.DesiredStopped {
		t.Fatalf("project: %+v", view.Project)
	}
	dir := filepath.Join(e.projDir, "blog")
	cfg, err := os.ReadFile(filepath.Join(dir, "wp-config.php"))
	if err != nil || !strings.Contains(string(cfg), "getenv('DB_DATABASE')") || strings.Contains(string(cfg), "old.example.com") {
		t.Fatalf("wp-config.php = %s (%v)", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wp-includes", "version.php")); err != nil {
		t.Fatalf("the site was not unpacked below the archive's folder: %v", err)
	}
	if !strings.Contains(restored, "CREATE TABLE wp_options") || strings.Contains(restored, "USE `old`") || strings.Contains(restored, "CREATE DATABASE") {
		t.Fatalf("imported dump = %q", restored)
	}
	// The database was started for the import only.
	if c, _ := e.engine.Container("envoryx-blog-database"); c.State == "running" {
		t.Fatal("the database container must be stopped again")
	}
	if res.Framework.ID != "wordpress" || !res.Database || len(res.Adapted.Changed) != 1 || len(res.Adapted.Originals) != 1 {
		t.Fatalf("result: %+v", res)
	}
	if _, err := e.m.SiteImport(id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the upload must be gone after the import, got %v", err)
	}
}

func TestCreateFromImportWithoutAdapting(t *testing.T) {
	e := newEnv(t)
	id := stageSite(t, e, wordpressSite, "")
	req := importRequest(id, false)
	req.Database = nil
	view, _, err := e.m.CreateFromImport(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// WordPress needs mysqli; it comes on top of the extensions the request chose.
	if php := view.Project.Service(store.ServicePHP); php == nil || !strings.Contains(string(php.Config), `"mysqli"`) || !strings.Contains(string(php.Config), `"gd"`) {
		t.Fatalf("php service: %+v", php)
	}
	cfg, _ := os.ReadFile(filepath.Join(e.projDir, "blog", "wp-config.php"))
	if string(cfg) != wordpressSite["public_html/wp-config.php"] {
		t.Fatalf("wp-config.php changed: %s", cfg)
	}
}

func TestCreateFromImportRollsBackAndKeepsTheUpload(t *testing.T) {
	e := newEnv(t)
	e.engine.StreamHandler = func(container string, cmd []string, env []string, stdin []byte) (string, int, error) {
		if cmd[0] == "mariadb" {
			return "ERROR 1064 (42000) at line 1: You have an error in your SQL syntax", 1, nil
		}
		return "", 0, nil
	}
	id := stageSite(t, e, wordpressSite, wordpressDump)
	_, _, err := e.m.CreateFromImport(context.Background(), importRequest(id, true))
	if err == nil || !strings.Contains(err.Error(), "could not be imported (exit 1)") {
		t.Fatalf("want the import error, got %v", err)
	}
	if list, _ := e.m.List(context.Background()); len(list) != 0 {
		t.Fatalf("the project must be rolled back: %+v", list)
	}
	if len(e.engine.ContainerNames()) != 0 {
		t.Fatalf("containers left: %v", e.engine.ContainerNames())
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "blog")); !os.IsNotExist(err) {
		t.Fatalf("the unpacked files must go with the project: %v", err)
	}
	// The upload stays for a second attempt.
	if _, err := e.m.SiteImport(id); err != nil {
		t.Fatalf("upload gone: %v", err)
	}
}

func TestCreateFromImportValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := stageSite(t, e, wordpressSite, wordpressDump)

	noDB := importRequest(id, true)
	noDB.Database = nil
	if _, _, err := e.m.CreateFromImport(ctx, noDB); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "needs a database") {
		t.Fatalf("dump without database: %v", err)
	}
	pg := importRequest(id, true)
	pg.Database = &DatabaseRequest{Type: "postgresql"}
	if _, _, err := e.m.CreateFromImport(ctx, pg); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("mariadb dump into postgresql: %v", err)
	}
	tpl := importRequest(id, true)
	tpl.Template = "wordpress"
	if _, _, err := e.m.CreateFromImport(ctx, tpl); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("template and upload: %v", err)
	}
	if _, _, err := e.m.CreateFromImport(ctx, importRequest("00000000-0000-4000-8000-000000000000", true)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown upload: %v", err)
	}

	// A directory with files in it is not overwritten.
	_ = os.MkdirAll(filepath.Join(e.projDir, "blog"), 0o755)
	_ = os.WriteFile(filepath.Join(e.projDir, "blog", "keep.txt"), []byte("mine"), 0o644)
	if _, _, err := e.m.CreateFromImport(ctx, importRequest(id, true)); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-empty directory: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.projDir, "blog", "keep.txt")); string(b) != "mine" {
		t.Fatal("an existing file was touched")
	}
}
