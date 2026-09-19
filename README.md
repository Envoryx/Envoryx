# Envoryx

**Docker-native development environments for Unraid and Linux servers.**

Envoryx runs as a single container on any Linux Docker host (x86_64 or
arm64; Unraid is the primary target) and manages complete
development stacks – web server, PHP runtime, database, cache – as isolated,
per-project Docker environments. Everything is controlled from a modern web UI;
no `docker-compose.yml` editing required.

```
Open Envoryx → Create project → PHP 8.4 + Caddy → Create → project is running
```

## Status

Envoryx is under active development. The current milestone (Phase 1 + 2) delivers:

- single-container deployment with embedded web UI (Go + React), English and
  German interface (more languages are one JSON file each)
- local admin account, secure sessions, audit log
- Docker engine integration that only ever touches resources labelled `envoryx.managed=true`
- project templates: Laravel, Symfony (skeleton + webapp), WordPress – scaffolded
  in a one-shot container as the project owner, wired to the project database
- project wizard: name, directory, document root, PHP version, php.ini settings
  and extensions (pdo_mysql, mysqli, pdo_pgsql, mongodb, gd, intl, zip, bcmath,
  opcache, imagick), Xdebug switch with IDE setup hints, web server (Caddy, Apache or Nginx), environment variables, plan preview
- per-project Docker network, PHP-FPM container (Envoryx image with Composer)
  and web server container: Caddy (default), Apache (with `.htaccess` support)
  or Nginx – switchable after creation
- MariaDB, MySQL, PostgreSQL or MongoDB per project: persistent volume, generated
  credentials, connection variables injected into PHP, optional host port for
  desktop clients, password rotation, create/drop databases, in-place version
  upgrades where the server supports them
- Redis (persistent volume, `REDIS_URL`), Mailpit (SMTP catcher with web
  inbox, `MAIL_*`/`MAILER_DSN`) and S3-compatible object storage (RustFS: a
  bucket per project, web console, `S3_*`/`AWS_*` injected, reachable from the
  browser for presigned URLs) as optional services
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
- Git: clone in the wizard (HTTPS with access token or SSH with a Envoryx
  deploy key), pull, branch switch, status – all inside short-lived containers
  as the project owner; the deploy key is never mounted into app containers
- workers per project: Laravel scheduler / queue worker / Horizon / Reverb,
  Symfony Messenger and Scheduler, PHP scripts, composer scripts – each in its
  own auto-restarting container from the PHP image, with logs
- project actions: composer install/update, artisan migrate/seed/cache,
  Symfony console, npm/pnpm/yarn – a fixed catalogue of argv commands with
  live output, shown only when the project has the matching files
- desired-state reconciliation on startup and periodically; orphan detection
- diagnostics view of all Docker resources (foreign containers read-only)

- backups per project: database dump + project files (optionally without
  vendor/node_modules) + configuration, stored under `/config/backups` or an
  optional separate `/backups` mount (e.g. on the Unraid array),
  restore with typed confirmation, download as a single archive; daily/weekly
  schedules with retention per project
- instance backups: Envoryx's own database, CA, SSH keys and configuration as
  one downloadable archive – taken automatically before every schema upgrade,
  restorable (or importable on another host) from the settings with an in-place
  restart

- database browser: optional Adminer container shared by all projects,
  started on first use, opened from the Database tab already logged in,
  served under the Envoryx UI so the session protects it
- IDE integration: embedded SSH server for PhpStorm/VS Code remote
  interpreters and SFTP into project containers (API token or public key),
  an IDE tab with Xdebug server/path mapping, `.idea/php.xml`, JDBC URLs;
  optional JetBrains Gateway support (backend in the container, shared cache)
- notifications (ntfy, Discord, Slack, Telegram, e-mail, generic webhook) for
  unhealthy projects (and their recovery), failed project creation, failed
  backups and certificate renewals – throttled, secrets never returned
- MCP server for AI assistants (Claude Code, Cursor, …): create, start, stop
  and inspect projects, read logs, run actions, create databases and backups
  – authenticated with personal API tokens, same validation and audit trail
  as the UI, no destructive tools

All phases of the original plan are implemented – see
[ARCHITECTURE.md](ARCHITECTURE.md) §13. Releases are listed in
[CHANGELOG.md](CHANGELOG.md); `:latest` is the newest release, `:main` the
development branch.

## Quick start

```yaml
services:
  envoryx:
    image: ghcr.io/envoryx/envoryx:latest
    container_name: envoryx
    ports:
      - "8787:8787"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/user/appdata/envoryx:/config
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
wget -O /boot/config/plugins/dockerMan/templates-user/my-Envoryx.xml \
  https://raw.githubusercontent.com/envoryx/envoryx/main/deploy/unraid/envoryx.xml
```

Then go to **Docker → Add Container**, pick **Envoryx** under *User
templates* and click **Apply** – ports, `/config`, `/projects`, the Docker
socket and `PUID`/`PGID` are pre-filled. Create the `development` share first
if it does not exist yet. Keep the file name `my-Envoryx.xml` and download it
only once – Unraid stores your container settings in it, and a second copy
under another name would make *Edit*/*Update* fall back to the defaults (see
[DEPLOYMENT.md](DEPLOYMENT.md)). Open `http://<unraid-ip>:8787` and create the
admin account.

For domains and HTTPS (`https://shop.test`) map the proxy ports 80/443 (bridge)
or give the container its own IP on `br0` – see
[Domains and HTTPS](DEPLOYMENT.md#domains-and-https).

Details, environment variables and Unraid notes: [DEPLOYMENT.md](DEPLOYMENT.md).

## How it works

```
Browser ──▶ Envoryx (Go API + React UI) ──▶ Docker Engine
                                             ├── envoryx-<project>      (network)
                                             ├── envoryx-<project>-web  (Caddy/Apache/Nginx, :port → 80)
                                             └── envoryx-<project>-php  (PHP-FPM)
```

- Envoryx stores the *desired state* of each project in SQLite (`/config/envoryx.db`).
- The Docker engine holds the *actual state*. Envoryx reconciles both, never trusting
  the database alone – restarting or updating Envoryx never loses projects.
- Every resource Envoryx creates carries `envoryx.managed=true` and
  `envoryx.project.id=<uuid>`. Envoryx refuses to modify anything else.

## Documentation

| Document | Content |
|----------|---------|
| [ARCHITECTURE.md](ARCHITECTURE.md) | design, data model, lifecycle, phases |
| [SECURITY.md](SECURITY.md) | threat model, Docker socket, hardening |
| [DEPLOYMENT.md](DEPLOYMENT.md) | Docker / Unraid deployment, configuration |
| [DEVELOPMENT.md](DEVELOPMENT.md) | building, running and testing locally |
| [CONTRIBUTING.md](CONTRIBUTING.md) | how to contribute, Contributor License Agreement |

## License

Envoryx is free software under the **GNU Affero General Public License v3.0**
(AGPL-3.0) – see [LICENSE](LICENSE). You may use it freely, also commercially.
If you modify and distribute it, or offer a modified version as a network
service, you must publish your changes under the same license.

Copyright (c) 2026 Stefan Mertens

The projects you run *inside* Envoryx are not affected by this license.

Contributions require a one-time signature of the
[Contributor License Agreement](CLA.md) – see
[CONTRIBUTING.md](CONTRIBUTING.md).
