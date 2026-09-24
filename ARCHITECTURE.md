# Envoryx – Architecture

Envoryx is a Docker-native development environment manager for Unraid and Linux
Docker hosts. It runs as a single container, talks to the Docker Engine API and
creates isolated, per-project stacks (PHP, Python, Node, web server, database, cache …).

This document describes the architecture that Phase 1 (Foundation) and Phase 2
(Project Lifecycle) are built on and the decisions that shape later phases.

```
Browser
   │  HTTPS/HTTP + WebSocket
   ▼
Envoryx Frontend (React, embedded in the binary)
   │  /api/v1/*
   ▼
Go API (single binary, single container)
   │
   ├── Auth            (argon2id, server-side sessions, CSRF/origin checks)
   ├── Project Manager (desired state, lifecycle, rollback, reconciliation)
   ├── Runtime Catalog (PHP / Node / Python / DB / cache versions → images)
   ├── Docker Manager  (label-scoped Docker Engine abstraction)
   ├── Backup Manager  (Phase 7)
   └── Audit Log
            │  Docker Engine API (unix socket or socket proxy)
            ▼
       Docker Engine
            │
     ┌──────┼──────────┬─────────┐
     ▼      ▼          ▼         ▼
   web    php      mariadb    redis      (one network per project)
```

---

## 1. Guiding principles

1. **Desired state → reconciliation → actual state.** SQLite stores what the
   user wants. Docker holds what actually exists. Envoryx compares both and never
   trusts the database alone for runtime status.
2. **Label-scoped authority.** Envoryx only ever mutates Docker resources that
   carry `envoryx.managed=true`. Foreign containers are visible read-only in the
   diagnostics view and are never touched.
3. **No user-controlled Docker parameters.** The browser sends *intent*
   (`php: 8.4`, `database: mariadb 11`). The backend translates intent into
   container specs from a fixed catalogue. Mounts, capabilities, privileges,
   networks and images are decided server-side.
4. **Everything persistent lives in `/config` and `/projects`.** Replacing the
   Envoryx image never destroys data.
5. **One binary, one container.** No microservices. The frontend is embedded.

---

## 2. Repository structure

```
.
├── cmd/envoryx/               main package (serve, healthcheck, admin rescue, CLI client)
├── internal/
│   ├── api/                  HTTP handlers (v1), request/response DTOs, errors
│   ├── auth/                 password hashing, sessions, auth middleware
│   ├── audit/                audit log writer
│   ├── config/               environment configuration
│   ├── db/                   SQLite open + embedded migrations
│   ├── docker/               Docker Engine abstraction (interface + moby impl + fake)
│   ├── hostpath/             host-path detection for bind mounts (see §7)
│   ├── instance/             backups of the instance itself (db + config), restore on start
│   ├── manifest/             envoryx.yml: the project manifest's file format
│   ├── project/              project manager: planning, lifecycle, reconciler
│   ├── runtime/              runtime catalogue (versions → images, config)
│   ├── server/               router, middleware, static file serving
│   ├── store/                repositories on top of database/sql
│   ├── stats/                container resource statistics
│   └── validate/             input validation (names, paths, versions)
├── web/                      React + TypeScript + Vite frontend
│   ├── src/
│   │   ├── api/              central typed API client + TanStack Query hooks
│   │   ├── components/       small reusable UI primitives
│   │   ├── features/         feature modules (auth, dashboard, projects, docker)
│   │   ├── layout/           app shell, navigation, theme
│   │   └── lib/              utilities
│   └── dist/                 build output (embedded into the Go binary)
├── deploy/                   docker-compose.yml, Unraid template + icon
├── .github/workflows/        CI (tests) and multi-arch image builds → ghcr.io/envoryx/*
├── images/php/               Envoryx PHP runtime image (all extensions compiled in, toggled per project)
├── Dockerfile                multi-stage build (web → go → alpine)
├── Makefile
├── ARCHITECTURE.md  SECURITY.md  DEVELOPMENT.md  DEPLOYMENT.md  README.md
```

---

## 3. Backend architecture

### 3.1 Technology

| Concern       | Choice                                   | Reason |
|---------------|------------------------------------------|--------|
| Language      | Go 1.27                                   | static binary, first-class Docker client |
| HTTP router   | `net/http` (Go ≥ 1.22 method patterns)    | no framework needed, fewer dependencies |
| Docker client | `github.com/moby/moby/client` v0.6       | maintained successor of `docker/docker/client` |
| SQLite        | `modernc.org/sqlite`                      | pure Go, no CGO, static build, WAL mode |
| Passwords     | `golang.org/x/crypto/argon2` (argon2id)   | modern, memory-hard |
| WebSocket     | `github.com/coder/websocket` (Phase 5)    | small, context-aware |
| Logging       | `log/slog` (JSON in production)           | structured, stdlib |
| IDs           | UUID v4 (`crypto/rand`)                   | required by spec |

### 3.2 Package responsibilities

- **config** – reads environment (`ENVORYX_*`), validates, provides defaults.
- **db** – opens SQLite (WAL, foreign keys on, busy timeout), applies embedded
  SQL migrations in order, records them in `schema_migrations`.
- **store** – one repository type per aggregate (`Users`, `Sessions`,
  `Projects`, `Settings`, `Audit`). Plain SQL, no ORM. Transactions are passed
  explicitly where multi-table writes happen (`Projects.Create` writes project
  + services + env vars in one transaction).
- **auth** – argon2id hashing with per-hash parameters, opaque session tokens
  (256-bit random, stored as SHA-256 hash), idle + absolute timeouts,
  middleware that injects the principal into the request context.
- **docker** – the *only* package that imports the moby client. Exposes a
  narrow `Engine` interface (create/start/stop/remove/list/inspect for
  containers, networks, volumes; image pull; stats; ping/info). All list
  operations used for management are filtered by `envoryx.managed=true`.
  Every mutating call verifies the label on the target first ("guard").
  A `fake` implementation lives in `docker/dockertest` for unit tests.
- **runtime** – the catalogue of supported runtimes and services. Versions are
  data, not code paths: `runtime.Catalog().PHP()` returns versions with image
  references, default extensions and the config generator. The frontend fetches
  `/api/v1/runtimes` and never hard-codes versions.
- **project** – the heart of Envoryx:
  - `Planner` turns a `ProjectSpec` (desired state) into a `ResourcePlan`
    (network, volumes, containers with full Docker specs, config files).
    The plan is what the wizard summary shows before creating.
  - `Manager` executes lifecycle operations (create, duplicate, rename, start,
    stop, restart, delete) with per-project locking, a resource journal and rollback.
    Every detached operation (`ops.go`) is registered in `progress.go`:
    deep call sites report their current step through `step(ctx, …)`
    (an English template with `{{placeholders}}` the UI translates), image
    pulls contribute download progress. `GET /api/v1/operations` lists running
    and recently finished operations, `status.operation` marks a project
    with one in flight; the UI polls this for its progress panel.
  - Duplicating (`duplicate.go`) is the create path with an existing project
    as the source of truth: the desired state is copied, only names, the
    directory and every published host port are new, and the parts the user
    asked for follow – the files as a recursive copy, the database as a dump
    streamed straight into the copy's client, the bucket object by object.
    Database and storage credentials are copied verbatim, so a `.env` that
    lives in the project files keeps working and the dump restores one to one;
    each copy has its own container, network and volume, so nothing is shared.
  - Renaming (`rename.go`) changes the slug everything else is derived from.
    Docker can rename neither containers nor networks nor volumes, so the first
    two are recreated from the new plan and the volumes are copied into their
    new names by a throw-away container from the project's own web image. The
    database follows its dialect (PostgreSQL renames in place, the others move
    the contents through a dump, MongoDB maps the namespace), the bucket's
    objects are copied into the new bucket. Checks come before the first stop,
    the record is renamed before any data moves and put back on failure, and old
    data is dropped only once the new copy is complete.
  - `Reconciler` compares database state with Docker on startup and
    periodically, updating derived status and flagging inconsistencies.
- **hostpath** – resolves the *host* path behind `/projects` and `/config`
  (see §7). Bind mounts for project containers must use host paths.
