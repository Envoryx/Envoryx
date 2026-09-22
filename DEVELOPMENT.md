# Development

## Prerequisites

- Go 1.27+
- Node.js 22+
- Docker (optional, for real lifecycle tests and running projects)

## Layout

```
cmd/envoryx          entry point (serve, healthcheck, admin, version) and the
                     CLI client (project/backup/git/login, see cli*.go)
internal/           Go packages – see ARCHITECTURE.md §3
web/                React + TypeScript frontend (Vite)
deploy/             docker-compose example
```

## Running locally

Backend (uses `.local/config` and `.local/projects` in the repo, text logs,
CORS for the Vite dev server):

```
make run
```

Frontend with hot reload, proxying `/api` to the backend on :8787:

```
cd web && npm install && npm run dev
```

Open http://localhost:5173. The first visit shows the setup page.

When Envoryx runs directly on your machine (not in a container) it detects
"bare metal" mode and uses the same paths for bind mounts that it sees itself,
so projects work against your local Docker daemon out of the box.

To run the production build (frontend embedded into the binary):

```
make build && ENVORYX_CONFIG_DIR=$PWD/.local/config ENVORYX_PROJECTS_DIR=$PWD/.local/projects ./bin/envoryx
```

The CLI half of the binary talks to whatever server it is pointed at, so a
local run is enough to try it. Give it a token from *Settings → API tokens*
and a configuration file of its own, well away from the one in your home
directory:

```
export ENVORYX_CLI_CONFIG=$PWD/.local/cli.json
./bin/envoryx login --url http://localhost:8787 --token stq_…
./bin/envoryx project list
```

## Tests

```
make test              # go vet + go test -race + tsc + vitest
make test-go
make test-web
make test-integration  # needs a Docker daemon; creates/removes alpine and RustFS containers
                       # and one project with database, Redis and Mailpit
```

Backend tests use an in-memory SQLite database and a fake Docker engine
(`internal/docker/dockertest`) that supports failure injection, foreign
containers and state manipulation. Covered flows include: create, start, stop,
restart, delete, rollback after a failed container create, Docker unavailable,
invalid paths / ids, Envoryx restart (new manager on existing state), container
unexpectedly stopped, orphaned resources, unauthorized requests and CSRF, and
the project shapes – PHP, project without PHP with a Node dev server
(routing of the project URL to the node container, unpublished web port,
`package.json` wait guard, injected service variables), Python application
server (`python_test.go`: entry-file wait guard, venv `PATH`, debugpy port,
Python + Node frontend, removal with paused workers) and static site (SPA
fallback, `index.html` starter). `internal/runtime/webserver_test.go` pins
the PHP web configs as goldens so the static branch cannot drift into them.

The fake engine proves what Envoryx asks Docker for, not whether an image
accepts it. `internal/project/integration_test.go` (build tag `integration`)
therefore starts one project with the stateful services – database, Redis,
Mailpit – against a real engine and checks that they reach *running* and that
the data survives a database container that is thrown away and rebuilt from
the plan. It resolves the **catalogue's default versions** on purpose: that is
what a new project gets, and it is how an upstream image changing its layout
shows up here first. It runs with PostgreSQL by default;
`ENVORYX_TEST_ALL_DATABASES=1` adds MariaDB, MySQL and MongoDB, whose images
are gigabytes – CI sets it on the weekly run. A container the restart policy keeps restarting fails the
test at once, with its last log lines, instead of waiting out the timeout.

For the same reason, anything whose path or layout depends on a version goes
through one accessor with the reason in its comment – see
`runtime.Dialect.DataDirTarget`, where PostgreSQL 18 moved its cluster into a
major-version subdirectory. Grep for that method before hard-coding such a
path somewhere else.

Frontend tests (Vitest + Testing Library) cover the login/setup flow, the
project list with actions, the complete wizard including preview and
validation errors for the PHP, Node.js and static stacks, and the detail
tabs per runtime shape. `src/i18n/i18n.test.ts` checks that every German
key exists in every other dictionary with matching placeholders.

### Browser end-to-end tests

