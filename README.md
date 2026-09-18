# Staqio

**Docker-native development environments for Unraid and Linux servers.**

Staqio runs as a single container on any Linux Docker host (x86_64 or
arm64; Unraid is the primary target) and manages complete
development stacks – web server, PHP runtime, database, cache – as isolated,
per-project Docker environments. Everything is controlled from a modern web UI;
no `docker-compose.yml` editing required.

```
Open Staqio → Create project → PHP 8.4 + Caddy → Create → project is running
```

## Status

Staqio is under active development. The current milestone (Phase 1 + 2) delivers:

- single-container deployment with embedded web UI (Go + React)
- local admin account, secure sessions, audit log
- Docker engine integration that only ever touches resources labelled `staqio.managed=true`
- project templates: Laravel, Symfony (skeleton + webapp), WordPress – scaffolded
  in a one-shot container as the project owner, wired to the project database
- project wizard: name, directory, document root, PHP version, php.ini settings
  and extensions (pdo_mysql, mysqli, pdo_pgsql, mongodb, gd, intl, zip, bcmath,
  opcache, imagick), Caddy web server, environment variables, plan preview
- per-project Docker network, PHP-FPM container (Staqio image with Composer)
  and Caddy container
- MariaDB, MySQL, PostgreSQL or MongoDB per project: persistent volume, generated
  credentials, connection variables injected into PHP, optional host port for
  desktop clients, password rotation, create/drop databases, in-place version
  upgrades where the server supports them
- Redis (persistent volume, `REDIS_URL`) and Mailpit (SMTP catcher with web
  inbox, `MAIL_*`/`MAILER_DSN`) as optional services
- project files bind-mounted from `/projects/<name>` on the host
- projects reachable at `http://<host>:<port>` (port auto-assigned) and,
  through the embedded reverse proxy, as `https://<project>.test` plus any
  additional domains; certificates from a local CA (download once, trust on
  your devices), or a Let's Encrypt wildcard for your own domain obtained
  and renewed automatically via DNS challenge (Cloudflare) – nothing to
  install anywhere
- start / stop / restart / edit / delete with confirmation
- live logs per container over WebSocket: pause, search, stderr filter, download
- browser terminal (xterm.js) into any project container; application
  containers run the shell as the project owner (PUID/PGID)
- Node.js toolchain container per project (npm, pnpm, yarn via corepack),
  version selectable, addable later; optional dev-server mode (Vite, Next.js,
  …) reachable as `https://<project>-dev.<base>` through the proxy with HMR
- Git: clone in the wizard (HTTPS with access token or SSH with a Staqio
  deploy key), pull, branch switch, status – all inside short-lived containers
  as the project owner; the deploy key is never mounted into app containers
- project actions: composer install/update, artisan migrate/seed/cache,
  Symfony console, npm/pnpm/yarn – a fixed catalogue of argv commands with
  live output, shown only when the project has the matching files
- desired-state reconciliation on startup and periodically; orphan detection
- diagnostics view of all Docker resources (foreign containers read-only)

- backups per project: database dump + project files (optionally without
  vendor/node_modules) + configuration under `/config/backups/<project>/`,
  restore with typed confirmation, download as a single archive

- notifications (ntfy, Discord, Slack, Telegram, e-mail, generic webhook) for
  unhealthy projects (and their recovery), failed project creation, failed
  backups and certificate renewals – throttled, secrets never returned
- MCP server for AI assistants (Claude Code, Cursor, …): create, start, stop
  and inspect projects, read logs, run actions, create databases and backups
  – authenticated with personal API tokens, same validation and audit trail
  as the UI, no destructive tools

All phases of the original plan are implemented – see
[ARCHITECTURE.md](ARCHITECTURE.md) §13.

## Quick start

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
    restart: unless-stopped
```

Open `http://<server>:8787`, create the admin account, click **New project**.

### Unraid

Install the template once from the Unraid terminal (or via SSH) – it lands on
the flash drive next to your other user templates:

```sh
wget -O /boot/config/plugins/dockerMan/templates-user/staqio.xml \
  https://raw.githubusercontent.com/seramos/staqio/main/deploy/unraid/staqio.xml
```

Then go to **Docker → Add Container**, pick **Staqio** under *User
templates* and click **Apply** – ports, `/config`, `/projects`, the Docker
socket and `PUID`/`PGID` are pre-filled. Create the `development` share first
if it does not exist yet. Open `http://<unraid-ip>:8787` and create the
admin account.

For domains and HTTPS (`https://shop.test`) map the proxy ports 80/443 (bridge)
or give the container its own IP on `br0` – see
[Domains and HTTPS](DEPLOYMENT.md#domains-and-https).

Details, environment variables and Unraid notes: [DEPLOYMENT.md](DEPLOYMENT.md).

## How it works

```
Browser ──▶ Staqio (Go API + React UI) ──▶ Docker Engine
                                             ├── staqio-<project>      (network)
                                             ├── staqio-<project>-web  (Caddy, :port → 80)
                                             └── staqio-<project>-php  (PHP-FPM)
```

- Staqio stores the *desired state* of each project in SQLite (`/config/staqio.db`).
- The Docker engine holds the *actual state*. Staqio reconciles both, never trusting
  the database alone – restarting or updating Staqio never loses projects.
- Every resource Staqio creates carries `staqio.managed=true` and
  `staqio.project.id=<uuid>`. Staqio refuses to modify anything else.

## Documentation

| Document | Content |
|----------|---------|
| [ARCHITECTURE.md](ARCHITECTURE.md) | design, data model, lifecycle, phases |
| [SECURITY.md](SECURITY.md) | threat model, Docker socket, hardening |
| [DEPLOYMENT.md](DEPLOYMENT.md) | Docker / Unraid deployment, configuration |
| [DEVELOPMENT.md](DEVELOPMENT.md) | building, running and testing locally |

## License

Staqio is free software under the **GNU Affero General Public License v3.0**
(AGPL-3.0) – see [LICENSE](LICENSE). You may use it freely, also commercially.
If you modify and distribute it, or offer a modified version as a network
service, you must publish your changes under the same license.

Copyright (c) 2026 Stefan Mertens

The projects you run *inside* Staqio are not affected by this license.
