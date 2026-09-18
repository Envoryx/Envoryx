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

	{ID: "php:version", Group: "PHP", Label: "php -v", Description: "Show the PHP version and loaded extensions", Service: store.ServicePHP, Cmd: []string{"php", "-v"}},
	{ID: "php:modules", Group: "PHP", Label: "php -m", Description: "List loaded PHP extensions", Service: store.ServicePHP, Cmd: []string{"php", "-m"}},

	{ID: "npm:install", Group: "npm", Label: "npm install", Description: "Install Node dependencies", Service: store.ServiceNode, Cmd: []string{"npm", "install"}, Requires: []string{"package.json"}},
	{ID: "npm:ci", Group: "npm", Label: "npm ci", Description: "Clean install from package-lock.json", Service: store.ServiceNode, Cmd: []string{"npm", "ci"}, Requires: []string{"package.json", "package-lock.json"}},
	{ID: "npm:build", Group: "npm", Label: "npm run build", Description: "Run the build script", Service: store.ServiceNode, Cmd: []string{"npm", "run", "build"}, Requires: []string{"package.json"}},
	{ID: "pnpm:install", Group: "pnpm", Label: "pnpm install", Description: "Install with pnpm", Service: store.ServiceNode, Cmd: []string{"pnpm", "install"}, Requires: []string{"pnpm-lock.yaml"}},
	{ID: "pnpm:build", Group: "pnpm", Label: "pnpm run build", Description: "Run the build script with pnpm", Service: store.ServiceNode, Cmd: []string{"pnpm", "run", "build"}, Requires: []string{"pnpm-lock.yaml"}},
	{ID: "yarn:install", Group: "yarn", Label: "yarn install", Description: "Install with yarn", Service: store.ServiceNode, Cmd: []string{"yarn", "install"}, Requires: []string{"yarn.lock"}},
	{ID: "yarn:build", Group: "yarn", Label: "yarn build", Description: "Run the build script with yarn", Service: store.ServiceNode, Cmd: []string{"yarn", "build"}, Requires: []string{"yarn.lock"}},
}

func findAction(id string) (Action, bool) {
	for _, a := range actionCatalog {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// ListActions returns the catalogue with availability for the project: the service must
// exist and be running, and the required files must be present in the project directory.
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
		info := ActionInfo{Action: a, Available: true}
		switch {
		case !present[a.Service]:
			info.Available, info.Reason = false, fmt.Sprintf("project has no %s service", a.Service)
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
	for _, info := range infos {
		if info.ID == actionID && !info.Available {
			return nil, Action{}, nil, fmt.Errorf("%w: %s", ErrConflict, info.Reason)
		}
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
	term, err = m.engine.OpenTerminal(ctx, c.ID, opts)
	if err != nil {
		return nil, Action{}, nil, err
	}
	m.audit.Log(ctx, audit.ActionRun, "project", id, map[string]any{"action": action.ID, "service": string(action.Service)})
	return term, action, unlock, nil
}
