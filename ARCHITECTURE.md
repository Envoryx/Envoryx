# Envoryx – Architecture

Envoryx is a Docker-native development environment manager for Unraid and Linux
Docker hosts. It runs as a single container, talks to the Docker Engine API and
creates isolated, per-project stacks (PHP, web server, database, cache, Node …).

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
├── cmd/envoryx/               main package (serve, healthcheck)
├── internal/
│   ├── api/                  HTTP handlers (v1), request/response DTOs, errors
│   ├── auth/                 password hashing, sessions, auth middleware
│   ├── audit/                audit log writer
│   ├── config/               environment configuration
│   ├── db/                   SQLite open + embedded migrations
│   ├── docker/               Docker Engine abstraction (interface + moby impl + fake)
│   ├── hostpath/             host-path detection for bind mounts (see §7)
│   ├── instance/             backups of the instance itself (db + config), restore on start
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
  - `Manager` executes lifecycle operations (create, start, stop, restart,
    delete) with per-project locking, a resource journal and rollback.
  - `Reconciler` compares database state with Docker on startup and
    periodically, updating derived status and flagging inconsistencies.
- **hostpath** – resolves the *host* path behind `/projects` and `/config`
  (see §7). Bind mounts for project containers must use host paths.
- **instance** – backups of Envoryx itself (see §10): create/list/import/
  delete, automatic pre-migrate/pre-restore snapshots, restore at next start.
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
container  envoryx-<slug>-<service>      e.g. envoryx-shimly-api-php
volume     envoryx-<slug>-<service>      e.g. envoryx-shimly-api-mariadb
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
  renders the config per variant), serves static files from
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
  ones from the plan) → start in dependency order (php before web) → set
  `desired_state=running`.
- Stop: stop containers (10 s grace) → `desired_state=stopped`.
- Restart: stop + start.

All operations hold the per-project lock; concurrent requests return `409`.

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
  envoryx.db                SQLite (WAL)
  projects/<id>/           generated config per project (web server config, php.ini, pool conf)
  backups/<slug>/          Phase 7
  ca/                      Phase 8 (0600)
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
application containers while stateful services keep running.

### SSH (`internal/sshd`)
`golang.org/x/crypto/ssh` server with an Ed25519 host key. Auth resolves the
user name through `Manager.ResolveSSHUser` (`<slug>` → PHP, `<slug>.node` →
Node) and validates either an API token (password) or an authorized key
from the settings. Session channels map `pty-req/shell/exec` to
`Engine.OpenTerminal` (PTY) or `Engine.ExecStream` (pipes, now with
`WorkingDir`) in the target container as PUID:PGID, `subsystem sftp` to a
`pkg/sftp` request server over `projectFS`, which serves `/var/www/html`
and `/home/envoryx` from the Envoryx-side directories of the same bind mounts
and chowns created files. `/home/envoryx` is a new persistent per-project
home (`/config/projects/<id>/home`) mounted into php/node/worker containers;
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
names, relative script paths, composer script names). The planner emits one
container per enabled worker from the PHP image (`Kind` and service label
`worker:<id>`, name `envoryx-<slug>-worker-<name>`, order 30, project env +
php.ini mount, PUID:PGID, `unless-stopped`), so `ensurePlan`, start/stop,
env recreation and delete treat them like any other container. Status lists
them as kind `worker` with `workerId`; logs/terminal accept `worker:<id>`.

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
`project.Templates()` is a closed list (Laravel, Symfony, WordPress). A
template is a sequence of argv steps run in transient containers from the
project's PHP image as PUID:PGID with the project directory mounted
(`RunOneShot`, label `envoryx.service=template`, default bridge network for
composer downloads) plus files Envoryx writes afterwards (WordPress
`wp-config.php` reading the injected `DB_*` variables, random salts). The
directory must be empty (like a clone); templates set the document root and
add required PHP extensions (`mysqli` for WordPress) and may require a
database. A failing step rolls the whole creation back.

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
certificate through Let's Encrypt (dns-01 via a `DNSProvider` interface,
Cloudflare implemented; `golang.org/x/crypto/acme`, no extra dependency),
stores it as the tlsca custom certificate and renews it 30 days before
expiry in a background loop; config/token under `/config/ca/acme.json`
(0600).

- **Phase 5 DX** (logs and terminal implemented): log streaming and PTY
  terminal over WebSocket – session cookie validated before the upgrade,
  same-origin enforced, containers resolved from `project + service kind`
  server-side; terminal shells in php/node run as PUID:PGID with `HOME=/tmp`
  and tool caches under `/tmp`. Project actions are a closed
  catalogue of argv commands (`actions.go`) gated by required files in the
  project directory; output streams over the same WebSocket mechanism and
  Ctrl+C is delivered on cancel/disconnect. Git runs in a transient
  container from the project's PHP image (`RunOneShot`) with the deploy key
  mounted only there; tokens travel via `GIT_CONFIG_*` env. The Node service is an idle
  tooling container (`sleep infinity`, runs as PUID:PGID) from
  `ghcr.io/envoryx/envoryx-node:<v>`. Dev-server mode (`runtime.NodeConfig`,
  stored in the service config): the package.json script becomes the
  container's main process (argv from a closed preset list – Vite/Next flags
  or HOST/PORT env only – script names validated), a host port is allocated
  like for other services and the proxy routes `<slug>-dev.<base>` to
  `envoryx-<slug>-node:<port>` (WebSocket/HMR passes through; Vite's host
  allow-list is set via `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS`). Config
  changes remove the node container so `ensurePlan` recreates it.
- **Phase 7 Backups** (implemented): `<backups dir>/<slug>/<timestamp-id>/`
  (`/backups` when mounted, else `/config/backups`; `ENVORYX_BACKUPS_DIR`)
  with `backup.json` (metadata + full project export incl. credentials),
  `database.sql.gz` (dump streamed from the database container via exec,
  password in env) and `files.tar.gz` (written by Envoryx, `vendor/` and
  `node_modules/` skipped unless requested). Restore requires the slug as
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
  `list_projects`, `get_project`, `list_runtimes`, `create_project`,
  `start/stop/restart_project`, `get_logs`, `list_actions`, `run_action`
  (runs a catalogue action to completion, returns stripped output + exit
  code, 20 min limit), `list/create_database`, `list/create_backup`,
  `add_domain`. Deleting projects, dropping databases and restoring backups
  are deliberately not exposed. Manager errors become tool errors
  (`isError`) so the assistant can react instead of the session failing.
