# Security

This page explains what Envoryx can do to your host, how it keeps that in check, and what
you should protect yourself.

## The Docker socket

Envoryx is mounted with `/var/run/docker.sock`. **Access to the Docker socket is
equivalent to root on the host**: anyone who can talk to it can start privileged
containers, mount `/` and read or modify any file. That's why Envoryx treats the socket as
its most sensitive capability:

- Only `internal/docker` talks to the engine. It exposes a narrow, closed API
  (`docker.Engine`); no other code path, let alone the browser, can pass arbitrary Docker
  parameters.
- The planner builds container specs from a fixed catalogue. The spec type can't express
  privileged mode, added capabilities, host networking, device access or arbitrary bind
  mounts. On top of that the engine applies `no-new-privileges`, drops `NET_RAW` and
  disables restart loops for transient containers.
- Every mutating call (start, stop, remove, …) first inspects the target and checks the
  `envoryx.managed=true` label. Unlabelled resources yield `ErrNotManaged` (HTTP 403) and
  stay untouched. Unit tests (fake engine) and integration tests
  (`go test -tags integration`) cover this.
- Foreign containers show up read-only in the diagnostics view, with name, image, state and
  ports only: no labels, env or mounts.
- The API never accepts container IDs for mutations. Operations address
  `projectID + service kind`, and the backend resolves the container.

### Socket proxy

Envoryx honours `DOCKER_HOST`. To shrink the blast radius, run a socket proxy such as
`tecnativa/docker-socket-proxy` and point Envoryx at it
(`DOCKER_HOST=tcp://docker-socket-proxy:2375`). The groups are the proxy's environment
variables; Envoryx needs these:

| Endpoint group | Used for |
|----------------|----------|
| `PING`, `INFO`, `VERSION` | health and dashboard |
| `CONTAINERS` (list, inspect, create, start, stop, restart, remove, stats, logs, attach, wait, update, exec create) | project lifecycle, logs, resource limits |
| `EXEC` | terminal, project actions, SSH sessions |
| `IMAGES` (list, inspect, pull, tag, remove) | runtime images, rollback tags, image clean-up |
| `NETWORKS` (list, inspect, create, connect, disconnect, remove) | project networks |
| `VOLUMES` (list, inspect, create, remove) | database and service volumes |
| `EVENTS` | OOM kills (on by default in the proxy) |
| `SYSTEM` | disk usage of the volumes (`/system/df`) for the resource history |
| `POST` | every request that isn't a GET: create, start, stop, remove, … |
| `BUILD` | custom runtime images built from a project Dockerfile |

Without `SYSTEM` everything else keeps working; the resource history just shows no volume
sizes. Without `BUILD` only custom images from a project Dockerfile fail; registry images
and the Envoryx images keep working. `SWARM`, `NODES`, `SERVICES`, `TASKS`, `SECRETS`,
`CONFIGS`, `PLUGINS`, `COMMIT`, `DISTRIBUTION`, `AUTH`, `SESSION` and `GRPC` can stay
disabled. `SYSTEM` only opens `/system/…`, where Envoryx reads the disk usage; the prune
endpoints belong to the other groups, and Envoryx doesn't call them.

Know what a socket proxy can't do: it filters by endpoint, not by the request body. With
`CONTAINERS` and `POST` open, whoever controls Envoryx can still create a privileged
container with `/` mounted, so the proxy doesn't turn a compromised Envoryx into anything
less than root on the host. It does take away whole areas (Swarm, secrets, plugins, image
commits) and stops a bug from reaching them. The real protection is keeping Envoryx itself
out of reach: HTTPS, no exposure to the open internet, and accounts only for people you
trust with the projects' containers.

## Authentication and sessions

- Passwords are hashed with **argon2id** (64 MiB, t=3, p=2) and never logged. The minimum
  length is 10 characters. Unknown usernames burn the same hashing time as wrong passwords,
  so timing doesn't tell an attacker which accounts exist.
- Sessions are opaque 256-bit random tokens, and only their SHA-256 hash is stored. By
  default they end after 12 h idle (sliding) and after 7 days at the latest.
- The cookie is `HttpOnly`, `SameSite=Lax`, and `Secure` when
  `ENVORYX_SECURE_COOKIES=true`. Switch that on when Envoryx is served through an HTTPS
  reverse proxy.
- Login attempts are rate-limited per IP **and** per username, with exponential back-off
  after 5 failures.
