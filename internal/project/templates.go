package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/runtime"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Template scaffolds a fresh application into an empty project directory. Steps are
// argv commands run in a transient container from the image of the runtime the template
// names (PHP, Node or Python) as the project owner, exactly like git operations; nothing
// is interpolated from user input.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Runtime is the service the template needs and runs in: "php", "node" or "python".
	Runtime string `json:"runtime"`
	// Node carries dev-server defaults merged field by field into the request's NodeConfig
	// (preset, port and script where empty; DevServer when the request set none of them).
	Node *runtime.NodeConfig `json:"node,omitempty"`
	// Python carries server defaults merged the same way into the request's PythonConfig
	// (preset, port and app where empty; Server when the request set none of them).
	Python *runtime.PythonConfig `json:"python,omitempty"`
	// Docroot the template expects (applied when the request leaves it empty).
	Docroot string `json:"docroot"`
	// RequiresDatabase refuses creation without a database service.
	RequiresDatabase bool `json:"requiresDatabase"`
	// RecommendedDatabase is preselected in the wizard.
	RecommendedDatabase string `json:"recommendedDatabase,omitempty"`
	// PHPExtensions are enabled in addition to the defaults.
	PHPExtensions []string `json:"phpExtensions,omitempty"`
	// Notes are shown after creation (next steps).
	Notes string `json:"notes,omitempty"`

	steps []templateStep
}

type templateStep struct {
	label string
	cmd   []string
	// files are written by Envoryx after the command (relative path → content generator).
	files map[string]func() (string, error)
}

const composerNoInteraction = "--no-interaction"

// Environments of the scaffold one-shots. Composer and npm must never prompt: the
// container has no TTY and nobody answers.
var (
	composerScaffoldEnv = []string{"HOME=/tmp", "COMPOSER_HOME=/tmp/composer", "COMPOSER_NO_INTERACTION=1", "COMPOSER_MEMORY_LIMIT=-1"}
	nodeScaffoldEnv     = []string{"HOME=/tmp", "npm_config_cache=/tmp/.npm", "npm_config_yes=true", "CI=1", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "NPM_CONFIG_UPDATE_NOTIFIER=false"}
	// Python scaffolds create the project's .venv first; the steps after it run through
	// its bin/ (PATH), so pip installs into the venv, never into the image.
	pythonScaffoldEnv = []string{"HOME=/tmp", "PIP_CACHE_DIR=/tmp/.pip", "PIP_DISABLE_PIP_VERSION_CHECK=1", "PYTHONUNBUFFERED=1", "VIRTUAL_ENV=" + pythonVenvPath, "PATH=" + pythonVenvPath + "/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"}
)

// Python scaffold sources. Each is a fixed file written by Envoryx; the Django settings
// patch runs as a Python one-liner from the venv so nothing depends on sed's dialect.
const (
	// djangoSettingsPatch makes the generated settings work behind Envoryx's proxy: every
	// host name is allowed (the proxy decides), TLS termination is trusted and the
	// injected DATABASE_URL is used when present (dj-database-url).
	djangoSettingsPatch = `
import pathlib
p = pathlib.Path("config/settings.py")
s = p.read_text()
s = s.replace("ALLOWED_HOSTS = []", 'ALLOWED_HOSTS = ["*"]')
s += """

# --- Added by Envoryx: development behind the Envoryx proxy ---
import os
import dj_database_url

SECURE_PROXY_SSL_HEADER = ("HTTP_X_FORWARDED_PROTO", "https")
USE_X_FORWARDED_HOST = True
# Envoryx sets DJANGO_DEBUG=0 when the runtime runs in production mode.
DEBUG = os.environ.get("DJANGO_DEBUG", "1") == "1"
# Only needed for cross-origin posts (e.g. from the dev-server host to the project
# host); add the origins as a project environment variable, comma separated.
CSRF_TRUSTED_ORIGINS = [o for o in os.environ.get("CSRF_TRUSTED_ORIGINS", "").split(",") if o]
if os.environ.get("DATABASE_URL"):
    DATABASES["default"] = dj_database_url.config(conn_max_age=60)
"""
p.write_text(s)
`
	flaskApp = `from flask import Flask

app = Flask(__name__)


@app.get("/")
def index():
    return "<h1>Flask on Envoryx</h1><p>Edit app.py – the dev server reloads on save.</p>"
`
	fastapiApp = `from fastapi import FastAPI

app = FastAPI(title="Envoryx")


@app.get("/")
def index():
    return {"message": "FastAPI on Envoryx – edit main.py, uvicorn reloads on save. Docs at /docs."}
`
)

