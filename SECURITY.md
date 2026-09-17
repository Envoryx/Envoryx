# Security

## The Docker socket

Staqio is mounted with `/var/run/docker.sock`. **Access to the Docker socket is
equivalent to root on the host**: anyone who can talk to it can start privileged
containers, mount `/` and read or modify any file. Staqio therefore treats the
socket as its most sensitive capability:

- Only `internal/docker` talks to the engine. It exposes a narrow, closed API
  (`docker.Engine`) – there is no way for any other code path, let alone the
  browser, to pass arbitrary Docker parameters.
- Container specs are built by the planner from a fixed catalogue. The spec type
  cannot express privileged mode, added capabilities, host networking, device
  access or arbitrary bind mounts. The engine additionally applies
  `no-new-privileges`, drops `NET_RAW`, and disables restart loops for
  transient containers.
- Every mutating call (start, stop, remove, …) first inspects the target and
  verifies the `staqio.managed=true` label. Unlabelled resources yield
  `ErrNotManaged` (HTTP 403) and remain untouched. This is covered by unit tests
  (fake engine) and integration tests (`go test -tags integration`).
- Foreign containers appear read-only in the diagnostics view with name, image,
  state and ports only – no labels, env or mounts.
- The API never accepts container IDs for mutations. Operations address
  `projectID + service kind`; the backend resolves the container.

### Socket proxy

Staqio honours `DOCKER_HOST`. To reduce the blast radius run a socket proxy such
as `tecnativa/docker-socket-proxy` and point Staqio at it
(`DOCKER_HOST=tcp://docker-socket-proxy:2375`). Staqio needs these endpoints:

| Endpoint group | Used for |
|----------------|----------|
| `PING`, `INFO`, `VERSION` | health and dashboard |
| `CONTAINERS` (list, inspect, create, start, stop, restart, remove, stats) | project lifecycle |
| `IMAGES` (inspect, pull) | runtime images |
| `NETWORKS` (list, inspect, create, remove) | project networks |
| `VOLUMES` (list, inspect, create, remove) | database volumes (Phase 3) |
| `EXEC` (Phase 5) | terminal, project actions |
| `POST` | required for create/start/stop |

`SWARM`, `NODES`, `SECRETS`, `CONFIGS`, `PLUGINS`, `SYSTEM` (prune), `BUILD`,
`COMMIT` can stay disabled.

## Authentication and sessions

- Passwords are hashed with **argon2id** (64 MiB, t=3, p=2) and never logged.
  Minimum length 10 characters. Unknown usernames burn the same hashing time as
  wrong passwords to blunt user enumeration by timing.
- Sessions are opaque 256-bit random tokens. Only the SHA-256 hash is stored.
  Idle timeout 12 h (sliding) and absolute timeout 7 days by default.
- Cookie: `HttpOnly`, `SameSite=Lax`, `Secure` when `STAQIO_SECURE_COOKIES=true`.
  Enable this when Staqio is served through an HTTPS reverse proxy.
- Login attempts are rate-limited per IP **and** per username with exponential
  back-off after 5 failures.
- Changing the password revokes all other sessions.
- The first admin is created through a one-time setup page (or from
  `STAQIO_ADMIN_USER`/`STAQIO_ADMIN_PASSWORD`). Setup is refused once any user
  exists. No generated passwords are ever written to logs.

## CSRF / CORS

State-changing API requests must:

1. carry the custom header `X-Requested-With: Staqio` (cannot be set cross-site
   without a CORS preflight, which is denied),
2. have no `Origin` header, or one matching the request host (or the explicit
   dev-server origin in `STAQIO_DEV` mode),
3. not carry a `Sec-Fetch-Site` of `cross-site`/`same-site`.

Together with `SameSite=Lax` cookies this blocks CSRF from other origins. CORS
headers are only emitted for the configured dev origin.

WebSockets (Phase 5) will validate the same session cookie at upgrade and apply
the same origin check.

## Input validation

- Project names: 2–64 printable characters; identifiers (slugs) derived and
  validated against `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`.
- Project paths are **relative** to `/projects`, at most 3 segments, no `.`/`..`,
  no hidden segments, restricted character set. Resolved paths are checked
  lexically and after symlink resolution to stay under the projects root.
  Deleting project files additionally refuses the root itself.
- Document roots follow the same rules relative to the project directory.
- Versions must exist in the runtime catalogue (no free-form image references).
- PHP settings: size values match `^[0-9]{1,6}[KMG]?$` (or `-1`),
  `error_reporting` a constrained expression, extensions from the known list.
- Environment variable names match `^[A-Z_][A-Z0-9_]*$`, values may not contain
  line breaks or NUL; `STAQIO_*` is reserved.
- All JSON bodies are limited to 1 MiB and reject unknown fields.
- UUIDs are validated before touching the database.
- No shell commands are built from strings anywhere. Container commands are
  argv arrays. Project actions (Phase 5) run inside project containers only.

## Destructive operations

- Deleting a project requires the project identifier to be typed as confirmation.
  Project files are only removed with an explicit second flag.
- Rollback after a failed create removes only resources recorded in the operation
  journal (all label-guarded) – never project files.
- Reconciliation never deletes anything; orphaned resources are reported.
- Interrupted create/delete operations (Staqio restart) are marked `failed` and
  surfaced in the UI instead of being auto-repaired.

## Secrets and logging

- Logs are structured (`log/slog`) and never contain passwords, tokens or
  environment variable values.
- The audit log records who did what (login, project lifecycle, settings) with
  IP and timestamp, but details exclude secrets.
- Project environment variables marked as secret are masked in the UI. They are
  stored in SQLite under `/config` (file mode 0600); protect that directory.
- Database credentials (Phase 3) and CA keys (Phase 8) will live under
  `/config` with restrictive permissions.

## HTTP hardening

- `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy: same-origin`, restrictive `Permissions-Policy`.
- Content-Security-Policy for the SPA (`default-src 'self'`, no inline scripts).
- Request IDs on every response; request/read/idle timeouts; 64 KiB header limit.
- Panics are recovered and reported as generic 500s.

## Running as root

The Staqio container runs as root because it needs the Docker socket and it
`chown`s newly created project directories to `PUID:PGID` so your editor user
owns the files. Project containers run their workers as `PUID:PGID` (PHP-FPM
pool `user`/`group`). If your socket is group-accessible you can run Staqio as
that group instead; directory ownership adjustments are then skipped.

## Reporting

Please report vulnerabilities privately to the maintainers before disclosure.
