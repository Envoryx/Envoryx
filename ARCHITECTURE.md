# Staqio – Architecture

Staqio is a Docker-native development environment manager for Unraid and Linux
Docker hosts. It runs as a single container, talks to the Docker Engine API and
creates isolated, per-project stacks (PHP, web server, database, cache, Node …).

This document describes the architecture that Phase 1 (Foundation) and Phase 2
(Project Lifecycle) are built on and the decisions that shape later phases.

```
Browser
   │  HTTPS/HTTP + WebSocket
   ▼
Staqio Frontend (React, embedded in the binary)
   │  /api/v1/*
   ▼
Go API (single binary, single container)
   │
   ├── Auth            (argon2id, server-side sessions, CSRF/origin checks)
   ├── Project Manager (desired state, lifecycle, rollback, reconciliation)
   ├── Runtime Catalog (PHP / Node / DB / cache versions → images)
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
   user wants. Docker holds what actually exists. Staqio compares both and never
   trusts the database alone for runtime status.
2. **Label-scoped authority.** Staqio only ever mutates Docker resources that
   carry `staqio.managed=true`. Foreign containers are visible read-only in the
   diagnostics view and are never touched.
3. **No user-controlled Docker parameters.** The browser sends *intent*
   (`php: 8.4`, `database: mariadb 11`). The backend translates intent into
   container specs from a fixed catalogue. Mounts, capabilities, privileges,
   networks and images are decided server-side.
4. **Everything persistent lives in `/config` and `/projects`.** Replacing the
   Staqio image never destroys data.
5. **One binary, one container.** No microservices. The frontend is embedded.

---

## 2. Repository structure

```
.
├── cmd/staqio/               main package (serve, healthcheck)
├── internal/
│   ├── api/                  HTTP handlers (v1), request/response DTOs, errors
│   ├── auth/                 password hashing, sessions, auth middleware
│   ├── audit/                audit log writer
│   ├── config/               environment configuration
│   ├── db/                   SQLite open + embedded migrations
│   ├── docker/               Docker Engine abstraction (interface + moby impl + fake)
│   ├── hostpath/             host-path detection for bind mounts (see §7)
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
├── .github/workflows/        CI (tests) and image build → ghcr.io/seramos/staqio
├── images/php/               Staqio PHP runtime image (all extensions compiled in, toggled per project)
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

- **config** – reads environment (`STAQIO_*`), validates, provides defaults.
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
  operations used for management are filtered by `staqio.managed=true`.
  Every mutating call verifies the label on the target first ("guard").
  A `fake` implementation lives in `docker/dockertest` for unit tests.
- **runtime** – the catalogue of supported runtimes and services. Versions are
  data, not code paths: `runtime.Catalog().PHP()` returns versions with image
  references, default extensions and the config generator. The frontend fetches
  `/api/v1/runtimes` and never hard-codes versions.
- **project** – the heart of Staqio:
  - `Planner` turns a `ProjectSpec` (desired state) into a `ResourcePlan`
    (network, volumes, containers with full Docker specs, config files).
    The plan is what the wizard summary shows before creating.
  - `Manager` executes lifecycle operations (create, start, stop, restart,
    delete) with per-project locking, a resource journal and rollback.
  - `Reconciler` compares database state with Docker on startup and
    periodically, updating derived status and flagging inconsistencies.
- **hostpath** – resolves the *host* path behind `/projects` and `/config`
  (see §7). Bind mounts for project containers must use host paths.
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

- `projects.path` is stored **relative** to the projects root (`shimly-api`),
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

Every resource Staqio creates gets:

```
staqio.managed=true
staqio.project.id=<uuid>
staqio.project.name=<slug>
staqio.service=<kind>          (containers, volumes)
staqio.version=<staqio version>
```

### 6.2 Names

```
network    staqio-<slug>
container  staqio-<slug>-<service>      e.g. staqio-shimly-api-php
volume     staqio-<slug>-<service>      e.g. staqio-shimly-api-mariadb
```

Slugs match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$` – lower-case DNS-safe.

Inside the project network containers use network aliases: `web`, `php`,
`database`, `redis`, `node`.

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

"Guarded" means: inspect target, verify `staqio.managed=true` label and, when a
project ID is supplied, `staqio.project.id` – otherwise return
`ErrNotManaged` and do nothing.

### 6.4 Socket proxy readiness

The client honours `DOCKER_HOST`. Pointing it to a socket proxy
(`tcp://socket-proxy:2375`) works without code changes. The set of API
endpoints Staqio needs is documented in SECURITY.md for proxy allow-lists.

---

## 7. Bind mounts and host paths (critical)

Staqio sees project files at `/projects/<slug>` **inside its own container**.
The Docker daemon, however, resolves bind-mount sources on the **host**. A
project container therefore needs `/mnt/user/development/<slug>` – the host
path – not `/projects/<slug>`.

Solution (`internal/hostpath`):

1. If `STAQIO_PROJECTS_HOST_PATH` / `STAQIO_CONFIG_HOST_PATH` are set, use them.
2. Otherwise auto-detect: find Staqio's own container (hostname = container ID
   prefix, confirmed via `/proc/self/mountinfo` or container inspect by
   hostname), inspect its mounts and read the `Source` of the mount whose
   `Destination` is `/projects` resp. `/config`.