// nodeScaffoldMount is where Node scaffolds see the project directory. create-next-app
// refuses to run unless the parent of the target directory is writable, and /var/www is
// owned by root in the image; /tmp is world-writable. Mounting under the slug also gives
// create-vite and create-next-app (which name the package after the directory) a sensible
// package name instead of "html".
func nodeScaffoldMount(slug string) string { return "/tmp/" + slug }

// wordpressInstall downloads and unpacks the latest WordPress with PHP only (phar +
// openssl are built into every PHP image, so no zip/curl binaries are needed).
const wordpressInstall = `
$tmp = sys_get_temp_dir() . '/envoryx-wp-' . getmypid();
mkdir($tmp, 0700, true);
$tgz = "$tmp/wordpress.tar.gz";
if (!copy('https://wordpress.org/latest.tar.gz', $tgz)) { fwrite(STDERR, "download failed\n"); exit(1); }
$p = new PharData($tgz);
$p->decompress();
$t = new PharData("$tmp/wordpress.tar");
$t->extractTo($tmp, null, true);
$src = "$tmp/wordpress";
$it = new RecursiveIteratorIterator(new RecursiveDirectoryIterator($src, FilesystemIterator::SKIP_DOTS), RecursiveIteratorIterator::SELF_FIRST);
foreach ($it as $item) {
  $dest = getcwd() . '/' . substr($item->getPathname(), strlen($src) + 1);
  if ($item->isDir()) { if (!is_dir($dest)) mkdir($dest, 0755, true); }
  else { copy($item->getPathname(), $dest); }
}
echo "WordPress unpacked\n";
`