- **instance** – backups of Envoryx itself (see §10): create/list/import/
  delete, automatic pre-migrate/pre-restore snapshots, restore at next start.
- **logs** – what happens to container output beyond streaming it: the level
  heuristic, the query filter, the error-frequency statistics and the
  persistent history with its collector (see *Logs*).
- **stats** – one-shot container stats with a short cache, aggregated per
  project and for the dashboard.
- **api** – thin handlers: decode → validate → call manager/store → encode.
  Uniform error envelope `{ "error": { "code", "message", "details" } }`.
- **server** – builds the router, middleware chain (recover, request id,
  logging, security headers, auth, CSRF origin check), serves the embedded SPA.

### 3.3 Request flow (example: start project)

```
POST /api/v1/projects/{id}/start
  → auth middleware (session cookie → user)
  → origin/CSRF check (state-changing method)
  → api.Projects.Start: validate UUID
  → project.Manager.Start(ctx, id)
      lock(project)
      load spec from store
      plan := Planner.Plan(spec)
      for each container in plan: ensure exists (guarded), start
      reconcile → status
      audit("project.started")
  → 200 { project }
```

### 3.4 Command line client

The same binary is the client: `envoryx project …`, `envoryx backup …`,
`envoryx db …`, `envoryx git …`, `envoryx up` and `envoryx login` talk to a
running server over `/api/v1` with an API token, exactly like the web interface
and the MCP server. The CLI holds no privilege of its own – it has no database handle, no
Docker socket and no way around a token's scope or project restriction – so
`docker exec envoryx
envoryx project start shop` and the same command from a laptop take the same
path through the API. (The exception is `envoryx admin …`, which is the rescue
path *onto* the database when the credentials are lost.)

One endpoint exists for the CLI's sake: `POST
/api/v1/projects/{id}/services/{kind}/exec` runs a command without a
pseudo-terminal and answers with newline-delimited JSON frames
(`stdout`/`stderr`, then `exit`). The terminal WebSocket next to it is a PTY
for humans, where the two streams are merged and the exit code is lost; scripts
need the opposite. Nothing is written until the first frame, so a refusal (no
such service, container not running) is still an ordinary HTTP error.

---

## 4. Frontend architecture

- **React 19 + TypeScript (strict) + Vite 7.**
- **TanStack Query** for server state (caching, invalidation, polling of
  status/stats); no global client store beyond theme + auth context.
- **react-router** for routes: `/login`, `/setup`, `/` (dashboard),
  `/projects`, `/projects/new`, `/projects/:id`, `/docker`, `/settings`.
- **Tailwind CSS v4** with CSS variables for theming (dark/light, system).
- **Own small component set** (`Button`, `Card`, `Badge`, `StatusDot`,
  `Dialog` on native `<dialog>`, `Input`, `Select`, `Table`, `EmptyState`,
  `ErrorState`, `Skeleton`). No heavy UI framework.
- **Central API client** (`src/api/client.ts`): typed fetch wrapper, error
  normalisation, `credentials: "include"`, automatic redirect to `/login`
  on 401, `X-Requested-With` header on every request (CSRF defence in depth).
- **Feature modules** own their pages, hooks and components. Cross-feature
  reuse goes through `components/`.
- Tests: Vitest + Testing Library (component tests, wizard flow, login flow).

---

## 5. Database schema (migration 0001)

All IDs are UUID strings. Timestamps are RFC 3339 UTC strings.

```sql
users            (id, username UNIQUE, password_hash, role, created_at, updated_at)
sessions         (id /*sha256(token)*/, user_id→users, created_at, expires_at,
                  last_seen_at, ip, user_agent)
projects         (id, name UNIQUE, slug UNIQUE, path, docroot, desired_state,
                  http_port UNIQUE NULL, lifecycle /*creating|ready|deleting|failed*/,
                  last_error, created_at, updated_at)
project_services (id, project_id→projects, kind, variant, version, image,
                  enabled, config /*JSON*/, position, UNIQUE(project_id, kind))
project_environment_variables
                 (id, project_id→projects, key, value, is_secret, created_at,
                  UNIQUE(project_id, key))
domains          (id, project_id→projects, hostname UNIQUE, is_primary, created_at)
backups          (id, project_id, filename, size_bytes, kind, metadata /*JSON*/, created_at)
settings         (key PRIMARY KEY, value, updated_at)
audit_log        (id, created_at, user_id, username, action, target_type,
                  target_id, details /*JSON*/, ip)
schema_migrations(version PRIMARY KEY, applied_at)
```

Notes:

- `projects.path` is stored **relative** to the projects root (`acme-shop`),
  never absolute. The absolute container path and host path are derived.
- `desired_state` is `running` or `stopped`. Runtime status is *derived* from
  Docker and never persisted as truth.
- `lifecycle` tracks long-running transitions so a crash mid-create leaves a
  visible `failed`/`creating` marker instead of a silent half state.
- `project_services.config` holds kind-specific settings as JSON (PHP ini
  values, extensions, DB credentials). Secrets in SQLite are protected by the
  `/config` directory permissions; see SECURITY.md.
- Service kinds in Phase 2: `web` (Caddy) and `php`. Later: `node`,
  `database`, `redis`, …

---

## 6. Docker abstraction and naming

### 6.1 Labels

Every resource Envoryx creates gets:

```
envoryx.managed=true
envoryx.project.id=<uuid>
envoryx.project.name=<slug>
envoryx.service=<kind>          (containers, volumes)
envoryx.version=<envoryx version>
```

### 6.2 Names

```
network    envoryx-<slug>
container  envoryx-<slug>-<service>      e.g. envoryx-acme-shop-php
volume     envoryx-<slug>-<service>      e.g. envoryx-acme-shop-mariadb
```

Slugs match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$` – lower-case DNS-safe.

Inside the project network containers use network aliases: `web`, `php`,
`database`, `redis`, `node`, `python`.

### 6.3 Engine interface (internal/docker)

```go
type Engine interface {
    Ping(ctx) (Info, error)
    // containers
    ListManagedContainers(ctx, projectID string) ([]Container, error)   // label filtered
    ListAllContainers(ctx) ([]Container, error)                          // diagnostics, read-only
    InspectContainer(ctx, id) (ContainerDetails, error)
    CreateContainer(ctx, ContainerSpec) (id string, error)
    StartContainer / StopContainer / RestartContainer / RemoveContainer (guarded)
    ContainerStats(ctx, id) (Stats, error)
    // networks / volumes
    ListManagedNetworks / CreateNetwork / RemoveNetwork (guarded)
    ListManagedVolumes / CreateVolume / RemoveVolume (guarded)
    // images
    EnsureImage(ctx, ref, progress) error
}
```

`ContainerSpec` is a *closed* struct built only by the planner (image, name,
labels, env, mounts, network + aliases, port bindings, restart policy,
user). The Docker implementation adds hardening (no privileged, drop
`CAP_NET_RAW`, `no-new-privileges`, restart policy `unless-stopped`).

"Guarded" means: inspect target, verify `envoryx.managed=true` label and, when a
project ID is supplied, `envoryx.project.id` – otherwise return
`ErrNotManaged` and do nothing.

### 6.4 Socket proxy readiness

The client honours `DOCKER_HOST`. Pointing it to a socket proxy
(`tcp://socket-proxy:2375`) works without code changes. The set of API
endpoints Envoryx needs is documented in SECURITY.md for proxy allow-lists.

---

## 7. Bind mounts and host paths (critical)

Envoryx sees project files at `/projects/<slug>` **inside its own container**.
The Docker daemon, however, resolves bind-mount sources on the **host**. A
project container therefore needs `/mnt/user/development/<slug>` – the host
path – not `/projects/<slug>`.

Solution (`internal/hostpath`):

1. If `ENVORYX_PROJECTS_HOST_PATH` / `ENVORYX_CONFIG_HOST_PATH` are set, use them.
2. Otherwise auto-detect: find Envoryx's own container (hostname = container ID
   prefix, confirmed via `/proc/self/mountinfo` or container inspect by
   hostname), inspect its mounts and read the `Source` of the mount whose
   `Destination` is `/projects` resp. `/config`.