- Changing the password revokes all other sessions.
- The first admin is created through a one-time setup page (or from
  `ENVORYX_ADMIN_USER`/`ENVORYX_ADMIN_PASSWORD`). Setup is refused once any user exists, and
  generated passwords never end up in logs.
- Further users are invited. The invitation link carries a 256-bit random token, only its
  hash is stored, it works once within 48 hours, and using it (also as a password reset)
  ends that user's other sessions. An invited user without a password can't sign in with
  one, and a disabled user can neither sign in nor use their sessions, API tokens or SSH
  keys.
- Every request is checked against the user's role (viewer, developer, admin, or none) and,
  for a project, against their role in that project; a project without access is left out
  of every list. Only an admin's browser session manages users or shows and replaces the
  secret key, and the last active admin can't be demoted, disabled or deleted.
- Single sign-on (OpenID Connect) uses the authorization code flow with PKCE, a state bound
  to the browser by a short-lived `HttpOnly` cookie, and a nonce; the ID token is verified
  against the provider's published keys, issuer and client ID. Accounts are linked by the
  provider's subject, never by name: an existing Envoryx account is only linked while an
  admin's invitation for it is open. The client secret is never sent back to the browser.

## API tokens and MCP

- The MCP endpoint (`/mcp`) authenticates **only** with bearer tokens created in Settings.
  It ignores session cookies, so a web page can never call tools with ambient credentials,
  and tokens can't mint tokens.
- Tokens are 256-bit random values with the prefix `stq_`; only their SHA-256 hash is
  stored. You see the plain value once, and revoking takes effect immediately.
- Tools reuse the project manager, so all validation (slugs, paths, hostnames, database
  names, versions), the `envoryx.managed` label guards and the per-project locks apply.
  `run_action` only runs entries of the closed action catalogue (argv arrays, no shell).
  Delete, drop and restore aren't available via MCP, on purpose.
- Every tool call that changes state produces an audit entry attributed to the user, with
  the token name.
- Treat a token like a password: it grants up to the rights of your account (minus the
  destructive operations), never more. The owner's role is checked at every use, so a
  token loses what its owner loses. Prefer HTTPS (`https://envoryx.<base>`) for the MCP URL when
  clients connect over the network.

## SSH server

The embedded SSH server (port 2222) never gives access to the Envoryx container or the
host. Every session is a `docker exec` into the selected project's application container
(PHP, Python, Go, Ruby, Java, .NET or Node) as `PUID:PGID`, with the same environment the Terminal section
uses. You authenticate with an API token (as the password) or a public key: a user's own
keys act with that user's roles, the admin keys from the settings open every project. Ten
failures lock an IP for five minutes. The exec command line goes to
`/bin/sh -lc` inside that container, which is the same capability the browser terminal
already grants.

SFTP is a virtual view of exactly two directories (project and persistent home), served
from Envoryx's side of the bind mounts through `os.Root`: symbolic links are followed only
while they stay inside that directory, whoever made them (the container, a git checkout or
the SFTP client), so a link to `/` doesn't reach Envoryx's own files. The Ed25519 host key lives in `/config/ssh/host_ed25519` (0600). Only the `env`
requests `LANG`, `LC_*`, `TERM`, `XDEBUG_*`, `PHP_IDE_CONFIG`, `APP_ENV` and `CI` are
forwarded, and sessions and commands are audit-logged.

## CSRF / CORS

State-changing API requests must:

1. carry the custom header `X-Requested-With: Envoryx` (which can't be set cross-site
   without a CORS preflight, and that is denied),
2. have no `Origin` header, or one matching the request host (or the explicit dev-server
   origin in `ENVORYX_DEV` mode),
3. not carry a `Sec-Fetch-Site` of `cross-site`/`same-site`.

Together with `SameSite=Lax` cookies this blocks CSRF from other origins. CORS headers are
only sent for the configured dev origin.

WebSockets (log streaming, terminal, actions, test runs) go through the same session
middleware: the cookie is checked before the upgrade, and a request that isn't a WebSocket
upgrade from the request host (plus the dev-server origin in `ENVORYX_DEV` mode) is refused
before anything starts, so navigating a browser to such a route doesn't run an action. Container IDs never
come from the client; WebSocket routes address `project + service kind` and resolve the
container on the server.

## Input validation

- Project names: 2-64 printable characters. Identifiers (slugs) are derived from them and
  checked against `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`.
- Project paths are **relative** to `/projects`, at most 3 segments, no `.`/`..`, no
  hidden segments and a restricted character set. Resolved paths must stay under the
  projects root, checked lexically and after symlink resolution. Deleting project files
  also refuses the root itself.