var templates = []Template{
	{
		ID: "laravel", Name: "Laravel", Description: "composer create-project laravel/laravel – ready to run with the project database.",
		Runtime: "php", Docroot: "public", RecommendedDatabase: "mariadb",
		Notes: "Run “artisan migrate” from the Actions tab. Envoryx injects DB_* and REDIS_*/MAIL_* variables; they override .env.",
		steps: []templateStep{{label: "composer create-project", cmd: []string{"composer", "create-project", "laravel/laravel", ".", composerNoInteraction, "--prefer-dist"}}},
	},
	{
		ID: "symfony", Name: "Symfony", Description: "symfony/skeleton plus the webapp pack (Twig, Doctrine, forms, security…).",
		Runtime: "php", Docroot: "public", RecommendedDatabase: "postgresql",
		Notes: "DATABASE_URL is injected by Envoryx. Create the schema with “doctrine:migrations:migrate” (Symfony console action).",
		steps: []templateStep{
			{label: "composer create-project", cmd: []string{"composer", "create-project", "symfony/skeleton", ".", composerNoInteraction, "--prefer-dist"}},
			{label: "composer require webapp", cmd: []string{"composer", "require", "webapp", composerNoInteraction}},
		},
	},
	{
		ID: "wordpress", Name: "WordPress", Description: "Latest WordPress with a generated wp-config.php wired to the project database.",
		Runtime: "php", Docroot: "", RequiresDatabase: true, RecommendedDatabase: "mariadb", PHPExtensions: []string{"mysqli"},
		Notes: "Open the site to run the WordPress installer (site title, admin account).",
		steps: []templateStep{{label: "download WordPress", cmd: []string{"php", "-r", wordpressInstall}, files: map[string]func() (string, error){"wp-config.php": wordpressConfig}}},
	},
	// The Node scaffolds are verified non-interactively against the Envoryx Node image
	// (DEVELOPMENT.md, "scaffold smoke"): create-vite honours --template with CI=1,
	// create-next-app --yes skips every prompt and installs, nuxi (citty) needs
	// --template when no TTY answers and --no-gitInit (never --gitInit false).
	{
		ID: "vite", Name: "Vite + React (TypeScript)", Description: "Vite scaffold with React and TypeScript – dev server with HMR on the project URL.",
		Runtime: "node", Docroot: "dist",
		Node:  &runtime.NodeConfig{DevServer: true, Preset: "vite", Port: 5173, Script: "dev"},
		Notes: "The dev server answers on the project URL. Turn the dev server off and run “npm run build” to serve dist/ statically (enable the SPA fallback for client-side routing).",
		steps: []templateStep{
			{label: "npm create vite", cmd: []string{"npm", "create", "vite@latest", ".", "--", "--template", "react-ts"}},
			{label: "npm install", cmd: []string{"npm", "install"}},
		},
	},
	{
		ID: "next", Name: "Next.js (App Router, TypeScript)", Description: "create-next-app with the App Router and TypeScript.",
		Runtime: "node", Docroot: "",
		Node:  &runtime.NodeConfig{DevServer: true, Preset: "next", Port: 3000, Script: "dev"},
		Notes: "create-next-app installs dependencies. For a production-like run: “npm run build”, then set the dev-server script to “start”.",
		steps: []templateStep{{label: "create-next-app", cmd: []string{"npx", "--yes", "create-next-app@latest", ".", "--yes", "--ts", "--app", "--use-npm", "--disable-git"}}},
	},
	{
		ID: "nuxt", Name: "Nuxt", Description: "Nuxt starter (minimal template) created by nuxi.",
		Runtime: "node", Docroot: "",
		Node:  &runtime.NodeConfig{DevServer: true, Preset: "nuxt", Port: 3000, Script: "dev"},
		Notes: "For a production-like run: “npm run build”, then set the dev-server script to “preview”.",
		steps: []templateStep{
			{label: "nuxi init", cmd: []string{"npx", "--yes", "nuxi@latest", "init", ".", "--template", "minimal", "--packageManager", "npm", "--no-install", "--no-gitInit", "--force"}},
			{label: "npm install", cmd: []string{"npm", "install"}},
		},
	},
	// The Python scaffolds run from the Envoryx Python image: the venv is created in
	// the project directory, requirements are pinned with pip freeze so the Actions tab's
	// "pip install" reproduces it after a fresh clone.
	{
		ID: "django", Name: "Django", Description: "django-admin startproject with settings prepared for the Envoryx proxy and the project database (dj-database-url).",
		Runtime: "python", Docroot: "", RecommendedDatabase: "postgresql",
		Python: &runtime.PythonConfig{Server: true, Preset: "django", Port: 8000, App: "config.wsgi:application"},
		Notes:  "Run “manage.py migrate” from the Actions tab, then open the site. DATABASE_URL is injected by Envoryx and picked up by settings.py; without a database Django uses SQLite. settings.py ties DEBUG to the runtime mode (DJANGO_DEBUG) and reads CSRF_TRUSTED_ORIGINS from the environment if you need cross-origin posts.",
		steps: []templateStep{
			{label: "python -m venv", cmd: []string{"python", "-m", "venv", pythonVenvPath}},
			{label: "pip install django", cmd: []string{"pip", "install", "django", "dj-database-url", "gunicorn", "psycopg[binary]", "mysqlclient"}},
			{label: "django-admin startproject", cmd: []string{"django-admin", "startproject", "config", "."}},
			{label: "prepare settings", cmd: []string{"python", "-c", djangoSettingsPatch}},
			{label: "pip freeze", cmd: []string{"sh", "-c", "pip freeze > requirements.txt", "envoryx-freeze"}},
		},
	},
	{
		ID: "flask", Name: "Flask", Description: "A minimal Flask application (app.py) – flask run with the debugger and reloader on the project URL.",
		Runtime: "python", Docroot: "",
		Python: &runtime.PythonConfig{Server: true, Preset: "flask", Port: 5000, App: "app:app"},
		Notes:  "The dev server answers on the project URL. For a production-like run switch the mode to production (gunicorn is installed).",
		steps: []templateStep{
			{label: "python -m venv", cmd: []string{"python", "-m", "venv", pythonVenvPath}},
			{label: "pip install flask", cmd: []string{"pip", "install", "flask", "gunicorn", "python-dotenv"}},
			{label: "pip freeze, write app.py", cmd: []string{"sh", "-c", "pip freeze > requirements.txt", "envoryx-freeze"}, files: map[string]func() (string, error){"app.py": func() (string, error) { return flaskApp, nil }}},
		},
	},
	{
		ID: "fastapi", Name: "FastAPI", Description: "A minimal FastAPI application (main.py) served by uvicorn with reload – interactive docs at /docs.",
		Runtime: "python", Docroot: "",
		Python: &runtime.PythonConfig{Server: true, Preset: "asgi", Port: 8000, App: "main:app"},
		Notes:  "uvicorn answers on the project URL; the OpenAPI docs are at /docs.",
		steps: []templateStep{
			{label: "python -m venv", cmd: []string{"python", "-m", "venv", pythonVenvPath}},
			{label: "pip install fastapi", cmd: []string{"pip", "install", "fastapi", "uvicorn[standard]"}},
			{label: "pip freeze, write main.py", cmd: []string{"sh", "-c", "pip freeze > requirements.txt", "envoryx-freeze"}, files: map[string]func() (string, error){"main.py": func() (string, error) { return fastapiApp, nil }}},
		},
	},
}

