# Development

## Prerequisites

- Go 1.27+
- Node.js 22+
- Docker (optional, for real lifecycle tests and running projects)

## Layout

```
cmd/staqio          entry point (serve, healthcheck, version)
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

When Staqio runs directly on your machine (not in a container) it detects
"bare metal" mode and uses the same paths for bind mounts that it sees itself,
so projects work against your local Docker daemon out of the box.

To run the production build (frontend embedded into the binary):

```
make build && STAQIO_CONFIG_DIR=$PWD/.local/config STAQIO_PROJECTS_DIR=$PWD/.local/projects ./bin/staqio
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
invalid paths / ids, Staqio restart (new manager on existing state), container
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

Edit `internal/runtime/catalog.go`. The frontend reads the catalogue from
`/api/v1/runtimes`; nothing else changes. For PHP also add the version to the
matrix in `.github/workflows/php-images.yml` so `ghcr.io/seramos/staqio-php:<v>`
gets built (pre-release versions map to upstream's `-rc` tag via `base`).

## PHP extensions

`images/php/Dockerfile` compiles every toggleable extension
(`STAQIO_PHP_EXTENSIONS`) and removes the auto-generated `docker-php-ext-*.ini`
files, so nothing is enabled by default. Staqio's generated
`zz-staqio.ini` adds `extension=…` lines for the extensions selected in the
UI. To add one: extend `STAQIO_PHP_EXTENSIONS`, add it to
`runtime.PHPExtensions()` with `Available: true`, rebuild the images.

## Adding a service kind (later phases)

1. Add the `store.ServiceKind` constant and catalogue entry.
2. Extend `project.Planner.Plan` with the container/volume/file plan.
3. Extend `buildProject` / `UpdateRequest` validation.
4. Add planner + lifecycle tests with the fake engine.
5. Expose it in the wizard step "Database & services".
