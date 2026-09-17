# Staqio

**Docker-native development environments for Unraid and Linux servers.**

Staqio runs as a single container on your Docker host and manages complete
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
- project wizard: name, directory, document root, PHP version, php.ini settings
  and extensions (pdo_mysql, mysqli, pdo_pgsql, gd, intl, zip, bcmath, opcache,
  imagick), Caddy web server, environment variables, plan preview
- per-project Docker network, PHP-FPM container (Staqio image with Composer)
  and Caddy container
- MariaDB per project: persistent volume, generated credentials, connection
  variables injected into PHP, optional host port for desktop clients,
  password rotation, create/drop databases, in-place version upgrades
- project files bind-mounted from `/projects/<name>` on the host
- projects reachable at `http://<host>:<port>` (port auto-assigned)
- start / stop / restart / edit / delete with confirmation
- live logs per container over WebSocket: pause, search, stderr filter, download
- browser terminal (xterm.js) into any project container; application
  containers run the shell as the project owner (PUID/PGID)
- Git: clone in the wizard (HTTPS with access token or SSH with a Staqio
  deploy key), pull, branch switch, status – all inside short-lived containers
  as the project owner; the deploy key is never mounted into app containers
- project actions: composer install/update, artisan migrate/seed/cache,
  Symfony console, npm/pnpm/yarn – a fixed catalogue of argv commands with
  live output, shown only when the project has the matching files
- desired-state reconciliation on startup and periodically; orphan detection
- diagnostics view of all Docker resources (foreign containers read-only)

MySQL/PostgreSQL, Redis, Node.js, terminal, logs, Git, backups, domains/HTTPS
and MCP follow in later phases – see [ARCHITECTURE.md](ARCHITECTURE.md) §13.

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

**Unraid:** copy [`deploy/unraid/staqio.xml`](deploy/unraid/staqio.xml) to
`/boot/config/plugins/dockerMan/templates-user/` and add the container from the
template – everything is pre-filled.

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