3. If Staqio is not running inside a container at all (local development with
   `go run`, or a bare-metal install), container paths and host paths are
   identical and the resolver switches to identity mapping.
4. If detection fails inside a container (e.g. Docker was not reachable at
   startup), it is retried lazily on the next project operation. Until it
   succeeds Staqio runs in a degraded mode: the UI shows a clear configuration
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

- `web` – Caddy, serves static files from `/var/www/html/<docroot>` and passes
  PHP to `php:9000` via FastCGI. Publishes the project's HTTP port on the
  host (auto-allocated from a configurable range, default 20000–20999).
- `php` – `ghcr.io/seramos/staqio-php:<version>` (`images/php/Dockerfile`:
  official php-fpm plus all toggleable extensions compiled in but disabled;
  the generated `zz-staqio.ini` enables the selected ones). Project files
  mounted at `/var/www/html` (same path in both containers so
  `SCRIPT_FILENAME` resolves). The catalogue owns the version→image mapping;
  stored images are refreshed from it on load, so a new runtime image is
  applied on the next restart.

Why a per-project web container instead of one central proxy speaking FastCGI:
FastCGI details stay inside the project; the future central reverse proxy
(Phase 4) just forwards HTTP by `Host` header to `staqio-<slug>-web`. Projects
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
  ones from the plan) → start in dependency order (php before web) → set
  `desired_state=running`.
- Stop: stop containers (10 s grace) → `desired_state=stopped`.
- Restart: stop + start.

All operations hold the per-project lock; concurrent requests return `409`.

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

1. List all `staqio.managed=true` containers/networks/volumes.
2. Group by `staqio.project.id`.
3. For each DB project compute status (§8.6).
4. Resources whose project ID is unknown are reported as **orphans**
   (visible in the Docker view, never auto-deleted).
5. Projects with `desired_state=running` but stopped containers are flagged
   (`unexpectedly stopped`) – no automatic restart in Phase 2; the UI shows the
   discrepancy and offers "Start".

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
  staqio.db                SQLite (WAL)
  projects/<id>/           generated config per project (Caddyfile, php.ini, pool conf)
  backups/<slug>/          Phase 7
  ca/                      Phase 8 (0600)
/projects/<slug>/          user project files (bind-mounted into project containers)
Docker volumes             database / cache data (named, labelled)
```

Migrations are forward-only SQL files embedded in the binary and applied in
a transaction each; the version table prevents re-application. Downgrading
the image below the schema version is refused with a clear error.

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

---

## 12. Test strategy

| Layer | What | How |
|-------|------|-----|
| validate | names, paths, versions, traversal attempts | table-driven unit tests |
| auth | hashing, sessions, expiry, middleware, CSRF | unit tests with in-memory SQLite |
| store | migrations, CRUD, transactions | in-memory SQLite |
| project | planner output, create/start/stop/restart/delete, rollback on failure, reconciliation after "restart", container unexpectedly stopped, unmanaged resources untouched | unit tests against the fake Engine |
| api | unauthorized access, validation errors, error envelope, full lifecycle over HTTP | httptest + fake Engine |
| docker | real engine behaviour (labels, guards, foreign containers untouched) | integration tests behind `//go:build integration` (need Docker) |
| web | components, login flow, wizard flow, project list actions | Vitest + Testing Library |

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

### Phase 3 – Database (implemented for MariaDB)
`database` service kind with a labelled named volume
(`staqio-<slug>-database`), healthcheck, start order database → php → web.

Credentials: generated with `crypto/rand` from a shell/URL-safe alphabet,
stored in `project_services.config` (SQLite under `/config`, mode 0600).
They are never part of the normal project payload, logs or audit details; the
explicit `GET /projects/{id}/database/credentials` call is audit-logged.
`DB_*` and `DATABASE_URL` are injected into application containers, user
variables override them, `MARIADB_*`/`MYSQL_*` are reserved.

Management operations run `mariadb` inside the database container via Docker
exec with argv arrays; the root password travels in `MYSQL_PWD`, identifiers
are validated (`^[a-z][a-z0-9_]*$`), the primary database cannot be dropped.
Rotating the password recreates the application containers. Removing the
service requires `removeData: true`; version downgrades are refused, upgrades
run on the same volume with `MARIADB_AUTO_UPGRADE`.

### Later phases (prepared, not implemented)
- **Phase 4 Webserver/Domains**: central reverse proxy routing by `Host` to
  `staqio-<slug>-web`. Recommendation: embed the proxy in the Go binary
  (`httputil.ReverseProxy`, joins project networks) instead of a separate
  Caddy container – one fewer moving part, no config reloads. Decision
  deferred to Phase 4.
- **Phase 5 DX** (logs and terminal implemented): log streaming and PTY
  terminal over WebSocket – session cookie validated before the upgrade,
  same-origin enforced, containers resolved from `project + service kind`
  server-side; terminal shells in php/node run as PUID:PGID with `HOME=/tmp`
  and tool caches under `/tmp`. Still open: project actions (composer/npm via
  exec with argv arrays), Git, Node container.
- **Phase 6 Services**: Redis, PostgreSQL, MySQL, Node.
- **Phase 7 Backups**, **Phase 8 HTTPS/DNS**, **Phase 9 MCP** (reuses the
  same manager and validation layer).
