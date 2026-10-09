package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// Shopware's build scripts call php and npm in turn, so they run in the PHP container,
	// which has Node.js for them.
	{ID: "shopware:build-administration", Group: "Shopware", Label: "build-administration.sh", Description: "Build the administration (npm install and build of the admin and its plugins); after installing or changing an admin plugin", Service: store.ServicePHP, Cmd: []string{"bin/build-administration.sh"}, Requires: []string{"bin/build-administration.sh"}},
	{ID: "shopware:build-storefront", Group: "Shopware", Label: "build-storefront.sh", Description: "Build the storefront JavaScript and compile the themes; after installing or changing a storefront plugin or theme", Service: store.ServicePHP, Cmd: []string{"bin/build-storefront.sh"}, Requires: []string{"bin/build-storefront.sh"}},

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
	{ID: "pip:install", Group: "pip", Label: "pip install -r requirements.txt", Description: "Create .venv (rebuilt after a Python version change) and install the requirements", Service: store.ServicePython, Cmd: []string{"sh", "-c", pipInstallScript, "envoryx-pip"}, Requires: []string{"requirements.txt"}},
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

	{ID: "ruby:version", Group: "Ruby", Label: "ruby --version", Description: "Show the Ruby version", Service: store.ServiceRuby, Cmd: []string{"ruby", "--version"}},
	// The gems go to GEM_HOME in the persistent project home (see rubyEnv).
	{ID: "bundle:install", Group: "Bundler", Label: "bundle install", Description: "Install the gems of the Gemfile (Gemfile.lock) into the project home", Service: store.ServiceRuby, Cmd: []string{"bundle", "install"}, Requires: []string{"Gemfile"}},
	{ID: "bundle:update", Group: "Bundler", Label: "bundle update", Description: "Update the gems and Gemfile.lock within the Gemfile's constraints", Service: store.ServiceRuby, Cmd: []string{"bundle", "update"}, Requires: []string{"Gemfile"}},
	{ID: "bundle:outdated", Group: "Bundler", Label: "bundle outdated", Description: "List the gems with newer versions", Service: store.ServiceRuby, Cmd: []string{"bundle", "outdated"}, Requires: []string{"Gemfile.lock"}},
	{ID: "rubocop", Group: "Ruby", Label: "rubocop", Description: "Run RuboCop over the project", Service: store.ServiceRuby, Cmd: []string{"bundle", "exec", "rubocop"}, Requires: []string{".rubocop.yml"}},

	{ID: "rails:db-prepare", Group: "Rails", Label: "rails db:prepare", Description: "Create the database if needed, load the schema or run pending migrations, seed a new database", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "db:prepare"}, Requires: []string{"bin/rails"}},
	{ID: "rails:db-migrate", Group: "Rails", Label: "rails db:migrate", Description: "Apply pending database migrations", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "db:migrate"}, Requires: []string{"bin/rails"}},
	{ID: "rails:db-rollback", Group: "Rails", Label: "rails db:rollback", Description: "Revert the last migration", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "db:rollback"}, Requires: []string{"bin/rails"}, Destructive: true},
	{ID: "rails:db-seed", Group: "Rails", Label: "rails db:seed", Description: "Load db/seeds.rb", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "db:seed"}, Requires: []string{"bin/rails"}},
	{ID: "rails:routes", Group: "Rails", Label: "rails routes", Description: "List the application's routes", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "routes"}, Requires: []string{"bin/rails"}},
	{ID: "rails:assets-precompile", Group: "Rails", Label: "rails assets:precompile", Description: "Build the assets into public/assets - what production mode serves", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "assets:precompile"}, Requires: []string{"bin/rails"}},
	{ID: "rails:tmp-clear", Group: "Rails", Label: "rails tmp:clear", Description: "Clear the cache, sockets and screenshot files in tmp/", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "tmp:clear"}, Requires: []string{"bin/rails"}},
	{ID: "rails:about", Group: "Rails", Label: "rails about", Description: "Show the versions of Ruby, Rails and the database adapter", Service: store.ServiceRuby, Cmd: []string{"bin/rails", "about"}, Requires: []string{"bin/rails"}},

	// Maven and Gradle run through the project's wrapper when it has one (see mavenCmd).
	{ID: "java:version", Group: "Java", Label: "java -version", Description: "Show the JDK version", Service: store.ServiceJava, Cmd: []string{"java", "-version"}},
	{ID: "maven:package", Group: "Maven", Label: "mvn package", Description: "Compile and package the project, without running the tests", Service: store.ServiceJava, Cmd: mavenCmd("-DskipTests", "package"), Requires: []string{"pom.xml"}},
	{ID: "maven:clean", Group: "Maven", Label: "mvn clean", Description: "Delete target/", Service: store.ServiceJava, Cmd: mavenCmd("clean"), Requires: []string{"pom.xml"}},
	{ID: "maven:dependency-tree", Group: "Maven", Label: "mvn dependency:tree", Description: "Show the resolved dependencies", Service: store.ServiceJava, Cmd: mavenCmd("dependency:tree"), Requires: []string{"pom.xml"}},
	{ID: "maven:dependency-updates", Group: "Maven", Label: "mvn versions:display-dependency-updates", Description: "List the dependencies with newer versions", Service: store.ServiceJava, Cmd: mavenCmd("versions:display-dependency-updates"), Requires: []string{"pom.xml"}},
	{ID: "gradle:build", Group: "Gradle", Label: "gradle build", Description: "Compile and assemble the project, without running the tests", Service: store.ServiceJava, Cmd: gradleCmd("-x", "test", "build"), Requires: []string{"build.gradle|build.gradle.kts"}},
	{ID: "gradle:clean", Group: "Gradle", Label: "gradle clean", Description: "Delete build/", Service: store.ServiceJava, Cmd: gradleCmd("clean"), Requires: []string{"build.gradle|build.gradle.kts"}},
	{ID: "gradle:dependencies", Group: "Gradle", Label: "gradle dependencies", Description: "Show the resolved dependencies", Service: store.ServiceJava, Cmd: gradleCmd("dependencies"), Requires: []string{"build.gradle|build.gradle.kts"}},
	{ID: "gradle:tasks", Group: "Gradle", Label: "gradle tasks", Description: "List the tasks the build offers", Service: store.ServiceJava, Cmd: gradleCmd("tasks"), Requires: []string{"build.gradle|build.gradle.kts"}},

	// The dotnet commands take the one solution or project file at the top of the project.
	{ID: "dotnet:info", Group: ".NET", Label: "dotnet --info", Description: "Show the SDK, the runtimes and the environment", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "--info"}},
	{ID: "dotnet:restore", Group: ".NET", Label: "dotnet restore", Description: "Restore the NuGet packages into the package cache", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "restore"}, Requires: []string{dotnetBuildFiles}},
	{ID: "dotnet:build", Group: ".NET", Label: "dotnet build", Description: "Compile the solution or project", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "build"}, Requires: []string{dotnetBuildFiles}},
	{ID: "dotnet:clean", Group: ".NET", Label: "dotnet clean", Description: "Delete the build output", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "clean"}, Requires: []string{dotnetBuildFiles}},
	{ID: "dotnet:format", Group: ".NET", Label: "dotnet format", Description: "Format the code by the .editorconfig rules", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "format"}, Requires: []string{dotnetBuildFiles}},
	{ID: "dotnet:outdated", Group: ".NET", Label: "dotnet list package --outdated", Description: "List the NuGet packages with newer versions", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "list", "package", "--outdated"}, Requires: []string{dotnetBuildFiles}},
	// dotnet-ef ships in the image; the project needs Microsoft.EntityFrameworkCore.Design.
	{ID: "ef:database-update", Group: "Entity Framework", Label: "dotnet ef database update", Description: "Apply pending migrations to the project database", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "ef", "database", "update"}, Requires: []string{dotnetProjectFiles}},
	{ID: "ef:migrations-list", Group: "Entity Framework", Label: "dotnet ef migrations list", Description: "List the migrations and whether they are applied", Service: store.ServiceDotnet, Cmd: []string{"dotnet", "ef", "migrations", "list"}, Requires: []string{dotnetProjectFiles}},
}