3. If Envoryx is not running inside a container at all (local development with
   `go run`, or a bare-metal install), container paths and host paths are
   identical and the resolver switches to identity mapping.
4. If detection fails inside a container (e.g. Docker was not reachable at
   startup), it is retried lazily on the next project operation. Until it
   succeeds Envoryx runs in a degraded mode: the UI shows a clear configuration
   error and project creation is disabled.

The same mechanism is used for per-project generated config files
(`/config/projects/<id>/...`) which are bind-mounted read-only into project
containers.

---

## 8. Project lifecycle

### 8.1 Desired state (ProjectSpec)

```go
type ProjectSpec struct {
    ID, Name, Slug string
    Path    string          // relative to projects root
    Docroot string          // relative to project path, e.g. "public"
    Services []ServiceSpec  // web, php, (node, database, redis …)
    Env      []EnvVar
    HTTPPort int            // host port for the project web server
}
```

### 8.2 Phase 2 stack

A PHP project consists of two containers from the start:

- `web` – Caddy (default), Apache httpd or Nginx (`internal/runtime/webserver.go`
  renders the config per variant, `WebServerConfig(variant, docroot,
  WebOptions{PHP, SPAFallback})`), serves static files from
  `/var/www/html/<docroot>` and passes PHP to `php:9000` via FastCGI. Apache
  runs with `AllowOverride All` so `.htaccess` files behave as on a shared host;
  Caddy and Nginx route unknown paths to `index.php`. The variant can be
  switched later; the web container is then recreated. Publishes the project's HTTP port on the
  host (auto-allocated from a configurable range, default 20000–20999).
- `php` – `ghcr.io/envoryx/envoryx-php:<version>` (`images/php/Dockerfile`:
  official php-fpm plus all toggleable extensions compiled in but disabled;
  the generated `zz-envoryx.ini` enables the selected ones). Project files
  mounted at `/var/www/html` (same path in both containers so
  `SCRIPT_FILENAME` resolves). The catalogue owns the version→image mapping;
  stored images are refreshed from it on load, so a new runtime image is
  applied on the next restart.

**Projects without PHP.** PHP is optional (`CreateRequest.PHP == nil`); the
web container is not. `internal/project/app.go` is the single source of
truth for the resulting shape: `appService(p)` is the enabled PHP service,
else the enabled Python service, else the enabled Node service, else nil;
`pythonServesApp(p)` is true when there is no PHP and the Python service
runs its application server, `nodeServesApp(p)` when there is neither PHP
nor a Python server and the Node service runs its dev server;
`appServesDirectly(p)` is either; `Serves(p)` yields `php`, `python`, `node`
or `static` (exposed as `serves` in the API and MCP output, `appService`
names the container kind).

