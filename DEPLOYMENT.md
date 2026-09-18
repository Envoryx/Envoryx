# Deployment

## Requirements

- Linux host with Docker Engine ≥ 24 (API ≥ 1.43); Unraid 6.12+ / 7.x or
  any other Linux distribution
- x86_64 or arm64 (Raspberry Pi 4/5, Ampere/Graviton, Apple Silicon under
  Linux); all Staqio images are multi-arch
- A directory for Staqio's state (`/config`) and one for your projects (`/projects`)

## Docker Compose (any Linux host)

Unraid is the primary target but nothing depends on it. On another Linux host
pick two directories and your own uid/gid:

```yaml
services:
  staqio:
    image: ghcr.io/seramos/staqio:latest
    container_name: staqio
    ports:
      - "8787:8787"
      - "80:80"     # proxy: projects by domain (optional, any free host port)
      - "443:443"   # proxy: HTTPS via local CA (optional)
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/user/appdata/staqio:/config
      - /mnt/user/development:/projects
    environment:
      PUID: 99      # Unraid: nobody; elsewhere: your uid (`id -u`)
      PGID: 100     # Unraid: users;  elsewhere: your gid (`id -g`)
      STAQIO_PORT_RANGE_START: 20000
      STAQIO_PORT_RANGE_END: 20999
    restart: unless-stopped
```

`deploy/docker-compose.yml` contains a commented version of this file.
Replace `/mnt/user/...` with e.g. `/srv/staqio` and `/home/you/dev` on a
regular server.

