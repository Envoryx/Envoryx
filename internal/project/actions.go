package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/envoryx/envoryx/internal/audit"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/store"
	"github.com/envoryx/envoryx/internal/validate"
)

// Action is a predefined command that can be run inside a project container. The command
// is a fixed argv array; nothing from the request is ever interpolated into it.
type Action struct {
	ID          string            `json:"id"`
	Group       string            `json:"group"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Service     store.ServiceKind `json:"service"`
	Cmd         []string          `json:"cmd"`
	// Requires lists files (relative to the project directory) that must exist.
	Requires []string `json:"requires"`
	// Destructive actions ask for confirmation in the UI.
	Destructive bool `json:"destructive"`
}

// ActionInfo is an Action with availability for a specific project.
type ActionInfo struct {
	Action
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// actionCatalog is the closed list of runnable commands.
var actionCatalog = []Action{
	{ID: "composer:install", Group: "Composer", Label: "composer install", Description: "Install dependencies from composer.lock", Service: store.ServicePHP, Cmd: []string{"composer", "install", "--no-interaction", "--prefer-dist"}, Requires: []string{"composer.json"}},
	{ID: "composer:update", Group: "Composer", Label: "composer update", Description: "Update dependencies and composer.lock", Service: store.ServicePHP, Cmd: []string{"composer", "update", "--no-interaction", "--prefer-dist"}, Requires: []string{"composer.json"}},
	{ID: "composer:dump-autoload", Group: "Composer", Label: "composer dump-autoload", Description: "Regenerate the autoloader", Service: store.ServicePHP, Cmd: []string{"composer", "dump-autoload", "--no-interaction"}, Requires: []string{"composer.json"}},

	{ID: "artisan:migrate", Group: "Artisan", Label: "artisan migrate", Description: "Run pending database migrations", Service: store.ServicePHP, Cmd: []string{"php", "artisan", "migrate", "--force", "--no-interaction"}, Requires: []string{"artisan"}},
	{ID: "artisan:migrate-fresh", Group: "Artisan", Label: "artisan migrate:fresh --seed", Description: "Drop all tables, migrate and seed", Service: store.ServicePHP, Cmd: []string{"php", "artisan", "migrate:fresh", "--seed", "--force", "--no-interaction"}, Requires: []string{"artisan"}, Destructive: true},
	{ID: "artisan:db-seed", Group: "Artisan", Label: "artisan db:seed", Description: "Run database seeders", Service: store.ServicePHP, Cmd: []string{"php", "artisan", "db:seed", "--force", "--no-interaction"}, Requires: []string{"artisan"}},
	{ID: "artisan:key-generate", Group: "Artisan", Label: "artisan key:generate", Description: "Generate APP_KEY in .env", Service: store.ServicePHP, Cmd: []string{"php", "artisan", "key:generate", "--force", "--no-interaction"}, Requires: []string{"artisan"}},
	{ID: "artisan:storage-link", Group: "Artisan", Label: "artisan storage:link", Description: "Create the public storage symlink", Service: store.ServicePHP, Cmd: []string{"php", "artisan", "storage:link", "--no-interaction"}, Requires: []string{"artisan"}},
	{ID: "artisan:cache-clear", Group: "Artisan", Label: "artisan cache:clear", Description: "Flush the application cache", Service: store.ServicePHP, Cmd: []string{"php", "artisan", "cache:clear", "--no-interaction"}, Requires: []string{"artisan"}},
	{ID: "artisan:optimize-clear", Group: "Artisan", Label: "artisan optimize:clear", Description: "Clear config, route, view and event caches", Service: store.ServicePHP, Cmd: []string{"php", "artisan", "optimize:clear", "--no-interaction"}, Requires: []string{"artisan"}},

	{ID: "console:cache-clear", Group: "Symfony", Label: "console cache:clear", Description: "Clear the Symfony cache", Service: store.ServicePHP, Cmd: []string{"php", "bin/console", "cache:clear", "--no-interaction"}, Requires: []string{"bin/console"}},
	{ID: "console:migrate", Group: "Symfony", Label: "console doctrine:migrations:migrate", Description: "Run Doctrine migrations", Service: store.ServicePHP, Cmd: []string{"php", "bin/console", "doctrine:migrations:migrate", "--no-interaction"}, Requires: []string{"bin/console"}},

	{ID: "drush:site-install", Group: "Drupal", Label: "drush site:install", Description: "Install Drupal with the standard profile into the project database; prints the admin password", Service: store.ServicePHP, Cmd: []string{"sh", "-c", drushInstallScript, "envoryx-drush"}, Requires: []string{"vendor/bin/drush"}, Destructive: true},
	{ID: "drush:cache-rebuild", Group: "Drupal", Label: "drush cache:rebuild", Description: "Rebuild all Drupal caches", Service: store.ServicePHP, Cmd: []string{"vendor/bin/drush", "cache:rebuild"}, Requires: []string{"vendor/bin/drush"}},

	{ID: "shopware:install", Group: "Shopware", Label: "system:install --basic-setup", Description: "Create the tables, a sales channel for APP_URL and the administrator admin / shopware", Service: store.ServicePHP, Cmd: []string{"php", "bin/console", "system:install", "--basic-setup", "--force", "--no-interaction"}, Requires: []string{"bin/console", "vendor/shopware/core"}, Destructive: true},

	{ID: "typo3:setup", Group: "TYPO3", Label: "typo3 setup", Description: "Set TYPO3 up in the project database with a site for the project URL; prints the admin password", Service: store.ServicePHP, Cmd: []string{"sh", "-c", typo3SetupScript, "envoryx-typo3"}, Requires: []string{"vendor/bin/typo3"}, Destructive: true},
	{ID: "craft:install", Group: "Craft CMS", Label: "craft install", Description: "Install Craft into the project database with a site for the project URL; prints the admin password", Service: store.ServicePHP, Cmd: []string{"sh", "-c", craftInstallScript, "envoryx-craft"}, Requires: []string{"craft", "vendor/craftcms/cms"}, Destructive: true},

	{ID: "php:version", Group: "PHP", Label: "php -v", Description: "Show the PHP version and loaded extensions", Service: store.ServicePHP, Cmd: []string{"php", "-v"}},
	{ID: "php:modules", Group: "PHP", Label: "php -m", Description: "List loaded PHP extensions", Service: store.ServicePHP, Cmd: []string{"php", "-m"}},

	{ID: "node:version", Group: "Node.js", Label: "node -v", Description: "Show the Node.js version", Service: store.ServiceNode, Cmd: []string{"node", "-v"}},

	{ID: "npm:install", Group: "npm", Label: "npm install", Description: "Install Node dependencies", Service: store.ServiceNode, Cmd: []string{"npm", "install"}, Requires: []string{"package.json"}},
	{ID: "npm:ci", Group: "npm", Label: "npm ci", Description: "Clean install from package-lock.json", Service: store.ServiceNode, Cmd: []string{"npm", "ci"}, Requires: []string{"package.json", "package-lock.json"}},
	{ID: "npm:build", Group: "npm", Label: "npm run build", Description: "Run the build script", Service: store.ServiceNode, Cmd: []string{"npm", "run", "build"}, Requires: []string{"package.json"}},
	{ID: "pnpm:install", Group: "pnpm", Label: "pnpm install", Description: "Install with pnpm", Service: store.ServiceNode, Cmd: []string{"pnpm", "install"}, Requires: []string{"pnpm-lock.yaml"}},
	{ID: "pnpm:build", Group: "pnpm", Label: "pnpm run build", Description: "Run the build script with pnpm", Service: store.ServiceNode, Cmd: []string{"pnpm", "run", "build"}, Requires: []string{"pnpm-lock.yaml"}},
	{ID: "yarn:install", Group: "yarn", Label: "yarn install", Description: "Install with yarn", Service: store.ServiceNode, Cmd: []string{"yarn", "install"}, Requires: []string{"yarn.lock"}},
	{ID: "yarn:build", Group: "yarn", Label: "yarn build", Description: "Run the build script with yarn", Service: store.ServiceNode, Cmd: []string{"yarn", "build"}, Requires: []string{"yarn.lock"}},

	{ID: "python:version", Group: "Python", Label: "python --version", Description: "Show the Python version (the project's .venv when it exists)", Service: store.ServicePython, Cmd: []string{"python", "--version"}},
	{ID: "python:venv", Group: "Python", Label: "python -m venv .venv", Description: "Create the virtual environment in the project directory", Service: store.ServicePython, Cmd: []string{"python", "-m", "venv", pythonVenvPath}},
	// pip installs into the project's .venv: PATH puts its bin/ first once it exists; the
	// venv is created on demand so a fresh checkout works with one click.
	{ID: "pip:install", Group: "pip", Label: "pip install -r requirements.txt", Description: "Create .venv if missing and install the requirements", Service: store.ServicePython, Cmd: []string{"sh", "-c", pipInstallScript, "envoryx-pip"}, Requires: []string{"requirements.txt"}},
	{ID: "pip:freeze", Group: "pip", Label: "pip freeze", Description: "List the installed packages with versions", Service: store.ServicePython, Cmd: []string{"pip", "freeze"}},
	{ID: "uv:sync", Group: "uv", Label: "uv sync", Description: "Create .venv and install the project from pyproject.toml / uv.lock", Service: store.ServicePython, Cmd: []string{"uv", "sync"}, Requires: []string{"pyproject.toml"}},
	{ID: "uv:lock", Group: "uv", Label: "uv lock", Description: "Resolve and write uv.lock", Service: store.ServicePython, Cmd: []string{"uv", "lock"}, Requires: []string{"pyproject.toml"}},

	{ID: "django:migrate", Group: "Django", Label: "manage.py migrate", Description: "Apply pending database migrations", Service: store.ServicePython, Cmd: []string{"python", "manage.py", "migrate", "--no-input"}, Requires: []string{"manage.py"}},
	{ID: "django:makemigrations", Group: "Django", Label: "manage.py makemigrations", Description: "Create migrations for model changes", Service: store.ServicePython, Cmd: []string{"python", "manage.py", "makemigrations", "--no-input"}, Requires: []string{"manage.py"}},
	{ID: "django:collectstatic", Group: "Django", Label: "manage.py collectstatic", Description: "Collect static files into STATIC_ROOT", Service: store.ServicePython, Cmd: []string{"python", "manage.py", "collectstatic", "--no-input"}, Requires: []string{"manage.py"}},
	{ID: "django:check", Group: "Django", Label: "manage.py check", Description: "Run Django's system checks", Service: store.ServicePython, Cmd: []string{"python", "manage.py", "check"}, Requires: []string{"manage.py"}},
	{ID: "django:flush", Group: "Django", Label: "manage.py flush", Description: "Remove all data from the database (keeps the schema)", Service: store.ServicePython, Cmd: []string{"python", "manage.py", "flush", "--no-input"}, Requires: []string{"manage.py"}, Destructive: true},

	{ID: "go:version", Group: "Go", Label: "go version", Description: "Show the Go version", Service: store.ServiceGo, Cmd: []string{"go", "version"}},
	{ID: "go:build", Group: "Go", Label: "go build ./...", Description: "Compile every package of the module (nothing is written)", Service: store.ServiceGo, Cmd: []string{"go", "build", "-o", "/dev/null", "./..."}, Requires: []string{"go.mod"}},
	{ID: "go:vet", Group: "Go", Label: "go vet ./...", Description: "Report suspicious constructs", Service: store.ServiceGo, Cmd: []string{"go", "vet", "./..."}, Requires: []string{"go.mod"}},
	{ID: "go:fmt", Group: "Go", Label: "gofmt -l .", Description: "List the files gofmt would change", Service: store.ServiceGo, Cmd: []string{"gofmt", "-l", "."}, Requires: []string{"go.mod"}},
	{ID: "go:mod-tidy", Group: "Go", Label: "go mod tidy", Description: "Add missing and remove unused module requirements", Service: store.ServiceGo, Cmd: []string{"go", "mod", "tidy"}, Requires: []string{"go.mod"}},
	{ID: "go:mod-download", Group: "Go", Label: "go mod download", Description: "Download the modules into the shared module cache", Service: store.ServiceGo, Cmd: []string{"go", "mod", "download"}, Requires: []string{"go.mod"}},
	{ID: "go:generate", Group: "Go", Label: "go generate ./...", Description: "Run the //go:generate directives", Service: store.ServiceGo, Cmd: []string{"go", "generate", "./..."}, Requires: []string{"go.mod"}},
}

// The installers of the CMS templates, run once the database is up. They read the
// connection and the project address from the injected variables and create the
// administrator with a generated password that the output shows once. Constants:
// nothing from the request is interpolated.
const (
	// adminPassword satisfies the password rules of TYPO3 and Craft (upper and lower
	// case, digits, a special character).
	adminPassword = `pw="Envoryx-$(php -r 'echo bin2hex(random_bytes(6));')!"`

	drushInstallScript = `DRUSH_OPTIONS_URI="$ENVORYX_URL" exec vendor/bin/drush site:install standard --yes --site-name="$ENVORYX_PROJECT"`

	typo3SetupScript = adminPassword + `
driver=mysqli; [ "$DB_CONNECTION" = pgsql ] && driver=postgres
vendor/bin/typo3 setup --force --no-interaction --driver="$driver" --host="$DB_HOST" --port="$DB_PORT" --dbname="$DB_DATABASE" \
  --username="$DB_USERNAME" --password="$DB_PASSWORD" --admin-username=admin --admin-user-password="$pw" \
  --admin-email=admin@example.com --project-name="$ENVORYX_PROJECT" --server-type=other --create-site="$ENVORYX_URL" || exit
printf '\nBackend: %s/typo3  User: admin  Password: %s\n' "$ENVORYX_URL" "$pw"`

	craftInstallScript = adminPassword + `
php craft install --interactive=0 --username=admin --email=admin@example.com --password="$pw" \
  --site-name="$ENVORYX_PROJECT" --site-url="$ENVORYX_URL" --language=en-US || exit
printf '\nControl panel: %s/admin  User: admin  Password: %s\n' "$ENVORYX_URL" "$pw"`
)

// pipInstallScript creates the venv when missing and installs requirements.txt into it.
// A constant: nothing from the request is interpolated.
const pipInstallScript = `[ -x ` + pythonVenvPath + `/bin/python ] || python -m venv ` + pythonVenvPath + `; exec ` + pythonVenvPath + `/bin/pip install -r requirements.txt`

func findAction(id string) (Action, bool) {
	for _, a := range actionCatalog {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// ListActions returns the catalogue entries whose service is part of the project, with
// availability: the service must be running and the required files must be present in
// the project directory. Actions of services the project does not have are omitted
// rather than greyed out (a PHP-only project has no use for npm entries).
func (m *Manager) ListActions(ctx context.Context, id string) ([]ActionInfo, error) {
	view, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	planner, err := m.planner()
	if err != nil {
		return nil, err
	}
	dir := planner.ProjectDir(view.Project)
	running := map[store.ServiceKind]bool{}
	present := map[store.ServiceKind]bool{}
	for _, s := range view.Status.Services {
		present[s.Kind] = true
		running[s.Kind] = s.Running
	}
	out := make([]ActionInfo, 0, len(actionCatalog))
	for _, a := range actionCatalog {
		if !present[a.Service] {
			continue
		}
		info := ActionInfo{Action: a, Available: true}
		switch {
		case !running[a.Service]:
			info.Available, info.Reason = false, fmt.Sprintf("%s container is not running", a.Service)
		default:
			for _, f := range a.Requires {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
					info.Available, info.Reason = false, f+" not found in project"
					break
				}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// RunAction starts a catalogue action inside the project container and returns the live
// session (output only; input is never forwarded). The project lock is held until the
// returned release function is called, so lifecycle operations report "busy" meanwhile.
func (m *Manager) RunAction(ctx context.Context, id, actionID string, cols, rows uint) (term docker.Terminal, action Action, release func(), err error) {
	if err := validate.UUID(id); err != nil {
		return nil, Action{}, nil, ErrNotFound
	}
	action, ok := findAction(actionID)
	if !ok {
		return nil, Action{}, nil, fmt.Errorf("%w: unknown action", store.ErrNotFound)
	}
	unlock, err := m.lock(id)
	if err != nil {
		return nil, Action{}, nil, err
	}
	defer func() {
		if err != nil {
			unlock()
		}
	}()
	infos, err := m.ListActions(ctx, id)
	if err != nil {
		return nil, Action{}, nil, err
	}
	listed := false
	for _, info := range infos {
		if info.ID != actionID {
			continue
		}
		listed = true
		if !info.Available {
			return nil, Action{}, nil, fmt.Errorf("%w: %s", ErrConflict, info.Reason)
		}
	}
	if !listed {
		return nil, Action{}, nil, fmt.Errorf("%w: project has no %s service", ErrConflict, action.Service)
	}
	c, err := m.ServiceContainer(ctx, id, action.Service)
	if err != nil {
		return nil, Action{}, nil, err
	}
	paths, err := m.paths()
	if err != nil {
		return nil, Action{}, nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	if cols == 0 || cols > 500 {
		cols = 120
	}
	if rows == 0 || rows > 200 {
		rows = 40
	}
	opts := docker.TerminalOptions{
		Cmd:        action.Cmd,
		Cols:       cols,
		Rows:       rows,
		WorkingDir: appMountTarget,
		User:       fmt.Sprintf("%d:%d", paths.PUID, paths.PGID),
		Env:        append([]string{"TERM=xterm-256color", "COLORTERM=truecolor", "LANG=C.UTF-8", "CI=1"}, toolEnv...),
	}
	m.ensurePasswdEntry(ctx, c.ID, c.Name, opts.User)
	term, err = m.engine.OpenTerminal(ctx, c.ID, opts)
	if err != nil {
		return nil, Action{}, nil, err
	}
	m.audit.Log(ctx, audit.ActionRun, "project", id, map[string]any{"action": action.ID, "service": string(action.Service)})
	return term, action, unlock, nil
}