- *Static* (`serves=static`, no PHP, Node absent or without dev server): the
  renderers' static branch serves the document root with `index.html` as the
  index, denies dotfiles (`/.env`, `/.git/…` – Caddy and Apache get what
  Nginx already had; the PHP branch is byte-identical to before) and returns
  404 for unknown paths unless `WebServiceConfig{SPAFallback}` (persisted in
  the web service's `Config`, `web.spaFallback` on the wire) rewrites them to
  `/index.html`. SPA fallback is rejected for projects with PHP – the front
  controller already handles unknown paths. The starter page is an
  `index.html` instead of `index.php`.
- *Node dev server* (`serves=node`): the embedded proxy routes
  `<slug>.<base>`, every extra domain and `<slug>-dev.<base>` (kept as an
  alias) to `envoryx-<slug>-node:<port>`; on bare metal it dials
  `127.0.0.1:<node host port>`. The `Running` flag of the route follows the
  node container. The planner leaves the web container's `Ports` empty so
  nothing on the LAN reaches the unfiltered project root (`.env`, sources)
  while the dev server is the application; `HTTPPort` stays allocated and is
  published again as soon as the dev server is turned off (the `Ports` change
  makes `ensurePlan` recreate the web container). The node container's `Cmd`
  is wrapped in an argv-safe guard that waits for `package.json` (a blank
  project would otherwise crash-loop on `npm run dev`); the validated argv is
  passed after `$0`, never interpolated. No healthcheck: the container is
  "running" while it waits, the proxy shows its 502 page until the dev
  server listens. `NodeConfig.Env` sets
  `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.<base>` – exactly one entry,
  because Vite before 8.3 appends the raw variable as a single host; the
  leading dot is Vite's suffix match, so `<slug>.<base>`, `<slug>-dev.<base>`
  and extra domains under the base domain pass without the planner knowing
  the domain table. PHP+Node
  projects keep today's `Cmd`, ports and `specFingerprint`, so no existing
  container is recreated by this change.
- *Python application server* (`serves=python`): the same mechanics with the
  Python container as the upstream (`envoryx-<slug>-python:<port>`, host
  port on bare metal, `Running` follows the python container, web port
  withdrawn). `runtime.PythonConfig` (`internal/runtime/python.go`) selects
  the preset – `django` (`manage.py runserver` / gunicorn), `flask` (`flask
  run --debug` / gunicorn), `asgi` (uvicorn, `--reload` in dev mode), `wsgi`
  (gunicorn) or `module` (`python -m`, HOST/PORT env) – plus `Mode`
  (`dev`|`production`), `App` (`module:attribute`, validated against a
  strict identifier pattern so it can appear in the wait guard) and `Port`.
  The wait guard tests for the entry file (`manage.py`, else
  `<module>.py`/`<module>/`) instead of `package.json`. The container runs as
  PUID:PGID with the project home and `pythonEnv`: `PATH` starts with
  `/var/www/html/.venv/bin` and `/home/envoryx/.local/bin`, so python, pip,
  gunicorn … resolve to the project's `.venv` once it exists, and pip/uv
  caches persist in the home. `PythonConfig.Debug` publishes `DebugPort`
  (debugpy, default 5678) on `DebugHostPort`; debugpy itself is started by
  the application. A Python server next to PHP keeps its host port but no
  route (PHP stays the application); next to a Node dev server the Python
  server takes the project URL and the dev server keeps `<slug>-dev.<base>`.
- Start order is database → php/python/node → web (planner order: database
  and services 5–8, php 10, python 12, node 15, web 20). One-shot
  containers (git, templates) run from `toolImage(p)`: the application
  container's image (PHP, Python or Node), else the catalogue's default Node
  image – git and ssh ship in every Envoryx image. Templates carry `Runtime`
  (`php`|`node`|`python`); a template refuses a request without its runtime
  (`ErrInvalid`).

Why a per-project web container instead of one central proxy speaking FastCGI:
FastCGI details stay inside the project; the future central reverse proxy
(Phase 4) just forwards HTTP by `Host` header to `envoryx-<slug>-web`. Projects
also remain reachable via `http://<host>:<port>` without any DNS setup, which
is the robust default for a remote Unraid server.

### 8.3 Create workflow (transactional with rollback)

```
1. validate request, allocate port, generate IDs        (no side effects)
2. INSERT project (lifecycle=creating) + services       (DB tx)
3. journal := []                                         (created resources)
4. ensure project directory exists (create if allowed)
5. write config files to /config/projects/<id>/         (journal: dir)
6. pull images if missing
7. create network                                        (journal: network)
8. create volumes                                        (journal: volumes)
9. create containers                                     (journal: containers)
10. if desired_state=running: start containers
11. UPDATE project lifecycle=ready
on error at any step ≥ 4:
    rollback journal in reverse order (guarded removes),
    delete config dir, DELETE project row, return structured error
```

Project files in `/projects` are **never** deleted by rollback.

### 8.4 Start / Stop / Restart

- Start: ensure network exists → ensure containers exist (recreate missing
  ones from the plan) → start in dependency order (database, php/node, web) → set
  `desired_state=running`.
- Stop: stop containers (10 s grace) → `desired_state=stopped`.
- Restart: stop + start.

All operations hold the per-project lock; concurrent requests return `409`.

**Database browser** (`internal/project/dbtool.go`): one Adminer container
(`envoryx-dbtool`, label `envoryx.system=dbtool`, so the reconciler never
reports it as an orphan) on its own managed network, which Envoryx's container
also joins. `OpenDBTool` starts it on demand, rewrites
`/config/dbtool/connections.json` from every project's database config
(atomic rename, mounted as a directory), connects the container to the
project network and returns `/dbtool/?server=…&username=…&db=…`. A plugin
mounted into `plugins-enabled/` submits Adminer's login form with the password
from that file (`loginForm` hook) – the browser never sees the credentials.
The API serves `/dbtool/` through a session-protected reverse proxy that
strips the prefix (Adminer's links are relative) and prefixes absolute
`Location` headers; the UI's CSP is not applied there because Adminer sends
its own nonce-based policy. Project deletion detaches the tool before removing
the network; disabling removes container, network and file. On bare metal the
container publishes on 127.0.0.1 instead of a shared network.

**Image history and rollback** (`project_images`, migration 0007): when
`startPlan` recreates a container because the same tag now resolves to a
different local image id, it records `{image, current_id, previous_id,
changed_at}` for the project. `UseImage(id, ref, previous|latest)` sets the
`pinned` flag and recreates the containers; while pinned the plan's
`Spec.Image` is replaced by `previous_id` (Docker accepts ids wherever a tag
goes), so restarts keep the rollback and the status suppresses the "newer
image pulled" hint. `UnusedImages` treats every `previous_id` (and the
`current_id` of pinned records) as in use so a prune cannot take the rollback
target away. Workers share the PHP image and follow its record.

**Detached execution** (`internal/project/ops.go`): create, start, stop,
restart, update, delete, project backup and restore run through
`Manager.run`, which derives the operation context from
`context.WithoutCancel(request ctx)` – the caller's values (principal, IP)
carry over, its cancellation does not, so a closed tab or a dropped
connection never aborts a pull and rolls a project back. Each operation has
an upper bound (`limitProvision` 30 min, `limitStop`, `limitDelete`,
`limitBackup`). At shutdown `Manager.Shutdown(grace)` refuses new operations
(`ErrShuttingDown` → 503), waits `ENVORYX_SHUTDOWN_GRACE` for running ones
and then cancels the shared root context with `ErrInterrupted` as cause;
`opError` turns the resulting `context.Canceled` into that cause so the
project's `last_error` reads "interrupted by an Envoryx restart" instead of
"context canceled". The HTTP server calls it from `BeforeShutdown` before
closing the listener, so handlers waiting on an operation return first.

Project containers are independent of the Envoryx container by default
(`unless-stopped`). With the `projects_follow_envoryx` setting on, the
`BeforeShutdown` hook continues after the drain with
`Manager.StopAllForShutdown`: every project is stopped under its lock through
`stopPlan` (projects in parallel, containers in reverse plan order), then any
managed container still running (database browser, orphans). Desired states
are untouched, so `Manager.ResumeProjects` at the next start – run before the
first reconcile – starts exactly the projects with `desired_state=running`
that are not running (`deriveStatus`), a few at a time through the regular
`Start` operation. A self-requested restart (`requestRestart`) skips the stop:
the process is back in a moment.

### 8.5 Delete

`DELETE /projects/{id}` requires `{"confirm": "<slug>"}` in the body. It
stops and removes containers, removes the network and volumes (all guarded),
removes `/config/projects/<id>`, then deletes the DB rows. Project files are
kept unless `deleteFiles: true` is set explicitly – and even then only the
resolved path under the projects root is removed.

### 8.6 Status derivation

```
running   all enabled services have a running container
stopped   no container running
partial   some running
missing   DB knows the project but expected containers do not exist
error     lifecycle = failed
creating / deleting   transitional
```

### 8.7 Reconciliation

On startup and every 30 s:

1. List all `envoryx.managed=true` containers/networks/volumes.
2. Group by `envoryx.project.id`.
3. For each DB project compute status (§8.6).
4. Resources whose project ID is unknown are reported as **orphans**
   (visible in the Docker view). `cleanOrphans` removes orphaned containers
   (stop, remove) and networks (detach proxy and database browser, remove
   unless a foreign container is attached) once the previous pass already
   listed them and the project lock is free – a project mid-create or
   mid-rollback is never mistaken for an orphan. Volumes are never removed
   automatically; `RemoveOrphan` (`POST /docker/orphans/remove`) removes a
   listed orphan on request. Removals are audited as `docker.orphans_removed`.
   Autonomous actions (orphans removed, projects resumed) are also kept in
   memory as `Manager.Activity()` – served in the dashboard payload and shown
   as a dismissible notice – and sent as notifications of the same kind.
5. Projects with `desired_state=running` but stopped containers are flagged
   (`unexpectedly stopped`) – no automatic restart in Phase 2; the UI shows the
   discrepancy and offers "Start". Reconcile only *reports*: a missing
   container (for example the web container of a Node-only project) shows up
   as `expected running but observed partial`; the next Start recreates it
   through `ensurePlan`.

---

## 9. Security boundaries

Detailed in SECURITY.md. Summary of the enforced boundaries:

| Boundary | Enforcement |
|----------|-------------|
| Docker socket power | only `internal/docker` talks to Docker; closed `ContainerSpec`; label guards on every mutation |
| Browser → Docker | no Docker parameters accepted; API takes catalogue keys and validated strings |
| Container IDs | never accepted raw for mutations; API addresses containers by project ID + service kind; diagnostics list is read-only |
| Paths | relative, segment-validated, resolved with `EvalSymlinks`, must stay under root |
| Mounts | only project dir + generated config dir (read-only) |
| Privileges | never privileged; `no-new-privileges`; no added capabilities |
| Auth | argon2id, server-side sessions, HttpOnly + SameSite=Lax cookie, idle/absolute timeout |
| CSRF | SameSite cookie + `Origin`/`Sec-Fetch-Site` verification + `X-Requested-With` requirement on mutating requests |
| WebSocket (Phase 5) | same session cookie validated at upgrade + origin check |
| Secrets | never logged; DB credentials shown only on explicit request; audit details exclude secrets |
| Destructive ops | confirmation token (slug) in request body |
| Login abuse | per-IP + per-user rate limiting with backoff |

---

## 10. Persistence strategy

```
/config/
  envoryx.db                SQLite (WAL)
  projects/<id>/           generated config per project (web server config, php.ini, pool conf)
  backups/<slug>/          Phase 7
  offsite.json             offsite targets with their credentials (0600)
  ca/                      Phase 8 (0600)
  logs/<id>/<service>/     log history, one file per UTC day (older days gzipped)
/projects/<slug>/          user project files (bind-mounted into project containers)
Docker volumes             database / cache data (named, labelled)
```

Migrations are forward-only SQL files embedded in the binary and applied in
a transaction each; the version table prevents re-application. Downgrading
the image below the schema version is refused with a clear error.

Startup refuses a database that fails `PRAGMA integrity_check` (`db.ErrCorrupt`,
the error names the newest instance backup) and a config directory on a
network filesystem (`config.ValidateStorage`; FUSE only warns, the warning is
shown in Settings).

`internal/instance` backs up the instance itself (database via `VACUUM INTO`,
`ca/`, `ssh/`, `notify.json`, `projects/<id>/` without `home/` caches) into a
single tarball under `<backups>/_instance/`. One is written automatically
before the first pending migration (`db.Options.BeforeMigrate`) and before a
restore. A restore is only recorded (`/config/.restore-pending`) and applied at
the next start before the database is opened; the API asks `main` to restart,
which re-execs the binary in place so the container keeps running regardless
of its restart policy.

`internal/offsite` copies backups off the host. `Target`s (S3-compatible, SFTP,
WebDAV) live in `/config/offsite.json`, so an instance backup brings them back;
`offsite_uploads` records one row per backup and target (pending, running,
done, failed with attempts and the next try). Every backend implements the
same five calls (`Put` a stream of unknown length, `Get`, `List` one
directory, `Delete`, `Close`): S3 through `internal/s3` with a multipart upload
in 16 MiB parts held in memory (a stream that fits one part is a single PUT),
SFTP through `pkg/sftp` with a pinned host key and a `.part` file renamed when
complete, WebDAV through `net/http` with MKCOL, a chunked PUT and PROPFIND.
With encryption on, the archive goes through an age writer (scrypt
passphrase) on its way to `Put` and the object name gets `.age`.

The `Syncer` runs in the background: the project manager's backup hook queues
scheduled backups for automatic targets, a manual request queues any backup;
once a target's hour has come it takes the day's `scheduled` instance backup
and queues it. Each pass works through the due rows, one upload at a time;
failures are retried with growing delays (a vanished local backup is not) and
raise the `backup.failed` notification. After a scheduled upload the target's
scheduled copies beyond its keep count are deleted. Object names carry
everything a listing needs (backup directory or id, contents, source,
encryption), so listing downloads nothing, rotation only ever touches
`scheduled` copies, and a name outside the layout is refused before a fetch or
delete. Fetching a project backup streams the tar back into
`Manager.ImportBackupArchive`, which accepts only the four known member files
of one backup directory and records the backup; an instance backup goes
through `instance.Store.Import` and is restored the usual way.

---

## 11. Error and rollback strategy

- Domain errors are typed (`ErrNotFound`, `ErrConflict`, `ErrValidation`,
  `ErrNotManaged`, `ErrDockerUnavailable`, `ErrBusy`) and mapped to HTTP
  codes (404, 409, 422, 403, 503, 409).
- Every multi-step Docker operation keeps a journal and rolls back on failure.
  Rollback failures are logged and surfaced (`project.lifecycle=failed`,
  `last_error`) rather than hidden.
- The reconciler is the safety net: whatever state a crash leaves behind is
  detected and displayed; no automatic destructive action is taken.
- Background tasks (reconciler, backup scheduler, session purge, certificate
  renewal, SSH, proxy) run under `supervise` in `main`: a panic is logged with
  its stack, reported as an `envoryx.failed` notification and the task is
  restarted with backoff. HTTP handlers have their own `recover` middleware.
  A refused start (corrupt database, network filesystem, newer schema) is
  notified synchronously before the process exits – notification settings are
  a file, so this works without the database.
- `internal/disk` guards space: backups check `Require(dir, need)` before
  writing, a monitor task notifies `storage.low` once per disk until it
  recovers, the dashboard shows usage. In-place database upgrades take a
  dump first (`createBackupLocked`) and are refused when that fails.
- SQLite runs with WAL and `synchronous=FULL`: committed transactions survive
  power loss, not just process crashes. At Envoryx's write volume the extra
  fsync is not measurable.

---

## 12. Test strategy

| Layer | What | How |
|-------|------|-----|
| validate | names, paths, versions, traversal attempts | table-driven unit tests |
| auth | hashing, sessions, expiry, middleware, CSRF | unit tests with in-memory SQLite |
| store | migrations, CRUD, transactions | in-memory SQLite |
| project | planner output, create/start/stop/restart/delete, rollback on failure, reconciliation after "restart", container unexpectedly stopped, unmanaged resources untouched | unit tests against the fake Engine |
| project (no PHP) | `app.go` helpers per shape; Node-only create (web+node, no starter, unpublished web port, wrapped `Cmd`, Vite allow-list); static create (`index.html` starter, published port); SPA fallback rendering and its PHP rejection; routes of `<slug>.<base>` / extra domains / `-dev` following `nodeServesApp` and flipping back to web when the dev server is turned off; injected env in the node container; one-shot image choice for git/templates; SSH user resolution `<slug>` → php → python → node; workers refused without their runtime; `.next/.nuxt/.output/.venv` in backups | unit tests against the fake Engine |
| project (Python) | `python_test.go`: Python-only create (wait guard on the entry file, venv `PATH`, published server port, unpublished web port, no starter), routes and bare-metal dial, SSH users, production mode + debugpy port kept across edits, server off → static, removal takes the Python workers' containers (definition paused), Python + Node dev server (Python takes the project URL, Vite keeps `-dev`), PHP added on top, template defaults merged into the request; `runtime/python_test.go` pins every preset's argv | unit tests against the fake Engine |
| runtime | per-preset ports, `Command()`/`WrappedCommand()`/`Env()`; web configs caddy/apache/nginx × {php, static, static+spa} with the PHP output pinned as golden | table-driven unit tests |
| api / mcp | project without `php` over HTTP (preview, DTO fields `serves`/`appService`, 409 on `PUT php`, 404 on php logs, node terminal), Python project over HTTP (preview ports, config with allocated host ports, python terminal with venv env, Python/Django actions, server off and removal, rejected preset/app), `/runtimes` with `nodePresets`/`pythonPresets` and template runtimes; MCP `phpVersion:"none"` + `nodePreset`, template/runtime errors, `get_logs` default service | httptest + fake Engine |
| api | unauthorized access, validation errors, error envelope, full lifecycle over HTTP | httptest + fake Engine |
| docker | real engine behaviour (labels, guards, foreign containers untouched) | integration tests behind `//go:build integration` (need Docker) |
| web | components, login flow, wizard flow (PHP, Python, Node.js and static stacks, template filtering), project list actions, IDE/Git/Domains tabs per runtime shape, Python server fields and card, i18n parity of all dictionaries | Vitest + Testing Library |
| e2e | lifecycle of a PHP project and of a static project without PHP (`web/e2e`) | Playwright against a Docker host |

---

## 13. Phase plan

### Phase 1 – Foundation
config, db + migrations, store, auth (setup/login/logout/me), docker engine
+ fake, health endpoint, dashboard stats, docker diagnostics, embedded SPA,
Dockerfile + compose.

### Phase 2 – Project Lifecycle
runtime catalogue (PHP versions, Caddy), hostpath detection, planner,
manager (create/start/stop/restart/delete with rollback), reconciler, project
API, wizard, project list + detail pages, lifecycle tests.

### Phase 3 + 6 – Databases and services (MariaDB, MySQL, PostgreSQL, MongoDB, Redis, Mailpit)
`database` service kind with a labelled named volume
(`envoryx-<slug>-database`), healthcheck, start order database → php → web.

Credentials: generated with `crypto/rand` from a shell/URL-safe alphabet,
stored in `project_services.config` (SQLite under `/config`, mode 0600).
They are never part of the normal project payload, logs or audit details; the
explicit `GET /projects/{id}/database/credentials` call is audit-logged.
`DB_*` and `DATABASE_URL` are injected into application containers, user
variables override them, `MARIADB_*`/`MYSQL_*` are reserved.

Database flavours are described by a `runtime.Dialect` (container env, data
directory, healthcheck, client argv + password env, admin statements,
in-place-upgrade capability). Management operations run the flavour's client
inside the container via Docker exec with argv arrays; passwords travel in
`MYSQL_PWD`/`PGPASSWORD`, identifiers are validated (`^[a-z][a-z0-9_]*$`), the
primary database cannot be dropped. Rotating the password recreates the
application containers. Removing the service requires `removeData: true`;
downgrades are refused, MariaDB/MySQL upgrade in place, PostgreSQL major
changes are refused (dump/restore required). MongoDB uses the same
`Dialect` with JavaScript instead of SQL: administrative calls run
`mongosh --nodb --eval` and connect through a URI passed in the environment
(never argv), the owner is the root user (like PostgreSQL), dumps are
`mongodump --archive` streams (the tools accept credentials only via
`--uri`), `MONGODB_URI` is injected in addition to `DATABASE_URL`, and major
upgrades are refused (one step at a time, FCV).

Redis (volume `envoryx-<slug>-redis`, `REDIS_*` injected) and Mailpit (web
inbox on an allocated host port, `MAIL_*`/`MAILER_DSN` injected) are
auxiliary services with a small `{hostPort}` config; env changes recreate the
application containers while stateful services keep running. Memcached
(`memcached:1.6-alpine`, no volume, `MEMCACHED_*` injected) is the same minus the
data: removing it needs no confirmation. RabbitMQ
(`rabbitmq:<version>-management-alpine`, volume `envoryx-<slug>-rabbitmq`) uses
the same config plus `webUiPort` (management UI, always published) and a
generated `username`/`password` that the image applies when it initialises the
volume; `RABBITMQ_*` including `RABBITMQ_URL` is injected, the password only
leaves the backend through `GET /projects/{id}/rabbitmq/credentials` (operate
scope). `RABBITMQ_NODENAME=rabbit@localhost` pins the node name: the data
directory is named after it, and the default contains the container's host
name, which changes with every recreate.

Meilisearch (`getmeili/meilisearch:v<version>`, volume `envoryx-<slug>-meilisearch`)
and Typesense (`typesense/typesense:<version>`, volume `envoryx-<slug>-typesense`)
share that config with a generated `apiKey` (32 characters; Meilisearch's master
key), which only leaves the backend through
`GET /projects/{id}/{meilisearch,typesense}/credentials` (operate scope).
Meilisearch always publishes its port, since the dashboard (`MEILI_ENV=development`)
is served there; it runs with `MEILI_UPGRADE_DB=true`, because a newer image
otherwise refuses a database an older one wrote. Typesense publishes on request and
pins `TYPESENSE_PEERING_ADDRESS=127.0.0.1`: its single-node Raft state in the volume
names the peering address, which would otherwise be the container's changing IP.
Its image has neither curl nor wget, so the healthcheck talks HTTP through bash's
`/dev/tcp`. The application gets Laravel Scout's names (`MEILISEARCH_HOST`/`KEY`,
`TYPESENSE_HOST`/`PORT`/`PROTOCOL`/`API_KEY`) plus `MEILISEARCH_URL`/`API_KEY`
(Symfony's meilisearch-bundle) and `TYPESENSE_URL`; `SCOUT_DRIVER` is left to the
application, like `MESSENGER_TRANSPORT_DSN` for RabbitMQ.

OpenSearch (`opensearchproject/opensearch:<version>`, volume
`envoryx-<slug>-opensearch`) runs as a development node: `discovery.type=single-node`
(which also skips the production bootstrap checks, so the host needs no
`vm.max_map_count`), `DISABLE_SECURITY_PLUGIN=true` for plain HTTP without login or
demo certificates, and a 512 MB heap (about 1 GB of RAM in total). With no login there
is no key and no credentials endpoint; the port is published on request, like
Redis's. The healthcheck waits for yellow, the normal state of a single node whose
replicas have nowhere to go. `OPENSEARCH_HOST`/`PORT`/`SCHEME`/`URL` is injected;
`ELASTICSEARCH_*` is not, because current Elasticsearch clients refuse OpenSearch.

OpenSearch Dashboards is a service of its own (`opensearch-dashboards`, so status,
logs, image pulls and removal work as for any service) that the API only exposes as
`dashboards` on the OpenSearch request and update. `syncOpenSearchDashboards` keeps it
on OpenSearch's version (Dashboards refuses another one; the catalogue lists the same
versions), removes it with OpenSearch and switches it on or off when an update carries
`dashboards` (absent leaves it). It has no volume – saved objects live in OpenSearch's
`.kibana` index –, its web UI (5601) is always published and appears as `webUiPort` on
the OpenSearch entry of `/extras`, and it reaches OpenSearch at
`http://opensearch:9200` inside the project network with its security plugin off.