Start with `docker compose up -d`, open `http://<host>:8787` and create the
admin account. Project web servers are published on ports from the configured
range, e.g. `http://<host>:20000`, and – with the proxy ports mapped and DNS
set up (see [Domains and HTTPS](#domains-and-https)) – as
`https://<project>.test`.

### Building locally

```
docker build -t ghcr.io/seramos/staqio:dev --build-arg VERSION=dev .
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `STAQIO_LISTEN` | `:8787` | Listen address |
| `STAQIO_CONFIG_DIR` | `/config` | Persistent state (SQLite, generated configs, backups) |
| `STAQIO_PROJECTS_DIR` | `/projects` | Root of all project directories |
| `STAQIO_PROJECTS_HOST_PATH` | auto | Host path behind `/projects` (see below) |
| `STAQIO_CONFIG_HOST_PATH` | auto | Host path behind `/config` |
| `DOCKER_HOST` | unix socket | Docker endpoint; set to a socket proxy URL if used |
| `PUID` / `PGID` | `99` / `100` | uid/gid project containers run as and project dirs are owned by |
| `STAQIO_PORT_RANGE_START` / `_END` | `20000` / `20999` | Host ports assigned to project web servers |
| `STAQIO_PUBLIC_HOST` | browser address | Host/IP used for project links (see below); also editable in Settings |
| `STAQIO_PROXY_HTTP` | `:80` | Listen address of the embedded proxy inside the container; empty disables it |
| `STAQIO_PROXY_HTTPS` | `:443` | HTTPS listener of the proxy (local CA); empty disables HTTPS |
| `STAQIO_ADMIN_USER` / `STAQIO_ADMIN_PASSWORD` | – | Create the first admin non-interactively |
| `STAQIO_SESSION_IDLE_TIMEOUT` | `12h` | Sliding session expiry |
| `STAQIO_SESSION_ABSOLUTE_TIMEOUT` | `168h` | Hard session expiry |
| `STAQIO_SECURE_COOKIES` | `false` | Mark cookies `Secure` (enable behind HTTPS) |
| `STAQIO_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `STAQIO_LOG_FORMAT` | `json` | `json` or `text` |

## Host paths (important)

Project containers mount your project files with Docker **bind mounts**. Bind
mount sources are resolved by the Docker daemon **on the host**, not inside the
Staqio container. Staqio therefore needs to know that `/projects` inside its
container is `/mnt/user/development` on the host.

Staqio detects this automatically by inspecting its own container's mounts. If
detection fails (the dashboard shows a red banner and project creation is
disabled), set the two variables explicitly:

```
STAQIO_PROJECTS_HOST_PATH=/mnt/user/development
STAQIO_CONFIG_HOST_PATH=/mnt/user/appdata/staqio
```

On Unraid always use `/mnt/user/...` (or `/mnt/cache/...`) paths – the same
ones you used in the volume mappings.

## Project links and the Staqio container's own IP

Project web servers publish their ports on the **Docker host** (the Unraid
IP). Staqio builds project links from the address in your browser's address
bar. If you reach Staqio under a different address – the container has its
own IP on `br0`/macvlan, or you use a reverse proxy – set the host to use for
project links in **Settings → Project links** (or `STAQIO_PUBLIC_HOST`),
typically the Unraid IP.

## Unraid

### Option A: Template (recommended, one click)

Copy the template to your flash drive so it appears under
**Docker → Add Container → Select a template → User templates**:

```
wget -O /boot/config/plugins/dockerMan/templates-user/staqio.xml \
  https://raw.githubusercontent.com/seramos/staqio/main/deploy/unraid/staqio.xml
```

Then *Add Container → Staqio → Apply*. Ports, paths, socket, PUID/PGID are
pre-filled; create the `development` share first if it does not exist.

Alternatively add `https://github.com/seramos/staqio` under
**Docker → Add Container → Template repositories** – Unraid then reads
`deploy/unraid/staqio.xml` directly from the repository and picks up updates.

No publication in Community Applications is required for either way.

### Option B: Manual

**Docker → Add Container**:

| Setting | Value |
|---------|-------|
| Repository | `ghcr.io/seramos/staqio:latest` |
| Network type | bridge |
| Port | `8787` → `8787` |
| Port | `80` → `80` and `443` → `443` (proxy; optional, other host ports work) |
| Path `/config` | `/mnt/user/appdata/staqio` |
| Path `/projects` | `/mnt/user/development` (create the share first) |
| Path `/var/run/docker.sock` | `/var/run/docker.sock` |
| Variable `PUID` | `99` |
| Variable `PGID` | `100` |

Project ports (20000–20999 by default) are published by the project containers
themselves, not by the Staqio container – nothing else to map in the template.
Unraid shows Staqio's containers (`staqio-<project>-…`) in the Docker tab; you
can leave them alone, Staqio manages them.

### Image visibility

The image is built by GitHub Actions (`.github/workflows/docker.yml`) and
pushed to `ghcr.io/seramos/staqio`. GitHub creates new packages as **private**;
set the package to *public* once (GitHub → Packages → staqio → Package settings
→ Change visibility) so Unraid can pull it without credentials. For a private
package run `docker login ghcr.io` on the Unraid server instead (add it to
`/boot/config/go` to survive reboots).

### File ownership

Files created by PHP inside a project are owned by `PUID:PGID` (Unraid default
`nobody:users` = `99:100`), so they are editable via SMB shares. Directories
created by Staqio itself are chowned to the same ids.

## Domains and HTTPS

Staqio contains a reverse proxy that routes requests by host name to the
project's web server and serves the Staqio UI itself. It listens on ports 80
and 443 **inside** the container; map them to host ports (80/443 or any free
ones – Staqio reads its own port bindings and adjusts links accordingly).
When neither port is published, Settings → *Domains & HTTPS* shows a warning
and project links keep using the direct port.

**Own IP (Unraid `br0`, macvlan/ipvlan) or host networking:** there is no
port mapping – the proxy is reachable directly on the container's address
(`http(s)://<staqio-ip>`). Staqio detects this and shows the address in the
settings. Your DNS entries for `*.test` must then point at the **Staqio IP**,
not at the Unraid IP (which is where the direct project ports live).

### Names

- Base domain, default `test` (Settings → Domains & HTTPS). Every project is
  `<slug>.<base>` (`shop.test`), the UI is `staqio.<base>`.
- Additional names per project in its **Domains** tab (`shop.local`,
  `api.shop.test`, …). Each name must be unique across projects.
- The names must resolve to the Staqio host on every client. Because Staqio
  runs on a server, use one of:
  - **Pi-hole / AdGuard Home**: DNS rewrite `*.test → <server-ip>`
    (AdGuard: *Filters → DNS rewrites*, domain `*.test`; Pi-hole ≥ 6:
    *Local DNS → DNS Records*, or `dnsmasq` line below in `/etc/dnsmasq.d/`)
  - **dnsmasq** (router, server): `address=/.test/<server-ip>`
  - **hosts file** per client (`/etc/hosts`, `C:\Windows\System32\drivers\etc\hosts`):
    `<server-ip> shop.test staqio.test`
  - macOS: `/etc/resolver/test` with `nameserver <dns-ip>` if you run your
    own DNS only for that zone.
- `.test` is reserved for exactly this purpose (RFC 6761) and never resolves
  on the public internet. `.local` collides with mDNS on macOS/Linux – prefer
  `.test` or an owned domain (`dev.example.com`).

### HTTPS

On first start Staqio creates a local certificate authority under
`/config/ca/` (`ca.key` 0600, `ca.crt`). The proxy issues a certificate per
host name on demand, so `https://shop.test` works as soon as the CA is
trusted on the client:

1. Settings → Domains & HTTPS → **Download staqio-ca.crt**.
2. Install it as a trusted root (instructions per OS are shown next to the
   button; Firefox has its own store).

**Force HTTPS** redirects `http://` requests for known names to HTTPS.

### Let's Encrypt instead of the local CA (no installation on clients)

If you own a domain, Staqio can obtain and renew a **public wildcard
certificate** itself, so every browser trusts project URLs without any
CA installation. It uses the ACME dns-01 challenge: Staqio creates a
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
   Staqio's address. Do not create a public record for it.

Staqio requests the certificate in the background (1–2 minutes, status is
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
needs `setcap cap_net_bind_service=+ep ./staqio` or other addresses
(`STAQIO_PROXY_HTTP=:8080`).

## Updating

```
docker compose pull && docker compose up -d
```

Staqio's state lives in `/config` and `/projects` only. Replacing the image
never touches projects. Database migrations run automatically and are forward
only; a database newer than the binary is refused with a clear error, so keep a
copy of `/config/staqio.db` before downgrading.

## Keeping PHP up to date

- **Patch releases** (e.g. 8.5.3 → 8.5.4): the `staqio-php` images are rebuilt
  weekly from the official `php` images. A project **Restart** pulls the tag
  again and recreates the container only if the image actually changed. Until
  then the project keeps running on the previous build – nothing changes
  behind your back.
- **New minor versions** (e.g. 8.6): a weekly workflow compares
  `internal/runtime/php_versions.json` with endoflife.date and Docker Hub and
  opens a pull request when a version appears, becomes stable or reaches EOL.
  Merging it builds the images and the next Staqio image shows the version in
  the wizard. Pre-release versions are marked *preview*.

Node.js images (`staqio-node:*`) follow the same scheme with `node_versions.json`.

## Git deploy key

For SSH repositories Staqio generates an Ed25519 key pair on first use under
`/config/ssh/`. Copy the public key from **Settings → Git deploy key** (or the
project's Git tab) into your repository as a read-only deploy key. Private
HTTPS repositories use an access token per project instead (GitHub:
fine-grained PAT with *Contents: read*; GitLab: username `oauth2` + token).

## Backups

Project backups (database dump, files, configuration) are created from the
project's **Backups** tab and stored under `/config/backups/<project>/`. They
are plain directories – include `/config` in your regular Unraid backup
(e.g. Appdata Backup plugin) to get them off the machine, or use the
per-backup download.

**Scheduled backups**: Backups tab → *Scheduled backups*: daily or weekly at
a given hour (server local time – set `TZ` on the container for your zone),
keep the last N scheduled backups (manual ones are never deleted), optionally
including `vendor/`/`node_modules/`. Failures raise a notification.

For Staqio itself back up `/config` (SQLite database, generated configuration,
deploy key, backups) and `/projects`.

## Xdebug

Runtime tab → PHP → **Xdebug**: enables step debugging for that project
(port 9003, mode `debug,develop`, `start_with_request=yes`). Xdebug connects
back to the machine that made the request – behind Staqio's proxy that
address comes from `X-Forwarded-For` – and falls back to the *developer
machine* set in Settings → Project links (or a per-project override). Map
`/var/www/html` to your project folder in the IDE; the tab shows the exact
PhpStorm/VS Code settings. Turn it off when you are done: it slows PHP down.

## Notifications

Settings → **Notifications**: pick a channel (ntfy, Discord or Slack
webhook, Telegram bot, e-mail via SMTP, or a generic JSON webhook), choose
the events and send a test. Events: a project that should be running is
stopped/broken (and when it recovers), project creation failed, backup
failed, Let's Encrypt renewal failed/succeeded, Staqio started. Repeats are
throttled (unhealthy project once per 6 h, failed renewal once per day).
Secrets live in `/config/notify.json` (0600). SMTP authentication requires
STARTTLS or TLS.

## AI assistants (MCP)

Staqio ships an MCP server at `/mcp` (streamable HTTP). Create a token under
**Settings → API tokens & MCP**; the page shows a ready-to-paste client
configuration:

```json
{
  "mcpServers": {
    "staqio": {
      "type": "http",
      "url": "https://staqio.test/mcp",
      "headers": { "Authorization": "Bearer stq_…" }
    }
  }
}
```

Claude Code: `claude mcp add --transport http staqio https://staqio.test/mcp --header "Authorization: Bearer stq_…"`.
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
`staqio healthcheck`.