`web/e2e` holds Playwright specs that drive the built binary in a real
browser against a real Docker engine – the path a new user takes: first-run
setup, sign-in, the wizard, a running project answering on its port, live
logs, stop and delete (`lifecycle.spec.ts`, a PHP project) and the same for
a static project without PHP (`node-only.spec.ts`: the web container alone
serves the starter `index.html`; Git and IDE tabs without PHP). They run in
CI on every push (`e2e` job) and locally with:

```
make build                                   # the binary embeds the frontend
cd web && npx playwright install chromium    # once
make test-e2e
```

`e2e/global-setup.ts` starts `bin/envoryx` on 127.0.0.1:18790 with throw-away
`/config` and `/projects` directories (project ports 25000–25099, no
proxy/SSH listeners) and removes everything at the end, including Docker
resources the test project may have left behind. Override with
`ENVORYX_E2E_PORT`, `ENVORYX_E2E_PORT_RANGE_START`, `ENVORYX_E2E_BIN`;
`ENVORYX_E2E_KEEP=1` keeps the data directory. The server log lands in
`web/test-results/envoryx.log`, traces and screenshots of failed specs next
to it (`npx playwright show-trace …`). The specs are serial and build on each
other – add new flows as further `test()` blocks in order, or as a new spec
file that creates its own project (`global-setup.ts` exports the project
names and slugs it cleans up). The Vite template case in `node-only.spec.ts`
is `test.skip` by default because it runs `npm install` against the
registry; run it by hand when touching the Node templates.

### Node image and scaffold smoke

Node templates (`internal/project/templates.go`) run `create-vite`,
`create-next-app` and `nuxi` as one-shot containers from
`ghcr.io/envoryx/envoryx-node:<v>`. The argv, env and mount point are pinned
to what was verified against the real image; the unit tests only check that
the plan carries them. Re-run this smoke when changing a template, bumping
the default Node version or rebuilding the image – each command must end
with exit 0 and never wait for input:

```sh
D=$(mktemp -d); chmod 777 "$D"
run() { docker run --rm -i --user 1000:1000 -w /tmp/my-shop -v "$D:/tmp/my-shop" \
  -e HOME=/tmp -e npm_config_cache=/tmp/.npm -e npm_config_yes=true -e CI=1 \
  -e COREPACK_ENABLE_DOWNLOAD_PROMPT=0 -e NPM_CONFIG_UPDATE_NOTIFIER=false \
  --entrypoint sh ghcr.io/envoryx/envoryx-node:24 -c '"$@" </dev/null' -- "$@"; }

# vite
run npm create vite@latest . -- --template react-ts && run npm install
# next (empty $D again first)
run npx --yes create-next-app@latest . --yes --ts --app --use-npm --disable-git
# nuxt (empty $D again first)
run npx --yes nuxi@latest init . --template minimal --packageManager npm --no-install --no-gitInit --force && run npm install
```

The project directory is mounted under `/tmp/<slug>` rather than
`/var/www/html` because `create-next-app` refuses a target whose parent
directory is not writable (`/var/www` is root-owned in the image). Then
start the dev server the way the planner does (`npm run dev -- --host
0.0.0.0 --port 5173 --strictPort` for Vite, `-H 0.0.0.0 -p 3000` for Next,
`--host 0.0.0.0 --port 3000` for Nuxt) and probe it from inside the
container – the image has no curl, use
`node -e 'http.get({host:"127.0.0.1",port:5173,headers:{Host:"my-shop.test"}},r=>console.log(r.statusCode))'`.
For Vite also confirm the host allow-list: with
`__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.test` requests for `my-shop.test`,
`my-shop-dev.test` and `app.test` answer 200 and `evil.example.com` gets 403.
The variable must stay a single entry: Vite before 8.3 appends it verbatim
as one host (only 8.3+ splits on commas), so a comma-joined list would block
every request.

### Python image and scaffold smoke