### SSH (`internal/sshd`)
`golang.org/x/crypto/ssh` server with an Ed25519 host key. Auth resolves the
user name through `Manager.ResolveSSHUser` (`<slug>` → the application
container: PHP when present, else Python, else Node; `<slug>.php` /
`<slug>.python` / `<slug>.node` pick one explicitly; a project with none is
`ErrNotFound`) and validates either an API token (password) or an authorized key
from the settings. Session channels map `pty-req/shell/exec` to
`Engine.OpenTerminal` (PTY) or `Engine.ExecStream` (pipes, now with
`WorkingDir`) in the target container as PUID:PGID, `subsystem sftp` to a
`pkg/sftp` request server over `projectFS`, which serves `/var/www/html`
and `/home/envoryx` from the Envoryx-side directories of the same bind mounts
and chowns created files. `/home/envoryx` is a new persistent per-project
home (`/config/projects/<id>/home`) mounted into php/python/node/worker containers;
tool caches and IDE helpers live there. Container specs now carry a
`envoryx.spec` fingerprint label (command, mounts, ports, …) so `ensurePlan`
recreates containers whose structure changed (e.g. the new home mount).
`direct-tcpip` channels (IDE tunnels) are accepted only for projects with
`ide_gateway` set (migration 0006) and only to localhost ports. The IDE
backend binds to 127.0.0.1 inside the container, so the tunnel is relayed by
`socat STDIO TCP:127.0.0.1:<port>` run via docker exec in the container's own
network namespace (probed once per container id); runtime images without
socat fall back to dialling `envoryx-<slug>-<kind>:<port>` over the project
network, which only reaches listeners on 0.0.0.0. The flag also
mounts `/config/jetbrains` at `~/.cache/JetBrains` so Gateway backends are
shared across projects. `StopIDEBackend` runs `pkill -f /.cache/JetBrains/`
as the project user.

