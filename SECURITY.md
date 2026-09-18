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

## API tokens and MCP

- The MCP endpoint (`/mcp`) authenticates **only** with bearer tokens
  created in Settings. Session cookies are ignored there, so a web page can
  never call tools with ambient credentials, and tokens cannot mint tokens.
- Tokens are 256-bit random values with the prefix `stq_`; only their
  SHA-256 hash is stored. The plain value is shown once. Revoking takes
  effect immediately.
- Tools reuse the project manager, so all validation (slugs, paths,
  hostnames, database names, versions), the `staqio.managed` label guards and
  per-project locks apply. `run_action` executes only entries of the closed
  action catalogue (argv arrays, no shell). Delete/drop/restore are not
  available via MCP by design.
- Every tool call that changes state produces an audit entry attributed to
  the user with the token name.
- Treat a token like a password: it grants the same rights as your account
  (minus the destructive operations). Prefer HTTPS (`https://staqio.<base>`)
  for the MCP URL when clients connect over the network.

## CSRF / CORS

State-changing API requests must:

1. carry the custom header `X-Requested-With: Staqio` (cannot be set cross-site
   without a CORS preflight, which is denied),
2. have no `Origin` header, or one matching the request host (or the explicit
   dev-server origin in `STAQIO_DEV` mode),
3. not carry a `Sec-Fetch-Site` of `cross-site`/`same-site`.

Together with `SameSite=Lax` cookies this blocks CSRF from other origins. CORS
headers are only emitted for the configured dev origin.

WebSockets (log streaming, terminal) go through the same session middleware:
the cookie is validated before the upgrade, and the upgrade itself is refused
for any `Origin` other than the request host (plus the dev-server origin in
`STAQIO_DEV` mode). Container IDs are never taken from the client; WebSocket
routes address `project + service kind` and resolve the container server-side.

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
  argv arrays. Project actions come from a closed catalogue
  (`internal/project/actions.go`): the browser sends only the action id, the
  argv is fixed server-side, execution happens inside the project container as
  the project owner, and the project lock is held for the duration.

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
- Git access tokens are stored in SQLite (write-only via the API, `hasToken`
  is the only thing returned) and passed to git through `GIT_CONFIG_*`
  environment variables inside a transient container – never in the URL, on
  a command line or in logs; command output is redacted before it is shown.
  Repository URLs are restricted to https/http/ssh/scp-like forms (no
  `file://`, `ext::`, local paths, embedded passwords or option-like values),
  branch names to `[A-Za-z0-9._/-]` and passed after `--`.
- The SSH deploy key (`/config/ssh/id_ed25519`, mode 0600, owned by
  PUID:PGID) is mounted only into the short-lived git container, never into
  the long-running PHP container, so application code cannot read it.
- Database credentials are generated (24 chars, `crypto/rand`) and stored in
  the SQLite database. They are excluded from project responses; the explicit
  credentials endpoint is audit-logged. Inside the database container they are
  passed via environment (`MYSQL_PWD`), never on a command line, and stripped
  from error messages before they reach logs or the UI.
- CA keys (Phase 8) will live under `/config` with restrictive permissions.

## Backups

Backups contain the full project export including database credentials and
git tokens (needed to rebuild a project) and live under `/config/backups`
with mode 0600/0700. Treat downloaded archives accordingly. Restores are
confirmed with the project identifier, only ever write inside the project
directory (path traversal and symlink escapes are rejected) and only import a
dump whose flavour matches the project's database.

## Reverse proxy and local CA

- The embedded proxy only routes host names that belong to a project or to
  Staqio itself; unknown names get a static 404 page, stopped projects a 503.
  It never proxies to arbitrary upstreams – targets are container names
  derived from the project slug (or `127.0.0.1:<port>` on bare metal).
- Staqio's own container is attached to every project network so the proxy
  can reach the web containers. Consequently project containers can reach
  Staqio's listeners (UI port, proxy) by IP on that network – the same
  exposure as any LAN client: the API requires an authenticated session and
  the CSRF checks, the proxy only routes known names. Application code you
  run in a project is trusted to the same degree as code on your workstation.
- The local CA key (`/config/ca/ca.key`, mode 0600) can sign certificates
  for **any** name. Anyone with that file can impersonate websites on
  clients that trust the CA. Keep `/config` private, and only install the
  CA on machines you control. The CA is scoped for a development network;
  it is not constrained by name. Delete `/config/ca/` to generate a new CA
  (re-install it on clients afterwards).
- Leaf certificates are valid for 397 days and re-issued automatically.
- Uploaded custom certificates are validated (PEM, matching key) and stored
  with mode 0600; the key is never returned by the API.
- Let's Encrypt integration: the DNS provider API token is stored in
  `/config/ca/acme.json` (0600) and never returned by the API; scope it to
  the one zone (Cloudflare "Edit zone DNS" template). The ACME account key
  lives next to it. Only dns-01 is used – no inbound connectivity is required
  and none is opened. Challenge TXT records are removed after each attempt.
- TLS certificates are only issued for names in the routing table, IPs and
  the configured public host; SNI for other names is rejected.

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