Python templates run `python -m venv`, `pip install …`, `django-admin
startproject config .` and a settings patch (`python -c`) as one-shot
containers from `ghcr.io/envoryx/envoryx-python:<v>` with the venv first on
`PATH`. Build the image locally (`docker build --build-arg
BASE_TAG=3.13-slim-bookworm --build-arg PYTHON_VERSION=3.13 -t
ghcr.io/envoryx/envoryx-python:3.13 images/python`) and re-run this smoke
when changing a template, bumping the default Python version or rebuilding
the image:

```sh
D=$(mktemp -d); chmod 777 "$D"
run() { docker run --rm --user 1000:1000 -w /var/www/html -v "$D:/var/www/html" \
  -e HOME=/tmp -e PIP_CACHE_DIR=/tmp/.pip -e PIP_DISABLE_PIP_VERSION_CHECK=1 \
  -e VIRTUAL_ENV=/var/www/html/.venv \
  -e PATH=/var/www/html/.venv/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin \
  ghcr.io/envoryx/envoryx-python:3.13 "$@"; }

run python -m venv /var/www/html/.venv
# django
run pip install django dj-database-url gunicorn 'psycopg[binary]' mysqlclient
run django-admin startproject config . && run python manage.py check
# fastapi / flask: pip install the packages, drop in main.py / app.py from templates.go
```

Then start the server the way the planner does (`python manage.py runserver
0.0.0.0:8000`, `uvicorn main:app --host 0.0.0.0 --port 8000 --reload`,
`flask --app app:app run --host 0.0.0.0 --port 5000 --debug`, and the
production variants `gunicorn config.wsgi:application --bind 0.0.0.0:8000`)
with `-p 18000:8000` and probe it with `curl -H 'Host: my-shop.test'
http://127.0.0.1:18000/` – Django must answer 200 for a foreign host name
(the template sets `ALLOWED_HOSTS = ["*"]`). The whole path through the
binary – template scaffold, wait guard, proxy route, actions, worker, add
and remove – was verified against a real Docker engine when Python support
landed; `make build` and create a project from the *FastAPI* or *Django*
template to repeat it.

## Conventions

- Go: `gofmt`, `go vet`, errors wrapped with `%w`, sentinel errors in the
  package that owns the concept, `context.Context` first parameter, no shell
  commands.
- API: versioned under `/api/v1`, JSON only, error envelope
  `{"error":{"code","message","details"}}`, unknown fields rejected.
- Frontend: strict TypeScript, all server state through TanStack Query hooks in
  `src/api/hooks.ts`, one central client in `src/api/client.ts`, small
  components in `src/components/ui.tsx`.
- Docker resources: always labelled via `docker.ManagedLabels`, names via
  `project.ContainerName` / `project.NetworkName`.

## Adding a runtime version

PHP, Node and Python versions live in `internal/runtime/php_versions.json`,
`node_versions.json` and `python_versions.json` – the single source of truth
for the catalogue (embedded into the binary) and the image build matrices
(`php-images.yml`, `node-images.yml`, `python-images.yml` read them with
`jq`). Normally you never edit them by hand:
`.github/workflows/runtime-versions.yml` runs `scripts/check-versions.py
php|node|python` weekly and opens a PR when upstream changes (Node: newest
LTS becomes the default, EOL "current" releases are dropped; Python: release
candidates appear as `preview` from the `<v>-rc-slim-bookworm` tag).
`base` is the upstream tag (`8.6-rc` for pre-releases), `preview`/`eol` drive
the labels in the UI, `default` is the newest stable version.

Other runtimes (Caddy, later databases) are still defined in
`internal/runtime/catalog.go`. The frontend reads everything from
`/api/v1/runtimes`.

## PHP extensions

`images/php/Dockerfile` compiles every toggleable extension
(`ENVORYX_PHP_EXTENSIONS`) and removes the auto-generated `docker-php-ext-*.ini`
files, so nothing is enabled by default. Envoryx's generated
`zz-envoryx.ini` adds `extension=…` lines for the extensions selected in the
UI. To add one: extend `ENVORYX_PHP_EXTENSIONS`, add it to
`runtime.PHPExtensions()` with `Available: true`, rebuild the images.

## UI languages