### Workers
`project_workers` (migration 0005: name, preset, args, enabled) hold
long-running processes. Presets are a closed catalogue in `workers.go`
(argv builders; the single user argument is validated per preset – queue
names, relative script paths, composer and npm script names, Python module
paths). Every preset names its `Runtime` (`php`, `node` or `python`); the
planner emits one container per enabled worker from that runtime's image
(`Kind` and service label `worker:<id>`, name
`envoryx-<slug>-worker-<name>`, order 30, project env, PUID:PGID,
`unless-stopped`; PHP workers get the php.ini mount, Node and Python
workers the tool env, the project home and – for Python – the venv `PATH`),
so `ensurePlan`, start/stop, env
recreation and delete treat them like any other container. A worker whose
runtime the project lacks is skipped by the planner – it comes back when
the runtime is added – and `AddWorker`/`UpdateWorker` refuse it with
`ErrConflict`. Status lists them as kind `worker` with `workerId`;
logs/terminal accept `worker:<id>`.

### Cron jobs
`project_cron_jobs` (migration 0009: name, runtime, schedule, command,
timeout, enabled) are commands run on a schedule; `project_cron_runs` keeps
the last 20 runs per job with status, exit code and the last 64 KB of the
combined output. `internal/cron` parses the five-field crontab format (lists,
ranges, steps, month/day names, `@hourly` … `@yearly`, Vixie's either-day
rule) and computes the next fire time in Envoryx's zone (`TZ`); schedules that
never fire (30 February) are refused.

There is no cron daemon in the images and no extra container: a run is a
`docker exec` into the running application container of the job's runtime –
`timeout -s TERM -k 10 <secs> sh -c <command>` as PUID:PGID in
`/var/www/html`, with the container's env plus `CI=1` and
`ENVORYX_CRON_JOB`. The command is free text on purpose (the user could run
the same in the terminal); what keeps it contained is who runs it and where:
the project owner inside that project's container, defined only with admin
scope (run now and the output need operate). coreutils' `timeout` ends the
command inside the container, which a cancelled exec would not; exit 124/137
is `timed_out`.

`RunCronScheduler` ticks every 15 seconds. Each job's next time comes from its
schedule when the scheduler first sees it (the current minute still counts)
and after every run, so a minute never fires twice, not even in the hour a DST
change repeats. Jobs only fire while the project is meant to be running and
ready; missed runs are not caught up. A job that is still running is skipped
(logged), at most 8 runs go at once. Runs left `running` by a crash become
`interrupted` at start. Failures raise `cron.failed` (per job, cleared by the
next success); manual runs are audited (`cron.run`), scheduled ones are not.
Duplicating with workers copies the jobs without their history.

PHP itself can be added and removed after creation (`PHPUpdate.Enabled`,
`applyPHPUpdate` in lifecycle.go): adding inserts the service (position
10) and drops the web service's SPA fallback; removing deletes the
service, the PHP container and the PHP workers' containers. The web
configuration and the proxy routing follow from the service list on the
next `update()` pass, which regenerates the config files and restarts.

The Node dev server has two modes (`NodeConfig.Mode`): `dev` runs the
script; `production` wraps it – `sh -c '<pm> run <build> && NODE_ENV=production
exec "$@"'` – so every start builds first and only the serve process sees
`NODE_ENV=production`. `NodeConfig.Inspect` publishes `InspectPort` (default
9229) on `InspectHostPort`; the inspector is started by the user's script,
never through a container-wide `NODE_OPTIONS`, which would attach to the
package manager's own node process.