// The files a dotnet command finds on its own at the top of the project.
const (
	dotnetProjectFiles = "*.csproj|*.fsproj|*.vbproj"
	dotnetBuildFiles   = "*.sln|*.slnx|" + dotnetProjectFiles
)

// mavenCmd runs Maven through the project's wrapper (mvnw) when there is one, else the
// image's mvn, in batch mode. The goals are constants of the catalogue.
func mavenCmd(goals ...string) []string {
	return append([]string{"sh", "-c", `mvn=mvn; [ -f ./mvnw ] && mvn="sh ./mvnw"; exec $mvn -B "$@"`, "envoryx-mvn"}, goals...)
}

// gradleCmd runs Gradle through the project's wrapper (gradlew) when there is one, else
// the image's gradle, without leaving a daemon behind in the action's exec.
func gradleCmd(tasks ...string) []string {
	return append([]string{"sh", "-c", `gradle=gradle; [ -f ./gradlew ] && gradle="sh ./gradlew"; exec $gradle --no-daemon "$@"`, "envoryx-gradle"}, tasks...)
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

// pipInstallScript installs requirements.txt into the venv. It creates the venv when it is
// missing and rebuilds it (venv --clear) when it was made for another Python minor: after a
// version change the old one still has a working bin/python link but no pip and none of the
// packages for the new interpreter. Before the rebuild it lists what the old venv held, so
// packages installed by hand and never added to requirements.txt can be put back. uv
// writes "version_info" instead of "version" into pyvenv.cfg. A constant: nothing from
// the request is interpolated.
const pipInstallScript = `v=$(python -c 'import sys; print("%d.%d" % sys.version_info[:2])'); ` +
	`if ! grep -Eq "^version(_info)? *= *$v([.]|$)" ` + pythonVenvPath + `/pyvenv.cfg 2>/dev/null; then ` +
	`old=$(ls -d ` + pythonVenvPath + `/lib/python*/site-packages/*.dist-info 2>/dev/null | sed -E 's|.*/||; s|-([^-]+)[.]dist-info$|==\1|' | grep -v '^pip==' | sort -f); ` +
	`if [ -n "$old" ]; then echo "envoryx: rebuilding .venv for Python $v. The old one had these packages - whatever requirements.txt doesn't list has to be installed again:"; echo "$old" | sed 's/^/  /'; echo; fi; ` +
	`python -m venv --clear ` + pythonVenvPath + `; fi; ` +
	`exec ` + pythonVenvPath + `/bin/pip install -r requirements.txt`

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
	restarting := map[store.ServiceKind]bool{}
	for _, s := range view.Status.Services {
		present[s.Kind] = true
		running[s.Kind] = s.Running
		restarting[s.Kind] = s.State == "restarting"
	}
	out := make([]ActionInfo, 0, len(actionCatalog))
	for _, a := range actionCatalog {
		if !present[a.Service] {
			continue
		}
		info := ActionInfo{Action: a, Available: true}
		switch {
		case !running[a.Service] && restarting[a.Service]:
			// A crash-looping server: name the way out, the logs and the server switch.
			info.Available, info.Reason = false, fmt.Sprintf("%s container keeps restarting - its main process exits, the logs say why; switch its server off to run commands in it", a.Service)
		case !running[a.Service]:
			info.Available, info.Reason = false, fmt.Sprintf("%s container is not running", a.Service)
		default:
			// An entry may name alternatives ("build.gradle|build.gradle.kts") and
			// patterns ("*.csproj"); one match is enough.
			for _, f := range a.Requires {
				found := false
				for _, alt := range strings.Split(f, "|") {
					if strings.ContainsAny(alt, "*?[") {
						if m, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(alt))); len(m) > 0 {
							found = true
							break
						}
					} else if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(alt))); err == nil {
						found = true
						break
					}
				}
				if !found {
					info.Available, info.Reason = false, strings.ReplaceAll(f, "|", " or ")+" not found in project"
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
	if strings.HasPrefix(action.ID, "rails:db-") {
		// The Rails tasks work on every database of the environment, the queue, cache and
		// cable databases rubyDatabaseEnv names included. PostgreSQL's project login
		// creates them itself, MySQL's and MariaDB's may not; a failure is left to the
		// task's own error.
		if err := m.ensureRailsDatabases(ctx, id); err != nil {
			m.log.Warn("create the Rails databases", "project", id, "err", err)
		}
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
