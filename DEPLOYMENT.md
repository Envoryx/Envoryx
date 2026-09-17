# Deployment

## Requirements

- Linux host with Docker Engine ≥ 24 (API ≥ 1.43); Unraid 6.12+ / 7.x
- x86_64 (arm64 images planned)
- A directory for Staqio's state (`/config`) and one for your projects (`/projects`)

## Docker Compose

```yaml
services:
  staqio:
    image: ghcr.io/seramos/staqio:latest
    container_name: staqio
    ports:
      - "8787:8787"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/user/appdata/staqio:/config
      - /mnt/user/development:/projects
    environment:
      PUID: 99
      PGID: 100
      STAQIO_PORT_RANGE_START: 20000
      STAQIO_PORT_RANGE_END: 20999
    restart: unless-stopped
```

`deploy/docker-compose.yml` contains a commented version of this file.

Start with `docker compose up -d`, open `http://<host>:8787` and create the
admin account. Project web servers are published on ports from the configured
range, e.g. `http://<host>:20000`.

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

## Domains and DNS (preview of Phase 4)

Currently projects are reached via `http://<host>:<port>`. Phase 4 adds a
central reverse proxy on ports 80/443 that routes by hostname
(`shimly-api.test`). Because Staqio runs on a remote server, `.test` names must
resolve to the server on every client machine. Options:

- **Router / Pi-hole / AdGuard DNS rewrite**: `*.test → <server-ip>` (recommended)
- **dnsmasq** on the server: `address=/.test/<server-ip>`
- **hosts file** per client: `<server-ip> shimly-api.test shop.test`

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

## Backups of Staqio itself

Back up `/config` (SQLite database + generated project configuration) and
`/projects`. Project backups from within Staqio arrive in Phase 7.

## Health check

`GET /api/v1/health` returns `{"status":"ok","docker":true,"database":true,…}`
without authentication. The image ships a `HEALTHCHECK` that calls
`staqio healthcheck`.