Python is added, changed and removed the same way (`PythonUpdate`,
`applyPythonUpdate`, position 12): host ports for the server and debugpy
are kept across edits and allocated when the server or debugpy is switched
on; removing Python takes its container and the Python workers' containers
with it, the worker definitions survive as "paused".

### Logs
Container output comes from Docker's `json-file` driver (10 MB × 3 per
container). `project.QueryLogs`, `ExportLogs` and `LogStats` read a service's
lines through one `scanLogs` source with the query's `since`/`until` handed to
Docker; an unfiltered query also hands over its line count as `tail`, a
filtered one reads the range and keeps the last N matches in a ring
(`logs.Ring`). `logs.Classify` guesses the level from the text – a JSON or
logfmt `level` field wins, otherwise whole words (`fatal`, `error`,
`exception`, `traceback`, `warning`, `deprecated` …, never `errors` or
`error_log`) and a 5xx status in access log lines. `logs.Stats` counts per
minute and picks the bucket size (1 minute … 1 day, under 96 bars) when the
range is known; errors and warnings are grouped by `logs.Pattern`, the text
with times, UUIDs, hex ids and numbers masked (at most 2000 patterns). The
download is streamed (`bufio`, no size limit); an error after the first byte
ends the file with a `# envoryx: export aborted` line. The live WebSocket
applies the same filter and sends each line's level.

The history (`logs.Store`, `/config/logs/<project id>/<service>/<YYYY-MM-DD>.jsonl`,
one `{"t","s","m"}` object per line, UTC days) is keyed by project and service,
not by container, which is what makes it survive a recreated container.
`logs.Collector` runs in `RunLogHistory` (every 5 s): it lists the managed
containers of the Logs tab's services (`IsLogService`, not git/template/move
helpers), follows each running one it does not follow yet and reads each
stopped one once to the end – output from while Envoryx was down. Every
reader starts at its service's newest stored line + 1 ns (Docker's `since` is
inclusive) and drops older lines, so a restart of Envoryx or a recreated
container whose Docker log starts over stores nothing twice; nothing older
than the retention or the last clear (`.cleared`) is collected. Lines are
batched (1 s or 2000 lines, flushed in order) and appended with `O_APPEND`.
Every hour `Prune` gzips the days before yesterday (a late write to a
compressed day goes into a plain file next to it and is read with it), drops
days beyond `log_history_days` (default 7), then the oldest days across all
projects beyond `log_history_max_mb` (default 1024), and removes the history
of projects that no longer exist; `Delete` removes a project's right away.
Queries use the history once it holds lines for the service
(`LogPage.source` = `history`, else `container`); the tail of an unfiltered
query without `since` reads the newest days backwards and only counts older
ones. The directory is outside instance backups.

### Notifications
`internal/notify` is a small `Sender` (`Notify(ctx, Event)`, `Clear(key)`)
with providers webhook/ntfy/Discord/Slack/Telegram/SMTP, per-kind cooldowns
and asynchronous best-effort delivery. Sources: the reconciler (first issue
per project → `project.unhealthy`, recovery → info event and cooldown
reset), `Create` rollbacks (`project.failed`), `CreateBackup` errors
(`backup.failed`), the ACME manager (`acme.failed`/`acme.renewed`) and
startup. Config with secrets in `/config/notify.json` (0600); the API never
returns secrets and keeps stored ones when a request leaves them empty.

### Templates
`project.Templates()` is a closed list: Laravel, Symfony, WordPress
(`Runtime: "php"`), Vite + React (TypeScript), Next.js (App Router,
TypeScript), Nuxt (`Runtime: "node"`) and Django, Flask, FastAPI
(`Runtime: "python"`). A template is a sequence of argv steps
run in transient containers from the image of the runtime it names as
PUID:PGID with the project directory mounted
(`RunOneShot`, label `envoryx.service=template`, default bridge network for
composer/npm downloads) plus files Envoryx writes afterwards (WordPress
`wp-config.php` reading the injected `DB_*` variables, random salts). Node
scaffolds mount the project directory at `/tmp/<slug>` instead of
`/var/www/html` because `create-next-app` checks that the parent directory
is writable, run with `HOME=/tmp`, `npm_config_cache=/tmp/.npm`, `CI=1` and
the corepack/npm prompts disabled, and carry Node defaults (`DevServer`,
`Preset`, `Port`, `Script`, docroot `dist` for Vite) that fill an empty
`NodeRequest.Config`. Python scaffolds create `/var/www/html/.venv` first
and run the following steps with the venv first on `PATH` (pip installs
into it, never into the image), pin the result with `pip freeze >
requirements.txt` and carry `PythonConfig` defaults (`Server`, `Preset`,
`Port`, `App`) that fill an empty `PythonRequest.Config`; the Django
template patches the generated `settings.py` for the proxy (`ALLOWED_HOSTS
= ["*"]`, `SECURE_PROXY_SSL_HEADER`, `USE_X_FORWARDED_HOST`,
`CSRF_TRUSTED_ORIGINS` from the environment) and reads the injected
`DATABASE_URL` through `dj-database-url`. The directory must be empty (like
a clone); templates
set the document root and add required PHP extensions (`mysqli` for
WordPress) and may require a database. A failing step rolls the whole
creation back.

### Resource limits
`store.ResourceLimits` (JSON column `projects.limits`) holds one `LimitSet`
(CPU cores, MiB) for the application containers (web, php, node, python,
`worker:*`) and one for the services, plus a process limit; `LimitGroup` maps a
container kind to its group. The planner puts the resulting `docker.Resources`
on every container spec – outside `specFingerprint`, so a changed limit never
recreates anything by itself. `ensurePlan` compares the running limits
(`InspectContainer`) with the plan and calls `UpdateResources` (docker update,
memory swap = memory, PIDs default 4096); only lifting a CPU or memory limit
comes back as `ErrNeedsRecreate`, because Docker reads 0 as "unchanged", and
the container is recreated. `SetLimits` stores and applies through the same
path. `RunOOMWatcher` follows Docker's `oom` events (label-filtered, reconnects
when the stream breaks), keeps the latest kill per container for 24 h as a
status warning and sends `project.oom`. The project stats endpoint returns
each running container with its group and limits for the usage bars.

### Project manifest
`internal/manifest` is the file format of `envoryx.yml` and nothing else:
strict YAML (unknown keys are errors, `redis: true` or a mapping), a format
`version`, checks that need no server (names, env keys, duplicates) and a
deterministic writer. Whether a version exists or an extension is available is
judged by the server that applies the file.

`internal/project/manifest.go` compares both directions through one exporter:
`exportState` turns a stored project into a manifest (host ports, credentials,
the repository and settings at their default left out, versions pinned);
`desiredState` builds the manifest into a project the way Create would
(`buildProject`, `buildWorker`, `buildCronJob` – versions resolved, configs
normalised, everything validated, no side effects) and exports that. The plan
is the section-by-section difference of the two exports, so a project created
from a manifest is in sync with it by construction, and the exported file of a
project plans to nothing. Applying maps the plan onto the existing operations:
one `Update` for docroot, web server, runtimes, services and environment (one
restart), then workers, cron jobs and domains through their own methods.
Removals are only planned as *skipped* unless `Prune` is set; a database type
change is a removal plus an add (two updates), a database downgrade is skipped.
Environment merges rather than replaces: plain values come from the file,
secrets keep the project's value unless one is given, unknown variables stay
unless pruned.

