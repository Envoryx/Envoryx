package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	// Runtime is the service the template needs and runs in: "php", "node", "python", "go"
	// or "ruby".
	Runtime string `json:"runtime"`
	// Node carries dev-server defaults merged field by field into the request's NodeConfig
	// (preset, port and script where empty; DevServer when the request set none of them).
	Node *runtime.NodeConfig `json:"node,omitempty"`
	// Python carries server defaults merged the same way into the request's PythonConfig
	// (preset, port and app where empty; Server when the request set none of them).
	Python *runtime.PythonConfig `json:"python,omitempty"`
	// Go carries server defaults merged the same way into the request's GoConfig
	// (package and port where empty; Server when the request set none of them).
	Go *runtime.GoConfig `json:"go,omitempty"`
	// Ruby carries server defaults merged the same way into the request's RubyConfig
	// (preset and port where empty; Server when the request set none of them).
	Ruby *runtime.RubyConfig `json:"ruby,omitempty"`
	// Docroot the template expects (applied when the request leaves it empty).
	Docroot string `json:"docroot"`
	// RequiresDatabase refuses creation without a database service.
	RequiresDatabase bool `json:"requiresDatabase"`
	// RecommendedDatabase is preselected in the wizard.
	RecommendedDatabase string `json:"recommendedDatabase,omitempty"`
	// PHPExtensions are enabled in addition to the defaults.
	PHPExtensions []string `json:"phpExtensions,omitempty"`
	// PHPMemoryLimit raises memory_limit where the application needs more than the
	// default (Shopware); a limit the request set itself stays.
	PHPMemoryLimit string `json:"phpMemoryLimit,omitempty"`
	// Notes are shown after creation (next steps).
	Notes string `json:"notes,omitempty"`

	steps []templateStep
}

type templateStep struct {
	label string
	cmd   []string
	// cmdFor replaces cmd when the command depends on the project (its name, its database).
	cmdFor func(p store.Project) []string
	// files are written by Envoryx after the command (relative path → content generator).
	files map[string]func() (string, error)
}

const composerNoInteraction = "--no-interaction"