The frontend uses `react-i18next`. English is the source language: every
`t("…")` call carries the English text itself as the key, so nothing has to
be maintained for English (except the plural forms in `src/i18n/en.json`).
Other languages are one JSON file each, English text → translation, see
`src/i18n/de.json`. Rules:

- Keep `{{placeholders}}` exactly as in the key (a test enforces this).
- Plural keys carry `_one` / `_other` suffixes (i18next convention). Languages
  with more plural categories (Russian, Ukrainian, Polish) carry `_one` /
  `_few` / `_many` / `_other` for every count string; i18next picks the form
  through `Intl.PluralRules`.
- Interface texts that come from the backend (worker presets, notification
  kinds, provider names) are translated on the client as well – their
  English strings appear in the dictionary like any other key.

To add a language: create `src/i18n/<code>.json`, add it to `languages`
(display name) and `loaders` (dynamic import) in `src/i18n/index.ts`, and to
the `translations` map in `i18n.test.ts` (the tests check that every German
key exists in every language and that placeholders match). Only English is
part of the main bundle; every other dictionary is its own chunk that the
browser fetches when the language is selected. The language selector in the
sidebar lists every entry; the
browser language is detected on first visit and the choice is remembered
per browser (`localStorage`). Shipped: German, French, Spanish, Italian, Dutch,
Polish, Portuguese (Brazil), Russian, Ukrainian.

## Adding a service kind (later phases)

1. Add the `store.ServiceKind` constant and catalogue entry.
2. Extend `project.Planner.Plan` with the container/volume/file plan.
3. Extend `buildProject` / `UpdateRequest` validation.
4. Add planner + lifecycle tests with the fake engine.
5. Expose it in the wizard step "Database & services".

## Releasing

1. Move the entries under `## [Unreleased]` in `CHANGELOG.md` into a new
   `## [x.y.z] – YYYY-MM-DD` section and add the compare/tag links at the
   bottom. Keep the wording user-facing (what changed for someone running
   Envoryx, not which files moved).
2. Commit, then tag and push:

   ```sh
   git tag -a vx.y.z -m "Envoryx x.y.z"
   git push origin main vx.y.z
   ```

3. The `Docker image` workflow builds `ghcr.io/envoryx/envoryx:x.y.z`,
   `:x.y` and `:latest`; the `Release` workflow creates the GitHub release
   with the changelog section as notes (it fails when the section is
   missing). Running instances see the new version through the daily update
   check.

Every push to `main` and every pull request also runs the **upgrade test**
(`.github/workflows/upgrade.yml`, `scripts/upgrade-test.sh`): the latest
published release is started with a sample project, then the candidate image
takes over the same `/config` and `/projects`. It checks the schema
migration, the automatic pre-migrate backup, that the project, its
containers, files, settings, audit log and API token survive, and – when the
schema moved – that the old release refuses to start on the new database.
The script runs on any Docker host (`jq` required):

```sh
scripts/upgrade-test.sh ghcr.io/envoryx/envoryx:0.1.0 ghcr.io/envoryx/envoryx:main
```

Version rules while below 1.0: a **minor** release may change behaviour or
require a migration (Envoryx takes a pre-migrate instance backup itself), a
**patch** release only fixes. Schema migrations are forward-only, so a
release that adds one cannot be downgraded without restoring that backup –
say so in the changelog entry.

## Dependency updates

Dependabot (`.github/dependabot.yml`) opens pull requests every Monday for
Go modules, the web frontend, GitHub Actions and the base images of the
application `Dockerfile`. Minor and patch updates arrive grouped per
ecosystem (one PR each); major updates and security fixes come as separate
PRs. CI, including the upgrade test, runs on every one of them – merge when
green, read the release notes first for majors. Node majors in the
`Dockerfile` are ignored on purpose: only even (LTS) lines are used, and a
move to the next one is done by hand in `Dockerfile` and `ci.yml` together.

The PHP and Node **runtime images** are not covered by Dependabot: their
base tags follow `internal/runtime/*_versions.json`, which the
`Runtime version check` workflow updates from upstream releases (see
"Adding a runtime version").
