package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seramos/staqio/internal/docker"
	"github.com/seramos/staqio/internal/runtime"
	"github.com/seramos/staqio/internal/validate"
)

func TestTemplatesScaffoldThroughOneShotContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var runs []docker.ContainerSpec
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		runs = append(runs, spec)
		if spec.Cmd[0] == "composer" {
			// Simulate create-project output.
			dir := filepath.Join(e.projDir, strings.TrimPrefix(spec.Name, "staqio-")[:strings.Index(strings.TrimPrefix(spec.Name, "staqio-"), "-template")])
			_ = os.MkdirAll(filepath.Join(dir, "public"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{}"), 0o644)
		}
		return docker.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
	}

	// Laravel: docroot defaults to public, composer create-project runs as the project owner.
	req := CreateRequest{Name: "Shop", Template: "laravel", PHP: &PHPRequest{Version: "8.4"}, Database: &DatabaseRequest{Type: "mariadb"}, CreateStarter: true, Start: false}
	v, err := e.m.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if v.Project.Docroot != "public" {
		t.Fatalf("docroot: %q", v.Project.Docroot)
	}
	if len(runs) != 1 || strings.Join(runs[0].Cmd, " ") != "composer create-project laravel/laravel . --no-interaction --prefer-dist" || runs[0].User != "1000:1000" || runs[0].Labels["staqio.service"] != "template" {
		t.Fatalf("laravel step: %+v", runs)
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop", "public", "index.php")); err == nil {
		t.Fatal("starter page must not be written when a template scaffolds")
	}

	// WordPress: needs a database and mysqli, writes wp-config.php.
	runs = nil
	_, err = e.m.Create(ctx, CreateRequest{Name: "Blog", Template: "wordpress", PHP: &PHPRequest{Version: "8.4"}})
	if !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("wordpress without database must be rejected: %v", err)
	}
	wp, err := e.m.Create(ctx, CreateRequest{Name: "Blog", Template: "wordpress", PHP: &PHPRequest{Version: "8.4", Config: runtime.DefaultPHPConfig()}, Database: &DatabaseRequest{Type: "mariadb"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Cmd[0] != "php" || runs[0].Cmd[1] != "-r" {
		t.Fatalf("wordpress step: %+v", runs)
	}
	cfg, err := os.ReadFile(filepath.Join(e.projDir, "blog", "wp-config.php"))
	if err != nil || !strings.Contains(string(cfg), "getenv('DB_DATABASE')") || !strings.Contains(string(cfg), "AUTH_KEY") {
		t.Fatalf("wp-config: %v %s", err, cfg)
	}
	var exts string
	for _, s := range wp.Project.Services {
		if s.Kind == "php" {
			exts = string(s.Config)
		}
	}
	if !strings.Contains(exts, "mysqli") || !strings.Contains(exts, "pdo_mysql") {
		t.Fatalf("template extensions must be added to the defaults: %s", exts)
	}

	// Failures roll the project back.
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		return docker.ExecResult{ExitCode: 1, Stderr: "Could not find package"}, nil
	}
	if _, err := e.m.Create(ctx, CreateRequest{Name: "Broken", Template: "symfony", PHP: &PHPRequest{Version: "8.4"}}); err == nil || !strings.Contains(err.Error(), "Could not find package") {
		t.Fatalf("template failure must surface: %v", err)
	}
	if views, _ := e.m.List(ctx); len(views) != 2 {
		t.Fatalf("failed project must be rolled back, have %d", len(views))
	}
	if _, err := e.m.Create(ctx, CreateRequest{Name: "X", Template: "nope", PHP: &PHPRequest{Version: "8.4"}}); !errors.Is(err, validate.ErrInvalid) {
		t.Fatalf("unknown template: %v", err)
	}
}