`CreateFromManifest` is `Create` with the manifest's request (secrets from the
caller, empty otherwise) followed by an apply for workers, cron jobs and
domains, then the start; a failure after the create names the project and is
repaired by running `envoryx up` again. `CreateFromRepository` (the wizard's
*Use the repository's envoryx.yml*) creates without starting, reads the cloned
file through an `os.Root` of the project directory (a symlink in the repository
cannot point the server at another file) and applies it with prune – the fresh
project has no data yet – before it starts. `envoryx up` reads the file
locally, sends it as text and lets the server clone `origin` at the current
branch; it never uploads files.

### Phase 4 + 8 – Domains, embedded proxy, HTTPS (implemented)
The proxy lives in the Envoryx binary (`internal/proxy`): two listeners
(`ENVORYX_PROXY_HTTP` `:80`, `ENVORYX_PROXY_HTTPS` `:443`) in front of an
`httputil.ReverseProxy` per upstream. A `Router` caches a routing `Table`
(2 s TTL, invalidated by the API after changes) built by
`Manager.RouteTable`: `<slug>.<base>` for every project, extra names from the
`domains` table, `envoryx.<base>` plus the public host for the UI. Unknown
names → 404 page, stopped project → 503 page, IPs/empty host → UI. Upstreams
are `envoryx-<slug>-web:80`; to reach them the Envoryx container is connected to
every project network (`ConnectNetwork` on create/ensure/reconcile,
disconnect before the network is removed). On bare metal the upstream is
`127.0.0.1:<httpPort>`. Host-side ports are discovered from the container's
own port bindings (`PortBindings(selfID)`), so non-standard mappings work and
the UI can warn when nothing is published.

TLS (`internal/tlsca`): an ECDSA P-256 CA under `/config/ca` (`ca.key`
0600), leaf certificates issued lazily per SNI name and cached on disk
(`certs/<host>.pem`, 397 days), `GetCertificate` restricted to names in the
routing table. An operator-supplied certificate (`custom.crt/key`) wins for
the names it covers. `force_https` (settings) redirects HTTP → HTTPS except
for bare IPs. `internal/acme` optionally obtains a public wildcard
certificate through Let's Encrypt (dns-01 via a `DNSProvider` interface –
Cloudflare, Hetzner Cloud API, netcup CCP, Route 53, DigitalOcean, Porkbun –
on `golang.org/x/crypto/acme`, no extra dependency), stores it as the tlsca
custom certificate and renews it 30 days before expiry in a background loop;
config and credentials under `/config/ca/acme.json` (0600). `ProviderList`
declares each provider's credential fields (the UI builds the form from it,
secrets never leave the server) and how long its name servers take. The
wildcard and the bare domain put two values on the same
`_acme-challenge` name: providers with record ids add and delete their own
record, the RRSet APIs (Route 53, Hetzner) add a value to the set and take
only that value away again. Propagation is checked at every authoritative
name server of the zone (found through 1.1.1.1), not at a resolver, so an
early NXDOMAIN cannot be cached against the check. Route 53 requests are
signed by `internal/awssig`, the SigV4 signer shared with `internal/s3`.

- **Phase 5 DX** (logs and terminal implemented): log streaming and PTY
  terminal over WebSocket – session cookie validated before the upgrade,
  same-origin enforced, containers resolved from `project + service kind`
  server-side; terminal shells in php/node run as PUID:PGID with `HOME=/tmp`
  and tool caches under `/tmp`. Project actions are a closed
  catalogue of argv commands (`actions.go`) gated by required files in the
  project directory; output streams over the same WebSocket mechanism and
  Ctrl+C is delivered on cancel/disconnect. `ListActions` only lists
  catalogue entries whose service the project has (a PHP-only project shows
  no npm actions, a Node-only project no composer/artisan ones, Python
  projects get pip/uv/Django entries); each runs in the matching runtime
  container, `node:version` and `python:version` are the counterparts of
  `php:version`. Git runs in a transient container from the project's
  runtime image (`toolImage`: PHP, else Python, else Node; `RunOneShot`) with the deploy
  key mounted only there; tokens travel via `GIT_CONFIG_*` env. The Node
  service is an idle tooling container (`sleep infinity`, runs as PUID:PGID)
  from `ghcr.io/envoryx/envoryx-node:<v>` until the dev server is enabled.
  Dev-server mode (`runtime.NodeConfig`, stored in the service config): the
  package.json script becomes the container's main process (argv from the
  ordered preset list `runtime.NodePresets` – Vite, Next.js, Nuxt flags or
  HOST/PORT env only, each with a default port – script names validated), a
  host port is allocated like for other services and the proxy routes
  `<slug>-dev.<base>` to `envoryx-<slug>-node:<port>` (WebSocket/HMR passes
  through; Vite's host allow-list is set via
  `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS`). Without PHP the dev server is
  the project's application and `<slug>.<base>` routes there too (§8.2).
  Config changes remove the node container so `ensurePlan` recreates it.
- **Phase 7 Backups** (implemented): `<backups dir>/<slug>/<timestamp-id>/`
  (`/backups` when mounted, else `/config/backups`; `ENVORYX_BACKUPS_DIR`)
  with `backup.json` (metadata + full project export incl. credentials),
  `database.sql.gz` (dump streamed from the database container via exec,
  password in env) and `files.tar.gz` (written by Envoryx; `vendor/`,
  `node_modules/` and the framework build caches `.next/`, `.nuxt/`,
  `.output/` skipped unless requested). Restore requires the slug as
  confirmation, verifies the dump flavour matches the project's database,
  pipes the dump back through the flavour's client, and extracts files with
  tar-slip protection (entries and symlink targets must stay inside the
  project directory; never writes through an existing symlink). Records live
  in the `backups` table; a download streams the directory as one tar.
  Schedules (migration 0004: `backup_schedule/hour/weekday/keep/include_deps/
  last_run` on projects) are driven by a one-minute ticker: a project is due
  when the last run precedes the most recent slot; the run is recorded before
  the backup so failures wait for the next slot; retention deletes the oldest
  backups with `meta.source == "scheduled"` beyond `keep`.
  *Snapshots* (`snapshot.go`) are the same backups with the database as their
  only content and `meta.source == "snapshot"`: `CreateSnapshot` /
  `RestoreSnapshot` wrap the dump and the import in `withServiceRunning` +
  `waitForDatabase`, so a stopped project is started for them and stopped
  again, and `ListSnapshots` returns every backup of `kind == "database"` (a
  pre-upgrade dump included). `pruneSnapshots` keeps the newest
  `snapshotKeep` (10) with `source == "snapshot"` and never touches the rest.
  `CloneDatabase` copies another project's primary database into this one with
  `copyDatabase`/`streamDump` (the duplicate path: dump piped into the other
  container's client, no temporary file): both projects are locked – source
  first, as when duplicating – both must be `ready` and run the same variant,
  the target's identifier confirms, and the target is snapshotted first
  unless the request says otherwise. Audit: `database.cloned`.
- **Phase 9 MCP** (implemented, `internal/mcpserver`): an embedded MCP server
  (official `modelcontextprotocol/go-sdk`, streamable HTTP, stateless, JSON
  responses) mounted at `/mcp` outside the cookie/CSRF scheme. It only
  accepts personal API tokens (`Authorization: Bearer stq_…`; migration
  `0003_api_tokens`, SHA-256 hashes, created/revoked in Settings, audit
  entries `token.created/revoked`; audit rows of tool calls carry
  `user (token: name)`). The same tokens are accepted by the REST API and
  the SSH server: `auth.Middleware` prefers a bearer header over the session
  cookie and never falls back to the cookie when the bearer is invalid;
  `csrfMiddleware` skips the origin/`X-Requested-With` checks for bearer
  requests because `Authorization` is not CORS-safelisted (a browser cannot
  send it cross-site without a preflight, which only allowed origins get).
  Password changes and token create/revoke refuse token principals. Tools call the same `project.Manager` methods as the
  REST API – validation, label guards, locks and audit apply unchanged:
  `list_projects`, `get_project`, `list_runtimes`, `create_project`
  (`phpVersion: "none"` for a project without PHP; `nodePreset`,
  `nodeScript`, `nodePort`, `nodePackageManager` for the dev server; the
  output carries `serves`, `devUrl` and a `directUrl` that is the node host
  port when the dev server serves the project),
  `start/stop/restart_project`, `get_logs` (default service = the
  application container: php, else node, else web; optional `since`, `until`,
  `query`, `level`), `get_log_stats` (error frequency and the most frequent
  errors of a range), `list_actions`, `run_action`
  (runs a catalogue action to completion, returns stripped output + exit
  code, 20 min limit), `list/create_database`, `list/create_backup`,
  `list/create_snapshot`, `add_domain`. Deleting projects, dropping databases,
  restoring backups or snapshots and cloning a database over another are
  deliberately not exposed. Manager errors become tool errors
  (`isError`) so the assistant can react instead of the session failing.
