# Deployment

## Requirements

> **Renamed from Staqio.** Envoryx is not a drop-in upgrade for a Staqio
> installation: the image, config directory (`/mnt/user/appdata/envoryx`),
> `ENVORYX_*` variables, Docker labels, container/volume names and the
> `envoryx.test` base domain all changed. Install Envoryx fresh and recreate
> projects (project files on disk are untouched; use the Staqio backups to restore
> databases).


- Linux host with Docker Engine ≥ 24 (API ≥ 1.43); Unraid 6.12+ / 7.x or
  any other Linux distribution
- x86_64 or arm64 (Raspberry Pi 4/5, Ampere/Graviton, Apple Silicon under
  Linux); all Envoryx images are multi-arch
- A directory for Envoryx's state (`/config`) and one for your projects (`/projects`);
  optionally a third one for backups (`/backups`, see [Backups](#backups))

## Docker Compose (any Linux host)

Unraid is the primary target but nothing depends on it. On another Linux host
pick two directories and your own uid/gid:

```yaml
services:
  envoryx:
    image: ghcr.io/envoryx/envoryx:latest
    container_name: envoryx
    ports:
      - "8787:8787"
      - "80:80"     # proxy: projects by domain (optional, any free host port)
      - "443:443"   # proxy: HTTPS via local CA (optional)
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/user/appdata/envoryx:/config
      - /mnt/user/development:/projects
    environment:
      PUID: 99      # Unraid: nobody; elsewhere: your uid (`id -u`)
      PGID: 100     # Unraid: users;  elsewhere: your gid (`id -g`)
      ENVORYX_PORT_RANGE_START: 20000
      ENVORYX_PORT_RANGE_END: 20999
    restart: unless-stopped
```

`deploy/docker-compose.yml` contains a commented version of this file.
Replace `/mnt/user/...` with e.g. `/srv/envoryx` and `/home/you/dev` on a
regular server.

Start with `docker compose up -d`, open `http://<host>:8787` and create the
admin account. Project web servers are published on ports from the configured
range, e.g. `http://<host>:20000`, and – with the proxy ports mapped and DNS
set up (see [Domains and HTTPS](#domains-and-https)) – as
`https://<project>.test`.

### Building locally

```
docker build -t ghcr.io/envoryx/envoryx:dev --build-arg VERSION=dev .
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ENVORYX_LISTEN` | `:8787` | Listen address |
| `ENVORYX_CONFIG_DIR` | `/config` | Persistent state (SQLite, generated configs, caches). Must be on a local filesystem, see [Where to put /config](#where-to-put-config) |
| `ENVORYX_ALLOW_NETWORK_FS` | `false` | Start even if `/config` is on NFS/SMB. Not recommended: SQLite corrupts silently without reliable file locks |
| `ENVORYX_PROJECTS_DIR` | `/projects` | Root of all project directories |
| `ENVORYX_BACKUPS_DIR` | `/backups` if that directory exists, else `/config/backups` | Where project backups are stored (see [Backups](#backups)) |
| `ENVORYX_PROJECTS_HOST_PATH` | auto | Host path behind `/projects` (see below) |
| `ENVORYX_CONFIG_HOST_PATH` | auto | Host path behind `/config` |
| `DOCKER_HOST` | unix socket | Docker endpoint; set to a socket proxy URL if used |
| `PUID` / `PGID` | `99` / `100` | uid/gid project containers run as and project dirs are owned by |
| `ENVORYX_PORT_RANGE_START` / `_END` | `20000` / `20999` | Host ports assigned to project web servers |
| `ENVORYX_PUBLIC_HOST` | browser address | Host/IP used for project links (see below); also editable in Settings |
| `ENVORYX_PROXY_HTTP` | `:80` | Listen address of the embedded proxy inside the container; empty disables it |
| `ENVORYX_PROXY_HTTPS` | `:443` | HTTPS listener of the proxy (local CA); empty disables HTTPS |
| `ENVORYX_SSH` | `:2222` | Embedded SSH server for IDE remote interpreters; empty disables it |
| `ENVORYX_ADMIN_USER` / `ENVORYX_ADMIN_PASSWORD` | – | Create the first admin non-interactively |
| `ENVORYX_SESSION_IDLE_TIMEOUT` | `12h` | Sliding session expiry |
| `ENVORYX_SESSION_ABSOLUTE_TIMEOUT` | `168h` | Hard session expiry |
| `ENVORYX_SECURE_COOKIES` | `false` | Mark cookies `Secure` (enable behind HTTPS) |
| `ENVORYX_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `ENVORYX_LOG_FORMAT` | `json` | `json` or `text` |

## Where to put /config

`/config` holds the SQLite database. SQLite needs a filesystem with working
`fsync` and POSIX locks, so at startup Envoryx checks what `/config` is on:

- **Network filesystem (NFS, SMB/CIFS, 9p, Ceph)**: Envoryx refuses to start.
  Locks are unreliable there and the database corrupts silently – the classic
  "SQLite is unreliable" story is almost always this. Use a local directory;
  `ENVORYX_ALLOW_NETWORK_FS=true` overrides the check at your own risk.
- **FUSE** (Unraid's `/mnt/user/...` user shares): Envoryx starts and shows a
  warning in *Settings*. It works in practice, but the pool path is the safer
  choice for a database: use `/mnt/cache/appdata/envoryx` (or your pool's
  name) as the host path for `/config`, or enable *Exclusive access* for the
  `appdata` share (Unraid ≥ 6.12, share on a single pool) – then `/mnt/user`
  bypasses FUSE and the warning disappears.
- The database file is integrity-checked at every start (`PRAGMA
  integrity_check`). A damaged file is refused with the name of the newest
  instance backup to restore instead of being migrated or served.

## Host paths (important)

Project containers mount your project files with Docker **bind mounts**. Bind
mount sources are resolved by the Docker daemon **on the host**, not inside the
Envoryx container. Envoryx therefore needs to know that `/projects` inside its
container is `/mnt/user/development` on the host.

Envoryx detects this automatically by inspecting its own container's mounts. If
detection fails (the dashboard shows a red banner and project creation is
disabled), set the two variables explicitly:

```
ENVORYX_PROJECTS_HOST_PATH=/mnt/user/development
ENVORYX_CONFIG_HOST_PATH=/mnt/user/appdata/envoryx
```

On Unraid always use `/mnt/user/...` (or `/mnt/cache/...`) paths – the same
ones you used in the volume mappings.

## Project links and the Envoryx container's own IP

Project web servers publish their ports on the **Docker host** (the Unraid
IP). Envoryx builds project links from the address in your browser's address
bar. If you reach Envoryx under a different address – the container has its
own IP on `br0`/macvlan, or you use a reverse proxy – set the host to use for
project links in **Settings → Project links** (or `ENVORYX_PUBLIC_HOST`),
typically the Unraid IP.

## Unraid

### Option A: Template (recommended, one click)

Copy the template to your flash drive so it appears under
**Docker → Add Container → Select a template → User templates**:

```
wget -O /boot/config/plugins/dockerMan/templates-user/envoryx.xml \
  https://raw.githubusercontent.com/envoryx/envoryx/main/deploy/unraid/envoryx.xml
```

Then *Add Container → Envoryx → Apply*. Ports, paths, socket, PUID/PGID are
pre-filled; create the `development` share first if it does not exist.

Alternatively add `https://github.com/envoryx/envoryx` under
**Docker → Add Container → Template repositories** – Unraid then reads
`deploy/unraid/envoryx.xml` directly from the repository and picks up updates.

No publication in Community Applications is required for either way.

### Option B: Manual

**Docker → Add Container**:

| Setting | Value |
|---------|-------|
| Repository | `ghcr.io/envoryx/envoryx:latest` |
| Network type | bridge |
| Port | `8787` → `8787` |
| Port | `80` → `80` and `443` → `443` (proxy; optional, other host ports work) |
| Port | `2222` → `2222` (SSH for IDEs; optional) |
| Path `/config` | `/mnt/cache/appdata/envoryx` (pool path; `/mnt/user/appdata/envoryx` works with a warning, see [Where to put /config](#where-to-put-config)) |
| Path `/projects` | `/mnt/user/development` (create the share first) |
| Path `/backups` | optional, e.g. `/mnt/user/backups/envoryx` – keeps backups off the cache/appdata share |
| Path `/var/run/docker.sock` | `/var/run/docker.sock` |
| Variable `PUID` | `99` |
| Variable `PGID` | `100` |

Project ports (20000–20999 by default) are published by the project containers
themselves, not by the Envoryx container – nothing else to map in the template.
Unraid shows Envoryx's containers (`envoryx-<project>-…`) in the Docker tab; you
can leave them alone, Envoryx manages them.

### Image visibility

The image is built by GitHub Actions (`.github/workflows/docker.yml`) and
pushed to `ghcr.io/envoryx/envoryx`. GitHub creates new packages as **private**;
set the package to *public* once (GitHub → Packages → envoryx → Package settings
→ Change visibility) so Unraid can pull it without credentials. For a private
package run `docker login ghcr.io` on the Unraid server instead (add it to
`/boot/config/go` to survive reboots).

### File ownership

Files created by PHP inside a project are owned by `PUID:PGID` (Unraid default
`nobody:users` = `99:100`), so they are editable via SMB shares. Directories
created by Envoryx itself are chowned to the same ids.

## Domains and HTTPS

Envoryx contains a reverse proxy that routes requests by host name to the
project's web server and serves the Envoryx UI itself. It listens on ports 80
and 443 **inside** the container; map them to host ports (80/443 or any free
ones – Envoryx reads its own port bindings and adjusts links accordingly).
When neither port is published, Settings → *Domains & HTTPS* shows a warning
and project links keep using the direct port.

**Own IP (Unraid `br0`, macvlan/ipvlan) or host networking:** there is no
port mapping – the proxy is reachable directly on the container's address
(`http(s)://<envoryx-ip>`). Envoryx detects this and shows the address in the
settings. Your DNS entries for `*.test` must then point at the **Envoryx IP**,
not at the Unraid IP (which is where the direct project ports live).

### Names

- Base domain, default `test` (Settings → Domains & HTTPS). Every project is
  `<slug>.<base>` (`shop.test`), the UI is `envoryx.<base>`.
- Additional names per project in its **Domains** tab (`shop.local`,
  `api.shop.test`, …). Each name must be unique across projects.
- The names must resolve to the Envoryx host on every client. Because Envoryx
  runs on a server, use one of:
  - **Pi-hole / AdGuard Home**: DNS rewrite `*.test → <server-ip>`
    (AdGuard: *Filters → DNS rewrites*, domain `*.test`; Pi-hole ≥ 6:
    *Local DNS → DNS Records*, or `dnsmasq` line below in `/etc/dnsmasq.d/`)
  - **dnsmasq** (router, server): `address=/.test/<server-ip>`
  - **hosts file** per client (`/etc/hosts`, `C:\Windows\System32\drivers\etc\hosts`):
    `<server-ip> shop.test envoryx.test`
  - macOS: `/etc/resolver/test` with `nameserver <dns-ip>` if you run your
    own DNS only for that zone.
- `.test` is reserved for exactly this purpose (RFC 6761) and never resolves
  on the public internet. `.local` collides with mDNS on macOS/Linux – prefer
  `.test` or an owned domain (`dev.example.com`).

### HTTPS

On first start Envoryx creates a local certificate authority under
`/config/ca/` (`ca.key` 0600, `ca.crt`). The proxy issues a certificate per
host name on demand, so `https://shop.test` works as soon as the CA is
trusted on the client:

1. Settings → Domains & HTTPS → **Download envoryx-ca.crt**.
2. Install it as a trusted root (instructions per OS are shown next to the
   button; Firefox has its own store).

**Force HTTPS** redirects `http://` requests for known names to HTTPS.

### Let's Encrypt instead of the local CA (no installation on clients)

If you own a domain, Envoryx can obtain and renew a **public wildcard
certificate** itself, so every browser trusts project URLs without any
CA installation. It uses the ACME dns-01 challenge: Envoryx creates a
temporary `_acme-challenge` TXT record through your DNS provider's API,
Let's Encrypt verifies it, done. Nothing needs to be reachable from the
internet and the domain never has to point at your server publicly.

1. Cloudflare (currently the supported provider): *My Profile → API Tokens →
   Create Token → template "Edit zone DNS"*, restricted to the zone. The
   token needs *Zone:Read* and *Zone:DNS:Edit*.
2. Settings → Domains & HTTPS → **Let's Encrypt**: provider, domain
   (e.g. `dev.example.com` – the wildcard `*.dev.example.com` is added),
   contact e-mail, token, "Use as base domain" → Enable.
3. In your **local** DNS (AdGuard/Pi-hole/…) rewrite `*.dev.example.com` →
   Envoryx's address. Do not create a public record for it.

Envoryx requests the certificate in the background (1–2 minutes, status is
shown in the card), stores it under `/config/ca/custom.*` and renews it 30
days before expiry. The local CA stays as fallback for other names (`.test`).
The staging checkbox uses Let's Encrypt's staging environment to test the
setup without rate limits (certificates from staging are not trusted).

Note: Let's Encrypt publishes every issued certificate in public
certificate-transparency logs, so the existence of `*.dev.example.com` is
visible there – nothing else.

**Own certificate**: alternatively upload any wildcard certificate with its
key in the same card. It is used for every name it covers; other names keep
using the local CA. Certificate and key are stored under `/config/ca/custom.*`
with owner-only permissions and are never returned by the API.

The proxy keeps the original `Host`, sets `X-Forwarded-For/-Proto/-Host` and
supports WebSockets. Projects can therefore generate correct absolute URLs
(`APP_URL=https://shop.test`).

### Node dev servers

Enable "Run a dev server" on the Node.js service (wizard or Runtime tab):
the script (default `dev`) runs as the container's main process and is
reachable at `https://<project>-dev.<base>` through the proxy (HMR
WebSockets included) and on a direct host port. Presets pass host/port to
Vite (`--host 0.0.0.0 --port`) or Next.js (`-H -p`); "Other" only sets
`HOST`/`PORT`. Run `npm install` via Actions first – a crashing script is
restarted by Docker until it works. For Laravel + Vite set
`VITE_DEV_SERVER_URL`/`APP_URL` accordingly, or let Vite's `server.hmr`
config point at the dev host name.

### Bare metal

Outside Docker the proxy dials the project's published port
(`127.0.0.1:<port>`) instead of joining the project network. Binding 80/443
needs `setcap cap_net_bind_service=+ep ./envoryx` or other addresses
(`ENVORYX_PROXY_HTTP=:8080`).

## Updating

```
docker compose pull && docker compose up -d
```

Envoryx's state lives in `/config`, `/projects` and `/backups` only. Replacing the image
never touches projects. Database migrations run automatically and are forward
only; a database newer than the binary is refused with a clear error.

Before the first migration of a new version Envoryx writes an **instance
backup** (`pre-migrate-…`) to `<backups dir>/_instance/`. To go back to the
previous version: pull the old image, then restore that backup from *Settings →
Instance backups* (or, if the old version does not start because the schema is
newer, see [Instance backups](#instance-backups) for the manual way).

## Keeping PHP up to date

- **Patch releases** (e.g. 8.5.3 → 8.5.4): the `envoryx-php` images are rebuilt
  weekly from the official `php` images. A project **Restart** pulls the tag
  again and recreates the container only if the image actually changed. Until
  then the project keeps running on the previous build – nothing changes
  behind your back.
- **New minor versions** (e.g. 8.6): a weekly workflow compares
  `internal/runtime/php_versions.json` with endoflife.date and Docker Hub and
  opens a pull request when a version appears, becomes stable or reaches EOL.
  Merging it builds the images and the next Envoryx image shows the version in
  the wizard. Pre-release versions are marked *preview*.

Node.js images (`envoryx-node:*`) follow the same scheme with `node_versions.json`.

## Git deploy key

For SSH repositories Envoryx generates an Ed25519 key pair on first use under
`/config/ssh/`. Copy the public key from **Settings → Git deploy key** (or the
project's Git tab) into your repository as a read-only deploy key. Private
HTTPS repositories use an access token per project instead (GitHub:
fine-grained PAT with *Contents: read*; GitLab: username `oauth2` + token).

## Backups

Project backups (database dump, files, configuration) are created from the
project's **Backups** tab and stored as plain directories, one per backup,
under `<backups dir>/<project>/`.

By default the backups directory is `/config/backups`, i.e. on the appdata
share. Backups are large and rarely read, so you may want them on the array
instead of the cache SSD: mount a host directory at `/backups` (Unraid: add
the optional *Backups* path in the template, e.g. `/mnt/user/backups/envoryx`;
Compose: uncomment the `/backups` volume) and Envoryx uses it automatically.
`ENVORYX_BACKUPS_DIR` overrides the location explicitly. To move existing
backups, stop Envoryx, move the contents of `/config/backups/` into the new
directory and start again – a warning is logged at startup while backups are
left behind in the old location.

Include the backups directory in your regular off-machine backup (e.g. the
Unraid Appdata Backup plugin or an rsync job), or use the per-backup download.

**Scheduled backups**: Backups tab → *Scheduled backups*: daily or weekly at
a given hour (server local time – set `TZ` on the container for your zone),
keep the last N scheduled backups (manual ones are never deleted), optionally
including `vendor/`/`node_modules/`. Failures raise a notification.

### Instance backups

*Settings → Instance backups* snapshots Envoryx itself: the SQLite database
(accounts, sessions, API tokens, projects and their service settings, domains,
workers, settings), the local CA, the SSH host key and deploy keys,
notification settings and the generated per-project configuration. Project
files and Docker volumes are **not** included – that is what project backups
are for. A backup is a single `envoryx-<id>.tar.gz` under
`<backups dir>/_instance/` (`instance.json` with version/schema, `envoryx.db`
as a consistent `VACUUM INTO` copy, `config/…`).

- **Create** a backup any time (e.g. before an update); **download** it and
  **import** it on another host to move an installation.
- **Automatic**: before every schema migration (`pre-migrate-…`) and before
  every restore (`pre-restore-…`). The last 5 of each kind are kept; manual and
  imported ones stay until deleted.
- **Restore** needs the confirmation word `restore`. Envoryx records the
  request, restarts in place (the process replaces itself, so no restart policy
  is needed) and applies the backup before opening the database: current
  state → `pre-restore` backup, then database and config are replaced.
  Containers and project files are untouched; projects that were created after
  the backup appear as orphans in *Docker* and can be removed there. All
  sessions end; sign in again with the credentials from the backup.
- A backup from a **newer** Envoryx (higher schema version) is refused; an
  older one is migrated forward on start (with its own `pre-migrate` backup).

Manual restore without the UI (e.g. Envoryx does not start): stop the
container, unpack the archive – `envoryx.db` to `/config/envoryx.db` (delete
`envoryx.db-wal`/`-shm` if present), `config/*` over `/config/` – and start
again.

Still include `/config`, `/projects` and the backups directory in your regular
off-machine backup (e.g. the Unraid Appdata Backup plugin or an rsync job).

## Workers (queues, schedulers)

Workers tab: add long-running processes from a preset list – Laravel
`schedule:work`, `queue:work`/`queue:listen` (queue names), Horizon,
Reverb, Symfony `messenger:consume` (transports) and Scheduler, a PHP script
or a composer script. Every worker is its own container
(`envoryx-<project>-worker-<name>`) from the project's PHP image, runs as
`PUID:PGID` with the same environment and php.ini as the web PHP, restarts
automatically (Docker `unless-stopped`) and follows start/stop/restart of
the project. `queue:work` stops after an hour (`--max-time`) so code changes
are picked up on the automatic restart; use `queue:listen` for instant
reloads. Logs are in the Logs tab; up to 10 workers per project.

## IDE integration (PhpStorm, VS Code)

Every project has an **IDE** tab with all values ready to copy.

**Remote interpreter over SSH.** Envoryx runs an SSH server on port 2222
(publish it, or use the container's own IP on `br0`). User name = project
slug (`shop`, or `shop.node` for the Node container), password = an API
token from Settings → API tokens, or a public key stored under Settings →
SSH access. Each session is a `docker exec` into the project's container as
the project owner – there is no shell on the host. SFTP exposes
`/var/www/html` (the project) and `/home/envoryx` (a persistent home for
tool caches and IDE helpers). PhpStorm: *Settings → PHP → CLI Interpreter →
… → From Docker, Vagrant, VM, WSL, Remote… → SSH*; PHP path
`/usr/local/bin/php`, helpers path `/home/envoryx/.phpstorm_helpers`, path
mapping *project folder* → `/var/www/html`. Afterwards PHPUnit/Pest,
Composer and Artisan run inside the container from the IDE. VS Code:
Remote-SSH works the same way (`ssh -p 2222 shop@<host>`).

The project must be running for sessions to open. Commands are logged to
the audit log (`ssh.exec`), failed logins are rate limited per IP.

### JetBrains Gateway (optional)

Gateway runs the complete IDE backend on the server and connects a thin
client. In Envoryx this is opt-in per project (IDE tab → *Allow JetBrains
Gateway*): it enables SSH port forwarding into the container and mounts a
shared backend cache (`/config/jetbrains`, ~1.5 GB per IDE version,
downloaded once). The backend runs as the project owner inside the PHP
(or Node, user `<slug>.node`) container and needs 2–4 GB RAM plus CPU while
indexing – nothing runs until you connect. Envoryx ships no JetBrains
software: Gateway itself is free, the IDE backend is uploaded by your
Gateway client and licensed through it – whoever connects needs a valid
subscription for that IDE (PhpStorm, WebStorm or All Products Pack), the
server needs nothing. Gateway → *SSH → New connection*
with the values from the IDE tab, choose PhpStorm/WebStorm, project
directory `/var/www/html`. Close the project in Gateway or use *Stop IDE
backend* to free the memory. Small NAS boxes: leave it off.

The tunnel to the backend needs `socat` in the runtime image (PHP and Node
images since September 2026). Troubleshooting:

- *Host unreachable* right after installing the backend: use *Restart* on
  the project (a restart pulls the runtime images and recreates the
  container; plain stop/start does not). `ssh forward … "via":"network"` in
  the Envoryx log means the container still runs an image without socat.
- Gateway hangs or shows the host as unreachable although `ssh` works:
  set `ENVORYX_LOG_LEVEL=debug` and follow `docker logs Envoryx | grep '"ssh'`.
  Every command Gateway runs is logged with exit code and the first bytes
  of output, every tunnel with the listener it was relayed to and the
  bytes transferred.
- Changing the runtime (PHP version, Xdebug, …) recreates the container and
  ends the IDE backend; close the project in Gateway first.

## Xdebug

Runtime tab → PHP → **Xdebug**: enables step debugging for that project
(port 9003, mode `debug,develop`, `start_with_request=yes`). Xdebug connects
back to the machine that made the request – behind Envoryx's proxy that
address comes from `X-Forwarded-For` – and falls back to the *developer
machine* set in Settings → Project links (or a per-project override). Map
`/var/www/html` to your project folder in the IDE; the tab shows the exact
PhpStorm/VS Code settings. Turn it off when you are done: it slows PHP down.

## Notifications

Settings → **Notifications**: pick a channel (ntfy, Discord or Slack
webhook, Telegram bot, e-mail via SMTP, or a generic JSON webhook), choose
the events and send a test. Events: a project that should be running is
stopped/broken (and when it recovers), project creation failed, backup
failed, Let's Encrypt renewal failed/succeeded, Envoryx started. Repeats are
throttled (unhealthy project once per 6 h, failed renewal once per day).
Secrets live in `/config/notify.json` (0600). SMTP authentication requires
STARTTLS or TLS.

## AI assistants (MCP)

Envoryx ships an MCP server at `/mcp` (streamable HTTP). Create a token under
**Settings → API tokens & MCP**; the page shows a ready-to-paste client
configuration:

```json
{
  "mcpServers": {
    "envoryx": {
      "type": "http",
      "url": "https://envoryx.test/mcp",
      "headers": { "Authorization": "Bearer stq_…" }
    }
  }
}
```

Claude Code: `claude mcp add --transport http envoryx https://envoryx.test/mcp --header "Authorization: Bearer stq_…"`.
Use `http://<host>:8787/mcp` if the proxy/HTTPS is not set up.

Available tools: list/get projects, list runtimes, create project (PHP
version + extensions, database, Redis, Mailpit, Node, git clone, env),
start/stop/restart, get logs, list/run actions (composer, artisan, npm …),
list/create databases, list/create backups, add domain. Deleting projects,
dropping databases and restoring backups are intentionally not exposed –
do those in the UI. Example prompt: *"Create a Laravel project called
test-api with PHP 8.4, MariaDB and Redis, then run composer install."*

## Health check

`GET /api/v1/health` returns `{"status":"ok","docker":true,"database":true,…}`
without authentication. The image ships a `HEALTHCHECK` that calls
`envoryx healthcheck`.
