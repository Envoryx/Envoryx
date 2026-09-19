# Development

## Prerequisites

- Go 1.27+
- Node.js 22+
- Docker (optional, for real lifecycle tests and running projects)

## Layout

```
cmd/envoryx          entry point (serve, healthcheck, version)
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

## Tests

```
make test              # go vet + go test -race + tsc + vitest
make test-go
make test-web
make test-integration  # needs a Docker daemon; creates/removes small alpine containers
```

Backend tests use an in-memory SQLite database and a fake Docker engine
(`internal/docker/dockertest`) that supports failure injection, foreign
containers and state manipulation. Covered flows include: create, start, stop,
restart, delete, rollback after a failed container create, Docker unavailable,
invalid paths / ids, Envoryx restart (new manager on existing state), container
unexpectedly stopped, orphaned resources, unauthorized requests and CSRF.

Frontend tests (Vitest + Testing Library) cover the login/setup flow, the
project list with actions and the complete wizard including preview and
validation errors.

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

PHP and Node versions live in `internal/runtime/php_versions.json` and
`node_versions.json` – the single source of truth for the catalogue (embedded
into the binary) and the image build matrices (`php-images.yml`,
`node-images.yml` read them with `jq`). Normally you never edit them by hand:
`.github/workflows/runtime-versions.yml` runs `scripts/check-versions.py php|node`
weekly and opens a PR when upstream changes (Node: newest LTS becomes the
default, EOL "current" releases are dropped).
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
- Plural keys carry `_one` / `_other` suffixes (i18next convention).
- Interface texts that come from the backend (worker presets, notification
  kinds, provider names) are translated on the client as well – their
  English strings appear in the dictionary like any other key.

To add a language: create `src/i18n/<code>.json`, import it in
`src/i18n/index.ts` and add it to `languages` (display name) and
`resources`. The language selector in the sidebar lists every entry; the
browser language is detected on first visit and the choice is remembered
per browser (`localStorage`). To find untranslated keys, run
`node -e` over the sources or copy the check from `i18n.test.ts`.

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

Version rules while below 1.0: a **minor** release may change behaviour or
require a migration (Envoryx takes a pre-migrate instance backup itself), a
**patch** release only fixes. Schema migrations are forward-only, so a
release that adds one cannot be downgraded without restoring that backup –
say so in the changelog entry.