- Document roots follow the same rules relative to the project directory.
- Versions must exist in the runtime catalogue; there are no free-form image references.
- PHP settings: size values match `^[0-9]{1,6}[KMG]?$` (or `-1`), `error_reporting` is a
  constrained expression, and extensions come from the known list.
- Environment variable names match `^[A-Z_][A-Z0-9_]*$`, values may not contain line breaks
  or NUL, and `ENVORYX_*` is reserved.
- All JSON bodies are limited to 1 MiB and reject unknown fields.
- UUIDs are validated before anything touches the database.
- No shell command is built from strings anywhere; container commands are argv arrays.
  Project actions come from a closed catalogue (`internal/project/actions.go`): the browser
  sends only the action id, the argv is fixed on the server, it runs inside the project
  container as the project owner, and the project lock is held while it runs.

## Destructive operations

- Deleting a project requires typing the project identifier as confirmation, and project
  files are only removed with an explicit second flag.
- Rolling back a failed create removes only resources recorded in the operation journal
  (all label-guarded). Of the project directory it removes only what the create put
  there: a directory the create made goes, one it found empty is emptied again, and one
  that already held files is never touched.
- Reconciliation doesn't delete projects or their data: it only clears orphaned containers
  and networks and reports the rest.
- Create and delete operations interrupted by an Envoryx restart are marked `failed` and
  shown in the UI instead of being repaired automatically.

## Secrets and logging

- Logs are structured (`log/slog`) and never contain passwords, tokens or environment
  variable values.
- The audit log records who did what (login, project lifecycle, settings) with IP and
  timestamp, but its details leave secrets out.
- Secrets at rest are encrypted with AES-256-GCM: Git tokens, project variables marked as
  secret, service credentials, addon secrets, the single sign-on client secret and registry
  logins in SQLite; `notify.json`, `offsite.json` and `ca/acme.json` as a whole; and the
  project export in each `backup.json`. The key comes from `ENVORYX_SECRET_KEY` or, without
  it, from `/config/secret.key` (0600); the variable keeps it out of `/config` and its
  backups. Instance backups never contain the key. A start whose key doesn't fit the
  database is refused rather than run with unreadable secrets. Private key files read by
  other programs (CA, SSH host key, ACME account key, deploy key) and the database
  browser's `dbtool/connections.json` are not encrypted; protect `/config` (file mode
  0600) all the same. Secret variables are also masked in the UI.
- Git access tokens are stored in SQLite (write-only via the API; `hasToken` is all that
  comes back) and handed to git through `GIT_CONFIG_*` environment variables inside a
  transient container: never in the URL, on a command line or in logs, and command output
  is redacted before it's shown. Repository URLs are restricted to https/http/ssh/scp-like
  forms (no `file://`, `ext::`, local paths, embedded passwords or option-like values), and
  branch names to `[A-Za-z0-9._/-]`, passed after `--`.
- The SSH deploy key (`/config/ssh/id_ed25519`, mode 0600, owned by PUID:PGID) is mounted
  only into the short-lived git container, never into the long-running application
  containers, so application code can't read it.
- Database credentials are generated (24 chars, `crypto/rand`) and stored in the SQLite
  database. Project responses leave them out; the explicit credentials endpoint is
  audit-logged. Inside the database container they're passed via environment
  (`MYSQL_PWD`), never on a command line, and stripped from error messages before those
  reach logs or the UI.

- Private registry logins (*Settings → Private registries*) are stored like the other secrets: encrypted
  in the SQLite database under `/config`, write-only through the API (`hasPassword` is all
  that comes back). They go to the Docker daemon with each pull and build, never
  into a container, a command line or a log.
- A custom runtime image runs with the same mounts and network as the Envoryx image it
  replaces, so it reaches nothing the runtime couldn't; only admins can set one. The
  build context of a project Dockerfile is read through an `os.Root` of the Dockerfile's
  directory and symbolic links are left out, so a repository can't send files from outside
  that directory (or the host) to the build.
