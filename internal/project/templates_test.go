package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

func TestTemplatesScaffoldThroughOneShotContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var runs []docker.ContainerSpec
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		runs = append(runs, spec)
		if spec.Cmd[0] == "composer" {
			// Simulate create-project output.
			dir := filepath.Join(e.projDir, strings.TrimPrefix(spec.Name, "envoryx-")[:strings.Index(strings.TrimPrefix(spec.Name, "envoryx-"), "-template")])
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
	if len(runs) != 1 || strings.Join(runs[0].Cmd, " ") != "composer create-project laravel/laravel . --no-interaction --prefer-dist" || !runsAsProjectUser(runs[0], 1000, 1000) || runs[0].Labels["envoryx.service"] != "template" {
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

func TestNodeTemplatesScaffoldFromTheNodeImage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var runs []docker.ContainerSpec
	e.engine.OneShotHandler = func(spec docker.ContainerSpec) (docker.ExecResult, error) {
		runs = append(runs, spec)
		return docker.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
	}

	// Vite: two steps from the Node image, dev-server defaults from the template.
	v, err := e.m.Create(ctx, CreateRequest{Name: "Shop", Template: "vite", Node: &NodeRequest{Version: "24"}, CreateStarter: true})
	if err != nil {
		t.Fatal(err)
	}
	if v.Project.Docroot != "dist" {
		t.Fatalf("docroot: %q", v.Project.Docroot)
	}
	if len(runs) != 2 {
		t.Fatalf("vite steps: %+v", runs)
	}
	if got := strings.Join(runs[0].Cmd, " "); got != "npm create vite@latest . -- --template react-ts" {
		t.Fatalf("create step: %s", got)
	}
	if got := strings.Join(runs[1].Cmd, " "); got != "npm install" {
		t.Fatalf("install step: %s", got)
	}
	for _, r := range runs {
		if r.Image != "ghcr.io/envoryx/envoryx-node:24" || !runsAsProjectUser(r, 1000, 1000) || r.Labels["envoryx.service"] != "template" || r.RestartPolicy != "no" {
			t.Fatalf("scaffold container: %+v", r)
		}
		// create-next-app needs a writable parent directory; /var/www is root-owned.
		if r.WorkingDir != "/tmp/shop" || len(r.Mounts) != 1 || r.Mounts[0].Target != "/tmp/shop" || r.Mounts[0].Source != "/host/development/shop" {
			t.Fatalf("scaffold mount: %+v", r)
		}
		env := strings.Join(r.Env, "\n")
		for _, want := range []string{"HOME=/tmp", "npm_config_cache=/tmp/.npm", "npm_config_yes=true", "CI=1", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0"} {
			if !strings.Contains(env, want) {
				t.Fatalf("scaffold env lacks %s: %s", want, env)
			}
		}
		if strings.Contains(env, "COMPOSER") {
			t.Fatalf("composer env on a node scaffold: %s", env)
		}
	}
	node := v.Project.Service(store.ServiceNode)
	if node == nil {
		t.Fatal("node service missing")
	}
	var cfg runtime.NodeConfig
	if err := json.Unmarshal(node.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.DevServer || cfg.Preset != "vite" || cfg.Port != 5173 || cfg.Script != "dev" {
		t.Fatalf("stored node config: %+v", cfg)
	}
	if v.Project.Service(store.ServicePHP) != nil {
		t.Fatal("a node template must not add PHP")
	}
	if _, err := os.Stat(filepath.Join(e.projDir, "shop", "dist", "index.html")); err == nil {
		t.Fatal("starter page must not be written when a template scaffolds")
	}

	// A request-provided dev-server config wins over the template defaults.
	runs = nil
	v, err = e.m.Create(ctx, CreateRequest{Name: "Custom", Template: "next", Node: &NodeRequest{Version: "24", Config: runtime.NodeConfig{DevServer: true, Preset: "next", Port: 4000}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(v.Project.Service(store.ServiceNode).Config, &cfg)
	if cfg.Port != 4000 || cfg.Preset != "next" {
		t.Fatalf("request port must win: %+v", cfg)
	}
	if len(runs) != 1 || strings.Join(runs[0].Cmd, " ") != "npx --yes create-next-app@latest . --yes --ts --app --use-npm --disable-git" {
		t.Fatalf("next step: %+v", runs)
	}

	// Nuxt: nuxi must not prompt (no TTY answers) and leaves the install to the next step.
	runs = nil
	if _, err := e.m.Create(ctx, CreateRequest{Name: "Nux", Template: "nuxt", Node: &NodeRequest{Version: "24"}}); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || strings.Join(runs[0].Cmd, " ") != "npx --yes nuxi@latest init . --template minimal --packageManager npm --no-install --no-gitInit --force" || strings.Join(runs[1].Cmd, " ") != "npm install" {
		t.Fatalf("nuxt steps: %+v", runs)
	}

	// Template/runtime mismatches are refused before anything is created.
	runs = nil
	if _, err := e.m.Create(ctx, CreateRequest{Name: "Lara", Template: "laravel", Node: &NodeRequest{Version: "24"}}); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "needs PHP") {
		t.Fatalf("laravel without PHP: %v", err)
	}
	if _, err := e.m.Create(ctx, CreateRequest{Name: "Nextless", Template: "next", PHP: &PHPRequest{Version: "8.4"}}); !errors.Is(err, validate.ErrInvalid) || !strings.Contains(err.Error(), "needs Node.js") {
		t.Fatalf("next without Node: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("no scaffold may run for a refused template: %+v", runs)
	}
	if views, _ := e.m.List(ctx); len(views) != 3 {
		t.Fatalf("projects: %d", len(views))
	}
}

func TestTemplatesCarryTheirRuntime(t *testing.T) {
	b, err := json.Marshal(Templates())
	if err != nil {
		t.Fatal(err)
	}
	var out []struct {
		ID      string `json:"id"`
		Runtime string `json:"runtime"`
		Node    *struct {
			DevServer bool   `json:"devServer"`
			Preset    string `json:"preset"`
			Port      int    `json:"port"`
			Script    string `json:"script"`
		} `json:"node"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, tpl := range out {
		byID[tpl.ID] = i
	}
	for _, id := range []string{"laravel", "symfony", "wordpress"} {
		if tpl := out[byID[id]]; tpl.Runtime != "php" || tpl.Node != nil {
			t.Errorf("%s: %+v", id, tpl)
		}
	}
	for id, port := range map[string]int{"vite": 5173, "next": 3000, "nuxt": 3000} {
		tpl := out[byID[id]]
		if tpl.Runtime != "node" || tpl.Node == nil || !tpl.Node.DevServer || tpl.Node.Preset != id || tpl.Node.Port != port || tpl.Node.Script != "dev" {
			t.Errorf("%s: %+v", id, tpl)
		}
	}
	// Names stay sorted.
	for i := 1; i < len(out); i++ {
		if Templates()[i-1].Name > Templates()[i].Name {
			t.Fatalf("templates not sorted by name at %d", i)
		}
	}
}

// runsAsProjectUser reports whether a one-shot starts as root and drops to uid:gid after
// writing the passwd entry (runAsProjectUser).
func runsAsProjectUser(spec docker.ContainerSpec, uid, gid int) bool {
	if spec.User != "0:0" || len(spec.Entrypoint) != 4 {
		return false
	}
	script := spec.Entrypoint[2]
	return strings.Contains(script, fmt.Sprintf(`getent passwd %d `, uid)) &&
		strings.Contains(script, fmt.Sprintf(`exec setpriv --reuid=%d --regid=%d --clear-groups -- "$@"`, uid, gid))
}

func TestRunAsProjectUserLeavesRootAlone(t *testing.T) {
	spec := docker.ContainerSpec{Cmd: []string{"git", "status"}}
	runAsProjectUser(&spec, 0, 0)
	if spec.User != "0:0" || spec.Entrypoint != nil || strings.Join(spec.Cmd, " ") != "git status" {
		t.Fatalf("root one-shot changed: %+v", spec)
	}
	runAsProjectUser(&spec, 99, 100)
	if !runsAsProjectUser(spec, 99, 100) || strings.Join(spec.Cmd, " ") != "git status" {
		t.Fatalf("unraid one-shot: %+v", spec)
	}
}

func TestFailureLineFindsTheCause(t *testing.T) {
	for name, tc := range map[string]struct{ out, want string }{
		"node crash": {
			out:  "node:os:306\n    throw new ERR_SYSTEM_ERROR(ctx);\n    ^\n\nSystemError [ERR_SYSTEM_ERROR]: A system error occurred: uv_os_get_passwd returned ENOENT (no such file or directory)\n    at Object.userInfo (node:os:306:11)\n  code: 'ERR_SYSTEM_ERROR',\n  info: {\n    errno: -2,\n  }\n}\n\nNode.js v24.21.0\n",
			want: "SystemError [ERR_SYSTEM_ERROR]: A system error occurred: uv_os_get_passwd returned ENOENT (no such file or directory)",
		},
		"npm": {
			out:  "\x1b[31mnpm error\x1b[39m code E404\nnpm error 404 Not Found - GET https://registry.npmjs.org/create-nope - Not found\nnpm error A complete log of this run can be found in: /tmp/.npm/_logs/x.log\n",
			want: "npm error 404 Not Found - GET https://registry.npmjs.org/create-nope - Not found",
		},
		"python": {
			out:  "Traceback (most recent call last):\n  File \"x.py\", line 1\nModuleNotFoundError: No module named 'django'\n",
			want: "ModuleNotFoundError: No module named 'django'",
		},
		"fallback": {out: "Installing...\n  Could not find package foo/bar.\n\n", want: "Could not find package foo/bar."},
	} {
		if got := failureLine(tc.out); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