// Templates lists the available project templates.
func Templates() []Template {
	out := make([]Template, len(templates))
	copy(out, templates)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TemplateByID finds a template.
func TemplateByID(id string) (Template, bool) {
	for _, t := range templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// wordpressConfig renders wp-config.php reading the injected DB_* variables at runtime.
func wordpressConfig() (string, error) {
	salts := make([]string, 8)
	for i := range salts {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		salts[i] = hex.EncodeToString(b)
	}
	names := []string{"AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY", "AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT"}
	var b strings.Builder
	b.WriteString("<?php\n// Generated by Envoryx. Database settings come from the environment Envoryx injects.\n")
	b.WriteString("define('DB_NAME', getenv('DB_DATABASE') ?: 'wordpress');\n")
	b.WriteString("define('DB_USER', getenv('DB_USERNAME') ?: 'wordpress');\n")
	b.WriteString("define('DB_PASSWORD', getenv('DB_PASSWORD') ?: '');\n")
	b.WriteString("define('DB_HOST', (getenv('DB_HOST') ?: 'database') . ':' . (getenv('DB_PORT') ?: '3306'));\n")
	b.WriteString("define('DB_CHARSET', 'utf8mb4');\ndefine('DB_COLLATE', '');\n\n")
	for i, n := range names {
		fmt.Fprintf(&b, "define('%s', '%s');\n", n, salts[i])
	}
	b.WriteString("\n$table_prefix = 'wp_';\ndefine('WP_DEBUG', true);\ndefine('WP_DEBUG_DISPLAY', true);\ndefine('FS_METHOD', 'direct');\n")
	b.WriteString("// Behind Envoryx's proxy the TLS connection is terminated upstream.\n")
	b.WriteString("if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') { $_SERVER['HTTPS'] = 'on'; }\n")
	b.WriteString("if (!defined('ABSPATH')) { define('ABSPATH', __DIR__ . '/'); }\nrequire_once ABSPATH . 'wp-settings.php';\n")
	return b.String(), nil
}

// applyTemplate scaffolds the template into the (empty) project directory, running the
// steps from the image of the runtime the template names.
func (m *Manager) applyTemplate(ctx context.Context, proj store.Project, tpl Template) error {
	kind, label, env, mount := store.ServicePHP, "PHP", composerScaffoldEnv, appMountTarget
	switch tpl.Runtime {
	case "node":
		kind, label, env, mount = store.ServiceNode, "Node", nodeScaffoldEnv, nodeScaffoldMount(proj.Slug)
	case "python":
		kind, label, env = store.ServicePython, "Python", pythonScaffoldEnv
	}
	svc := proj.Service(kind)
	if svc == nil {
		return fmt.Errorf("%w: template %s needs a %s service", validate.ErrInvalid, tpl.ID, label)
	}
	if tpl.RequiresDatabase && proj.Service(store.ServiceDatabase) == nil {
		return fmt.Errorf("%w: template %s needs a database service", validate.ErrInvalid, tpl.ID)
	}
	paths, err := m.paths()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	planner := NewPlanner(paths, m.catalog)
	dir := planner.ProjectDir(proj)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: the project directory is not empty; templates need an empty directory", ErrConflict)
	}
	if err := m.engine.EnsureImage(ctx, svc.Image, m.pullProgress(ctx, proj.Slug, svc.Image)); err != nil {
		return err
	}
	for i, ts := range tpl.steps {
		step(ctx, "Scaffolding the {{template}} template: {{step}}", "template", tpl.Name, "step", ts.label)
		spec := docker.ContainerSpec{
			Name:       fmt.Sprintf("envoryx-%s-template-%d-%d", proj.Slug, i, time.Now().UnixNano()%1_000_000),
			Image:      svc.Image,
			Labels:     docker.ManagedLabels(proj.ID, proj.Slug, "template", paths.EnvoryxVersion),
			Env:        env,
			Cmd:        ts.cmd,
			WorkingDir: mount,
			User:       fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
			Mounts:     []docker.MountSpec{{Type: "bind", Source: planner.projectHostDir(proj), Target: mount}},
			// Composer/npm downloads need DNS/internet: default bridge network.
			RestartPolicy: "no",
		}
		res, err := m.engine.RunOneShot(ctx, spec)
		if err != nil {
			return fmt.Errorf("template %s (%s): %w", tpl.ID, ts.label, err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("%w: template %s failed at %q: %s", ErrConflict, tpl.ID, ts.label, lastLine(res.Stdout+"\n"+res.Stderr))
		}
		for rel, gen := range ts.files {
			content, err := gen()
			if err != nil {
				return err
			}
			path, err := validate.ResolveUnder(dir, rel)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", rel, err)
			}
			_ = os.Chown(path, paths.PUID, paths.PGID)
		}
	}
	chownTree(dir, paths.PUID, paths.PGID)
	m.audit.Log(ctx, audit.ActionProjectUpdated, "project", proj.ID, map[string]any{"name": proj.Name, "template": tpl.ID})
	return nil
}