// Environments of the scaffold one-shots. Composer and npm must never prompt: the
// container has no TTY and nobody answers.
var (
	composerScaffoldEnv = []string{"HOME=/tmp", "COMPOSER_HOME=/tmp/composer", "COMPOSER_NO_INTERACTION=1", "COMPOSER_MEMORY_LIMIT=-1"}
	nodeScaffoldEnv     = []string{"HOME=/tmp", "npm_config_yes=true", "CI=1", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "NPM_CONFIG_UPDATE_NOTIFIER=false"}
	// Python scaffolds create the project's .venv first; the steps after it run through
	// its bin/ (PATH), so pip installs into the venv, never into the image.
	// The Go scaffolds use the shared module and build caches (withPackageCache) and a
	// throwaway GOPATH; the module is called "app" – a main module needs no import path.
	goScaffoldEnv     = []string{"HOME=/tmp", "GOPATH=/tmp/go", "GOFLAGS=-modcacherw", "PATH=/tmp/go/bin:/usr/local/go/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"}
	pythonScaffoldEnv = []string{"HOME=/tmp", "PIP_DISABLE_PIP_VERSION_CHECK=1", "PYTHONUNBUFFERED=1", "VIRTUAL_ENV=" + pythonVenvPath, "PATH=" + pythonVenvPath + "/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"}
	// The Ruby scaffolds install into the project's GEM_HOME (the project home is mounted),
	// so the server finds the bundle complete on its first start.
	rubyScaffoldEnv = append([]string{"HOME=" + homeMountTarget}, rubyEnv...)
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
	// goHTTPApp is the net/http template: the standard library only.
	goHTTPApp = `package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Hello from Go on Envoryx!")
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Envoryx injects HOST and PORT; the proxy sends the project URL here.
	addr := os.Getenv("HOST") + ":" + os.Getenv("PORT")
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
`
	goGinApp = `package main

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func main() {
	r := gin.Default()
	// The proxy in front of the project sets X-Forwarded-For.
	_ = r.SetTrustedProxies(nil)
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "Hello from Gin on Envoryx!"})
	})
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	// Envoryx injects HOST and PORT (and GIN_MODE, which follows the runtime mode).
	_ = r.Run(os.Getenv("HOST") + ":" + os.Getenv("PORT"))
}
`
	goEchoApp = `package main

import (
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	e := echo.New()
	e.Use(middleware.Logger(), middleware.Recover())
	e.GET("/", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"message": "Hello from Echo on Envoryx!"})
	})
	e.GET("/healthz", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

	// Envoryx injects HOST and PORT; the proxy sends the project URL here.
	e.Logger.Fatal(e.Start(os.Getenv("HOST") + ":" + os.Getenv("PORT")))
}
`

	sinatraGemfile = `source "https://rubygems.org"

gem "sinatra"
gem "sinatra-contrib"
gem "puma"

group :development do
  gem "debug", require: false
end
`
	sinatraConfigRu = `require_relative "app"

run App
`
	sinatraApp = `require "sinatra/base"
require "json"

class App < Sinatra::Base
  configure :development do
    require "sinatra/reloader"
    register Sinatra::Reloader
  end

  # The Envoryx proxy decides which host names reach the project.
  set :host_authorization, { permitted_hosts: [] }

  get "/" do
    content_type :json
    { message: "Hello from Sinatra on Envoryx!" }.to_json
  end

  get "/healthz" do
    204
  end
end
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
	// The CMS and shop templates: composer create-project, then what the application
	// needs to find the project database. Their installers need the database running,
	// so they run after the start – in the browser, or as an action.
	{
		ID: "drupal", Name: "Drupal", Description: "drupal/recommended-project with Drush and a settings.php wired to the project database.",
		Runtime: "php", Docroot: "web", RequiresDatabase: true, RecommendedDatabase: "mariadb",
		Notes: "Open the site to run the installer – the database is already set up – or run “drush site:install” from the Actions tab, which prints the admin password.",
		steps: []templateStep{
			{label: "composer create-project", cmd: []string{"composer", "create-project", "drupal/recommended-project", ".", composerNoInteraction, "--prefer-dist"}},
			{label: "composer require drush", cmd: []string{"composer", "require", "drush/drush", composerNoInteraction}},
			{label: "prepare settings.php", cmd: []string{"php", "-r", drupalSettings}},
		},
	},
	{
		ID: "typo3", Name: "TYPO3", Description: "typo3/cms-base-distribution with the installer enabled and the connection to the project database prepared.",
		Runtime: "php", Docroot: "public", RequiresDatabase: true, RecommendedDatabase: "mariadb", PHPExtensions: []string{"mysqli"},
		Notes: "Run “typo3 setup” from the Actions tab once the project is running: it creates the tables, a site for the project URL and the administrator, and prints the password. The web installer on the site works too; the connection comes from config/system/additional.php.",
		steps: []templateStep{
			{label: "composer create-project", cmd: []string{"composer", "create-project", "typo3/cms-base-distribution", ".", composerNoInteraction, "--prefer-dist"}},
			{label: "enable the installer", cmd: []string{"php", "-r", typo3Settings}},
		},
	},
	{
		ID: "shopware", Name: "Shopware", Description: "shopware/production – Shopware 6 with APP_URL following the project address.",
		Runtime: "php", Docroot: "public", RequiresDatabase: true, RecommendedDatabase: "mariadb", PHPMemoryLimit: "1G",
		Notes: "Run “Shopware system:install” from the Actions tab once the project is running: it creates the tables, a sales channel for the project URL and the administrator admin / shopware (change the password in the admin at /admin).",
		steps: []templateStep{
			{label: "composer create-project", cmd: []string{"composer", "create-project", "shopware/production", ".", composerNoInteraction, "--prefer-dist"}, files: map[string]func() (string, error){".env.local": func() (string, error) { return shopwareEnvLocal, nil }}},
		},
	},
	{
		ID: "craft", Name: "Craft CMS", Description: "craftcms/craft with the database connection and the site URL read from the project environment.",
		Runtime: "php", Docroot: "web", RequiresDatabase: true, RecommendedDatabase: "mariadb",
		Notes: "Run “craft install” from the Actions tab once the project is running (it prints the admin password), or open /admin/install on the site.",
		steps: []templateStep{
			{label: "composer create-project", cmd: []string{"composer", "create-project", "craftcms/craft", ".", composerNoInteraction, "--prefer-dist"}},
			{label: "wire the database", cmd: []string{"php", "-r", craftEnv}},
		},
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
	// The Go scaffolds run from the Envoryx Go image: go mod init, the program, then the
	// framework's module; air rebuilds it on the project URL.
	{
		ID: "go", Name: "Go (net/http)", Description: "A minimal Go web server on the standard library (main.go) – rebuilt by air on every change.",
		Runtime: "go", Docroot: "",
		Go:    &runtime.GoConfig{Server: true, Package: ".", Port: runtime.DefaultGoPort},
		Notes: "air rebuilds and restarts the server when a .go file changes. Add a .air.toml to configure it yourself; the Tests tab runs go test.",
		steps: []templateStep{
			{label: "go mod init, write main.go", cmd: []string{"go", "mod", "init", "app"}, files: map[string]func() (string, error){"main.go": func() (string, error) { return goHTTPApp, nil }}},
		},
	},
	{
		ID: "gin", Name: "Gin", Description: "A minimal Gin application (main.go) with a JSON route and a health check – rebuilt by air on every change.",
		Runtime: "go", Docroot: "",
		Go:    &runtime.GoConfig{Server: true, Package: ".", Port: runtime.DefaultGoPort},
		Notes: "GIN_MODE follows the runtime mode (debug in dev, release in production).",
		steps: []templateStep{
			{label: "go mod init, write main.go", cmd: []string{"go", "mod", "init", "app"}, files: map[string]func() (string, error){"main.go": func() (string, error) { return goGinApp, nil }}},
			{label: "go get gin", cmd: []string{"go", "get", "github.com/gin-gonic/gin"}},
			{label: "go mod tidy", cmd: []string{"go", "mod", "tidy"}},
		},
	},
	{
		ID: "echo", Name: "Echo", Description: "A minimal Echo application (main.go) with logging, recovery and a health check – rebuilt by air on every change.",
		Runtime: "go", Docroot: "",
		Go:    &runtime.GoConfig{Server: true, Package: ".", Port: runtime.DefaultGoPort},
		Notes: "air rebuilds and restarts the server when a .go file changes.",
		steps: []templateStep{
			{label: "go mod init, write main.go", cmd: []string{"go", "mod", "init", "app"}, files: map[string]func() (string, error){"main.go": func() (string, error) { return goEchoApp, nil }}},
			{label: "go get echo", cmd: []string{"go", "get", "github.com/labstack/echo/v4"}},
			{label: "go mod tidy", cmd: []string{"go", "mod", "tidy"}},
		},
	},
	// The Ruby scaffolds run from the Envoryx Ruby image with the project home mounted:
	// rails and the bundle land in the project's GEM_HOME.
	{
		ID: "rails", Name: "Rails", Description: "rails new with Hotwire and importmap (no Node.js needed), on the project database.",
		Runtime: "ruby", Docroot: "", RecommendedDatabase: "postgresql",
		Ruby:  &runtime.RubyConfig{Server: true, Preset: "rails", Port: 3000},
		Notes: "Run “rails db:prepare” from the Actions tab, then open the site. DATABASE_URL is injected by Envoryx and Rails merges it into config/database.yml; without a database Rails uses SQLite. Rails reloads code on every request in dev mode. Production mode needs config/master.key (rails new wrote one) and the assets built with “rails assets:precompile”.",
		steps: []templateStep{
			{label: "gem install rails, rails new", cmdFor: railsNew()},
		},
	},
	{
		ID: "rails-api", Name: "Rails (API only)", Description: "rails new --api: a JSON backend without views and assets, on the project database.",
		Runtime: "ruby", Docroot: "", RecommendedDatabase: "postgresql",
		Ruby:  &runtime.RubyConfig{Server: true, Preset: "rails", Port: 3000},
		Notes: "Run “rails db:prepare” from the Actions tab, then generate resources in the Ruby terminal (bin/rails generate scaffold …). DATABASE_URL is injected by Envoryx; without a database Rails uses SQLite.",
		steps: []templateStep{
			{label: "gem install rails, rails new --api", cmdFor: railsNew("--api")},
		},
	},
	{
		ID: "sinatra", Name: "Sinatra", Description: "A minimal Sinatra application (app.rb, config.ru) on Puma with a JSON route and a health check.",
		Runtime: "ruby", Docroot: "",
		Ruby:  &runtime.RubyConfig{Server: true, Preset: "rack", Port: 9292},
		Notes: "Sinatra::Reloader (sinatra-contrib) reloads app.rb in dev mode. The debug gem is in the Gemfile, so the rdbg switch works right away.",
		steps: []templateStep{
			{label: "write Gemfile, config.ru, app.rb", cmd: []string{"ruby", "--version"}, files: map[string]func() (string, error){
				"Gemfile":   func() (string, error) { return sinatraGemfile, nil },
				"config.ru": func() (string, error) { return sinatraConfigRu, nil },
				"app.rb":    func() (string, error) { return sinatraApp, nil },
			}},
			{label: "bundle install", cmd: []string{"bundle", "install"}},
		},
	},
}

// railsNew installs rails into the project's GEM_HOME and generates the application in
// the project directory: named after the project, for its database (SQLite without one),
// without a git repository of its own. rails new runs bundle install and the importmap,
// Turbo and Stimulus installers itself.
func railsNew(extra ...string) func(p store.Project) []string {
	return func(p store.Project) []string {
		args := []string{"rails", "new", ".", "--name=" + railsAppName(p.Slug), "--database=" + railsDatabase(p), "--skip-git"}
		args = append(args, extra...)
		return append([]string{"sh", "-c", `gem install --no-document rails && exec "$@"`, "envoryx-rails-new"}, args...)
	}
}

// railsAppName turns a slug into a name rails new accepts: underscores for hyphens, and a
// prefix where the slug starts with a digit or is one of the names Rails reserves.
func railsAppName(slug string) string {
	name := strings.ReplaceAll(slug, "-", "_")
	switch name {
	case "application", "destroy", "plugin", "runner", "test", "rails":
		return "app_" + name
	}
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		return "app_" + name
	}
	return name
}

// railsDatabase is the --database of rails new for the project's primary database.
func railsDatabase(p store.Project) string {
	db := p.Service(store.ServiceDatabase)
	if db == nil || !db.Enabled {
		return "sqlite3"
	}
	switch db.Variant {
	case "postgresql":
		return "postgresql"
	case "mysql":
		return "mysql"
	case "mariadb":
		return "mariadb-mysql"
	}
	return "sqlite3"
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

// drupalSettings copies default.settings.php and appends the connection to the project
// database (read from the injected variables at runtime), a hash salt and the config
// sync directory; the proxy in front terminates HTTPS.
const drupalSettings = `
$dir = getcwd() . '/web/sites/default';
if (!is_file("$dir/settings.php") && !copy("$dir/default.settings.php", "$dir/settings.php")) { fwrite(STDERR, "copy default.settings.php failed\n"); exit(1); }
$salt = bin2hex(random_bytes(32));
$block = <<<'EOT'

// Added by Envoryx: the project database (Envoryx injects DB_*), and the proxy in
// front of the site terminates HTTPS.
$databases['default']['default'] = [
  'driver' => getenv('DB_CONNECTION') === 'pgsql' ? 'pgsql' : 'mysql',
  'database' => getenv('DB_DATABASE'),
  'username' => getenv('DB_USERNAME'),
  'password' => getenv('DB_PASSWORD'),
  'host' => getenv('DB_HOST'),
  'port' => getenv('DB_PORT'),
  'prefix' => '',
];
$settings['hash_salt'] = '%SALT%';
$settings['config_sync_directory'] = '../config/sync';
$settings['reverse_proxy'] = TRUE;
$settings['reverse_proxy_addresses'] = [$_SERVER['REMOTE_ADDR'] ?? '127.0.0.1'];
EOT;
file_put_contents("$dir/settings.php", str_replace('%SALT%', $salt, $block), FILE_APPEND);
@mkdir("$dir/files", 0775, true);
@mkdir(getcwd() . '/config/sync', 0775, true);
echo "settings.php prepared\n";
`

// typo3Settings enables the web installer and points TYPO3 at the project database.
const typo3Settings = `
@mkdir('config/system', 0775, true);
$block = <<<'EOT'
<?php
// Added by Envoryx: the project database (Envoryx injects DB_*).
$GLOBALS['TYPO3_CONF_VARS']['DB']['Connections']['Default'] = array_merge($GLOBALS['TYPO3_CONF_VARS']['DB']['Connections']['Default'] ?? [], [
    'driver' => getenv('DB_CONNECTION') === 'pgsql' ? 'pdo_pgsql' : 'mysqli',
    'dbname' => getenv('DB_DATABASE'),
    'user' => getenv('DB_USERNAME'),
    'password' => getenv('DB_PASSWORD'),
    'host' => getenv('DB_HOST'),
    'port' => (int) getenv('DB_PORT'),
]);
EOT;
if (file_put_contents('config/system/additional.php', $block) === false || !touch('public/FIRST_INSTALL')) { fwrite(STDERR, "writing the configuration failed\n"); exit(1); }
echo "installer enabled\n";
`

// shopwareEnvLocal lets APP_URL follow the project address (Symfony's Dotenv resolves
// the injected ENVORYX_URL).
const shopwareEnvLocal = "# Added by Envoryx: the address the project answers at.\nAPP_URL=${ENVORYX_URL}\n"

// craftEnv appends the connection and the site URL to Craft's .env; phpdotenv resolves
// the references to the injected variables when Craft starts.
const craftEnv = `
$lines = "\n# Added by Envoryx: the project database and address (injected by Envoryx).\n"
  . "CRAFT_DB_DRIVER=\${DB_CONNECTION}\nCRAFT_DB_SERVER=\${DB_HOST}\nCRAFT_DB_PORT=\${DB_PORT}\n"
  . "CRAFT_DB_DATABASE=\${DB_DATABASE}\nCRAFT_DB_USER=\${DB_USERNAME}\nCRAFT_DB_PASSWORD=\${DB_PASSWORD}\n"
  . "PRIMARY_SITE_URL=\${ENVORYX_URL}\n";
if (file_put_contents('.env', $lines, FILE_APPEND) === false) { fwrite(STDERR, "writing .env failed\n"); exit(1); }
echo ".env prepared\n";
`

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
	case "go":
		kind, label, env = store.ServiceGo, "Go", goScaffoldEnv
	case "ruby":
		kind, label, env = store.ServiceRuby, "Ruby", rubyScaffoldEnv
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
	// The downloads land in the shared package cache, which the project's plan has not
	// created yet at this point.
	if err := os.MkdirAll(planner.PackageCacheDir(), 0o755); err != nil {
		return fmt.Errorf("create the package cache: %w", err)
	}
	_ = os.Chown(planner.PackageCacheDir(), paths.PUID, paths.PGID)
	if kind == store.ServiceRuby {
		// The gems go to the project home, which the plan has not created yet either.
		if err := os.MkdirAll(planner.HomeDir(proj), 0o755); err != nil {
			return fmt.Errorf("create the project home: %w", err)
		}
		_ = os.Chown(planner.HomeDir(proj), paths.PUID, paths.PGID)
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
			Mounts:     []docker.MountSpec{{Type: "bind", Source: planner.projectHostDir(proj), Target: mount}},
			// Composer/npm downloads need DNS/internet: default bridge network.
			RestartPolicy: "no",
		}
		if ts.cmdFor != nil {
			spec.Cmd = ts.cmdFor(proj)
		}
		if kind == store.ServiceRuby {
			spec.Mounts = append(spec.Mounts, planner.HomeMount(proj))
		}
		spec.Env = append([]string{}, spec.Env...)
		planner.withPackageCache(&spec)
		if kind == store.ServicePHP {
			// The project's php.ini: the extensions the application's composer.json
			// asks for (gd for Drupal, intl for Shopware …) are switched on there.
			spec.Mounts = append(spec.Mounts, docker.MountSpec{Type: "bind", Source: filepath.Join(planner.configHostDir(proj.ID), "php", "zz-envoryx.ini"), Target: phpIniTarget, ReadOnly: true})
		}
		runAsProjectUser(&spec, paths.PUID, paths.PGID)
		res, err := m.engine.RunOneShot(ctx, spec)
		if err != nil {
			return fmt.Errorf("template %s (%s): %w", tpl.ID, ts.label, err)
		}
		if res.ExitCode != 0 {
			out := res.Stdout + "\n" + res.Stderr
			m.log.Warn("template step failed", "project", proj.Slug, "template", tpl.ID, "step", ts.label, "exit", res.ExitCode, "output", tailLines(out, 40))
			return fmt.Errorf("%w: template %s failed at %q: %s", ErrConflict, tpl.ID, ts.label, failureLine(out))
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

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	// errorLine matches the line a crashing Node or Python process prints for the thrown
	// error: "SystemError [ERR_SYSTEM_ERROR]: …", "TypeError: …", "django.core…Error: …".
	errorLine = regexp.MustCompile(`^[\w.]*(Error|Exception)\b[^:]{0,40}: \S`)
	// npmNoise are "npm error" lines that name no cause.
	npmNoise = regexp.MustCompile(`^npm error (code|errno|syscall|path|A complete log|Log files|\s|$)`)
)

// failureLine picks the line of a failed scaffold's output that says what went wrong. The
// last line is often boilerplate: Node ends a crash with "Node.js v24.x" and npm with the
// path of its log file. It prefers the thrown error, then npm's first "npm error" line
// with a cause, and falls back to the last line.
func failureLine(out string) string {
	out = ansiEscape.ReplaceAllString(out, "")
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); errorLine.MatchString(l) {
			return l
		}
	}
	for _, l := range lines {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "npm error ") && !npmNoise.MatchString(l) {
			return l
		}
	}
	return lastLine(out)
}

// tailLines returns the last n non-empty lines of s, for the log.
func tailLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}