- Addons are a sandbox by construction: the file format has no field for privileged mode,
  capabilities, host directories, the host network or devices, and it's read strictly
  (`KnownFields`), so an unknown key is an error rather than ignored. An addon container
  gets named volumes on its project's network, like the built-in services, and its
  image runs whatever that image runs; only admins install or change addon files (under
  `/config/addons`), developers can only add installed ones to their projects. *Install
  from a URL* fetches any http(s) URL from the Envoryx server as the admin asks (20 s
  timeout, 64 KB limit, HTTP 200 only) and installs it only if it validates. The secrets
  an addon generates per project are stored like the other secrets (encrypted, in the
  project's service configuration in SQLite); the project's service list leaves them
  out, and secret credentials reach only users who may operate the project.

## Database browser

The optional database browser is one Adminer container for every project, and the
credentials file it reads holds the logins of all of them. It therefore never decides on
its own what a request may open:

- It is reached only through Envoryx at `/dbtool/`, behind the Envoryx session or a
  bearer token. The proxy reads the database each request names (Adminer's driver and
  server parameters and `username`, and the fields of a login form) and forwards it only
  when that login belongs to a project the user or token may operate. The project's
  administrator login (`root`, `postgres`) follows the same rule, because whoever may
  operate a project can read its credentials anyway. Anything else, including hosts that
  belong to no project and query shapes PHP would read differently (repeated or array
  parameters, two drivers), is refused with `403`. The check runs on every request, not
  only at login, since Adminer keeps its logins in a session of its own.
- The proxy tells the container which login it checked, and the Envoryx plugin inside
  Adminer connects only to exactly that login.
- The container joins the network of every project whose database is opened in it, so
  the project containers there could reach it. It answers only requests that carry a
  random secret the proxy adds (`/config/dbtool/proxy-token`), serves nothing but
  Adminer's entry page, and a container from an older Envoryx without this guard is
  removed by the next reconcile.

## Backups

Backups contain the full project export, including database credentials and git tokens
(they're needed to rebuild a project), encrypted with the secret key in `backup.json`, and
live in the backups directory (`/config/backups` or the `/backups` mount) with mode
0600/0700. The database dump in a backup is not encrypted: treat downloaded archives with
the same care. Instance backups hold the encrypted database and never the key.
Restores are confirmed with the project identifier, only ever write inside the project
directory (path traversal and symlink escapes are rejected) and only import a dump whose
flavour matches the project's database.

## Reverse proxy and local CA

- The embedded proxy only routes host names that belong to a project or to Envoryx itself.
  Unknown names get a static 404 page, stopped projects a 503. It never proxies to
  arbitrary upstreams: the targets are container names derived from the project slug (or
  `127.0.0.1:<port>` on bare metal).
- Envoryx's own container is attached to every project network so the proxy can reach the
  web containers. As a result, project containers can reach Envoryx's listeners (UI port,
  proxy) by IP on that network. That's the same exposure as any LAN client: the API
  requires an authenticated session and passes the CSRF checks, and the proxy only routes
  known names. Code you run in a project is trusted as much as code on your workstation.
- The local CA key (`/config/ca/ca.key`, mode 0600) can sign certificates for **any** name,
  so anyone with that file can impersonate websites on clients that trust the CA. Keep
  `/config` private and install the CA only on machines you control. The CA is meant for a
  development network and isn't constrained by name. Delete `/config/ca/` to get a new CA
  (and install it on your clients again afterwards).
- Leaf certificates are valid for 397 days and re-issued automatically.
- Uploaded custom certificates are validated (PEM, matching key) and stored with mode 0600;
  the API never returns the key.
- Let's Encrypt: the DNS provider's credentials are stored encrypted in
  `/config/ca/acme.json` (0600), and the API never returns the secret ones. Scope them to the one zone where the provider
  allows it (on Cloudflare, the "Edit zone DNS" template). The ACME account key lives next
  to them. Only dns-01 is used, so no inbound connectivity is required and none is opened,
  and the challenge TXT records are removed after each attempt.
- TLS certificates are only issued for names in the routing table, IPs and the configured
  public host; SNI for other names is rejected.

## HTTP hardening

- `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy: same-origin` and a restrictive `Permissions-Policy`.
- A Content-Security-Policy for the SPA (`default-src 'self'`, no inline scripts).
- Request IDs on every response, request/read/idle timeouts and a 64 KiB header limit.
- Panics are recovered and reported as a generic 500.

## Running as root

The Envoryx container runs as root because it needs the Docker socket and `chown`s newly
created project directories to `PUID:PGID`, so your editor user owns the files. Project
containers run their workers as `PUID:PGID` (PHP-FPM pool `user`/`group`). If your socket
is group-accessible you can run Envoryx as that group instead; it then skips the directory
ownership adjustments.

## Reporting

Please report vulnerabilities privately to the maintainers before disclosing them.
