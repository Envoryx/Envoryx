# Changelog

All notable changes to Envoryx are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (0.x: minor versions may change
behaviour, patch versions are fixes only).

Release images: `ghcr.io/envoryx/envoryx:<version>` and `:latest` (newest
release). `:main` follows the development branch.

## [Unreleased]

### Added
- Python runtime. The wizard's first step offers *Python application* next
  to PHP, Node.js and static; a Python container
  (`ghcr.io/envoryx/envoryx-python:<3.10–3.14>`, official slim image plus
  pip, uv, git and the build dependencies psycopg/mysqlclient/Pillow need)
  runs as the project owner with the project's `.venv` first on `PATH`.
  *Run the application server* makes the preset's command the container's
  main process: Django (`manage.py runserver`, gunicorn in production
  mode), Flask (`flask run --debug` / gunicorn), FastAPI and any ASGI app
  (uvicorn, `--reload` in dev mode), WSGI (gunicorn) or `python -m
  <module>`. Production mode also turns the frameworks' debuggers off
  (`DJANGO_DEBUG`, `FLASK_DEBUG`); the Django template ties its `DEBUG` to
  the first. Without PHP the project URL and extra domains reach the Python
  server through the proxy, the web container's port stays unpublished and
  a blank project waits for its entry file (`manage.py`, `main.py` …)
  instead of crash-looping. A Node dev server next to Python keeps
  `<project>-dev.<base>` – Django/FastAPI backend plus Vite frontend.
- Python templates: *Django* (`startproject config`, settings prepared for
  the proxy and `DATABASE_URL` via dj-database-url, psycopg and mysqlclient
  installed), *Flask* and *FastAPI* – each creates the `.venv`, installs
  the packages and pins `requirements.txt`.
- Python actions (`python --version`, `python -m venv`, `pip install -r
  requirements.txt`, `pip freeze`, `uv sync`, `uv lock`, Django `migrate`,
  `makemigrations`, `collectstatic`, `check`, `flush`) and worker presets
  (Python script, Python module, `manage.py` command, Celery worker, Celery
  beat) in the Python image; SSH user `<project>.python`; PyCharm/VS Code
  interpreter hints and a debugpy card (published port, path mapping,
  command lines) on the IDE tab; `pythonPresets` on `/api/v1/runtimes`;
  The debugpy port does not depend on the application server: a tooling
  container publishes it too, because what a developer steps through is
  usually a management command or a script started from the terminal, and
  debugpy attaches to whatever process you launch.
  MCP `create_project` takes `pythonVersion`, `pythonServer`,
  `pythonPreset`, `pythonApp`, `pythonPort`, `pythonMode`. Backups skip
  `.venv` and `__pycache__` with the other dependency caches. The weekly
  runtime-version workflow and the image builds cover Python like PHP and
  Node.

### Fixed
- A new project with the default PostgreSQL 18 came up with a database
  container in a restart loop: the data volume was mounted at
  `/var/lib/postgresql/data`, and the 18 image – which keeps its cluster in
  `/var/lib/postgresql/<major>/docker` – refuses to start when it finds a
  volume on the old path, even an empty one. From 18 on the volume takes
  `/var/lib/postgresql` (the layout `pg_upgrade --link` expects); 16 and 17
  keep the data directory itself, so existing volumes stay where they are.
  A major upgrade was already refused for PostgreSQL, so no data moves.

## [0.5.0] – 2026-09-22

### Added
- Projects without PHP. The first wizard step asks for the runtime – *PHP
  application*, *Node.js application* or *Static site* – and PHP is no
  longer required. For a Node.js project the dev server is the application:
  `https://<project>.<base>`, extra domains and `<project>-dev.<base>` all
  reach it through the proxy (HMR included), the project's direct port is
  the node container's host port, and a blank project waits for a
  `package.json` instead of crash-looping. Git, templates, actions, the
  terminal, the IDE tab (WebStorm/VS Code over SSH with user `<project>`),
  database, Redis, Mailpit and object storage variables all work without a
  PHP container. A static site is the web server alone with a starter
  `index.html`.
- Node templates: *Vite + React (TypeScript)*, *Next.js (App Router,
  TypeScript)* and *Nuxt* (Nuxt 4, minimal template) scaffold in a one-shot
  container from the Node image and preset the dev server; the Vite template
  sets the document root to `dist` for later static builds.
- Nuxt preset for the dev server (`--host --port`, default port 3000); each
  preset carries a default port that the wizard fills in when you switch.
- *SPA fallback to index.html* for static sites (Web server card and
  wizard): unknown paths return `index.html` so client-side routers survive
  a reload.
- API: `serves` (`php`/`node`/`static`) and `appService` on projects,
  `web.spaFallback`, `nodePresets` and template runtimes in `/runtimes`;
  MCP `create_project` accepts `nodePreset`, `nodeScript`, `nodePort` and
  `nodePackageManager`, its output carries `serves`, `devUrl` and a
  `directUrl` that points at the dev server for Node-only projects;
  `get_logs` defaults to the application container. New action
  `node -v`.
- Mailpit also injects `SMTP_HOST` and `SMTP_PORT` next to the `MAIL_*`
  variables (Node mailers usually read those).
- PHP can be added to or removed from a project after creation (Runtime tab
  → PHP → *Enable PHP*; API `php.enabled`). Adding makes PHP the
  application (FastCGI, project URL, published HTTP port); removing hands
  the project back to the dev server or the static document root, pauses
  PHP workers and keeps files and worker definitions.
- Node.js production build mode: the dev server can run as *Production
  build* – every start runs the build script, then the serve script
  (`start`, `preview` for Vite) with `NODE_ENV=production` for the serve
  process only.
- Node.js workers: *npm script* (`npm run <name>`) and *Node.js script*
  (`node <file>`) presets run from the Node image; the Workers tab offers
  the presets whose runtime the project has, and a worker whose runtime is
  missing pauses until it is back.
- Node.js debugging: publish the inspector port on a host port of its own
  (Runtime tab); the IDE tab shows host, port, path mapping and
  `package.json` examples for Next.js, Vite, Nuxt and plain Node.
- Whatever Envoryx does on its own is visible: the dashboard shows a
  dismissible notice with the projects it started again after a restart and
  the orphaned resources it removed, notifications carry the new kinds
  `projects.resumed` and `docker.orphans_removed`, and the audit log reads
  in plain words – actions as labels instead of `docker.orphans_removed`,
  "Envoryx (automatic)" as the actor of automatic entries, and a details
  column with what was changed or removed.
- Orphaned Envoryx containers and networks – left behind by a restored
  instance backup or a wiped `/config` – are stopped and removed by the
  reconciler about a minute after they appear, instead of lingering in the
  host's Docker list. Volumes hold data and are never removed automatically;
  the Docker page lists them with a *Remove* button. Removals appear in the
  audit log as `docker.orphans_removed`.
- Settings → General → *Projects and the Envoryx container*: an opt-in that
  stops every running project when the Envoryx container is stopped (for
  maintenance, a host shutdown) and starts them again when Envoryx comes
  back – also after a reboot of the host. Off by default: the project
  containers stay independent of Envoryx as before. A restart Envoryx asks
  for itself does not bounce the projects. Give the Envoryx container a stop
  timeout that covers all projects (see DEPLOYMENT.md, *Stopping and
  restarting*).
- The logo leads to "The spatial foundry", a short ASCII film about the
  build factory, with the version, update status and links to the
  documentation, release notes, source and licence below it.

### Changed
- Actions of services a project does not have are no longer listed (a
  PHP-only project shows no npm actions, a Node-only project no
  composer/artisan ones); running an action of an absent service answers
  409. The Node service badge reads *Node.js 24*.
- The bare SSH user `<project>` lands in the application container: PHP as
  before, Node when the project has no PHP. `<project>.php` and
  `<project>.node` pick one explicitly on projects with both. PhpStorm
  configurations of PHP projects are unaffected.
- While a Node dev server serves a project, the web container's HTTP port is
  not published – the document root would otherwise expose the project
  root (`.env`, sources) on the LAN. Turning the dev server off publishes
  the same port again.
- Web server configs of projects without PHP deny dotfiles (`/.env`,
  `/.git/…`) on Caddy and Apache as Nginx already did; PHP configs are
  unchanged.
- Backups skip the framework build caches `.next/`, `.nuxt/` and `.output/`
  by default, like `vendor/` and `node_modules/`; *Include dependencies*
  covers them all.
- The project list is a table again: name, stack, state, resources and
  actions sit in the same columns on every row, however many services a
  project has. Badges follow a fixed order (runtime, web server, database,
  extras), the address is shown without its scheme, and small screens get a
  two-row layout with the state under the actions. *Restart* is offered only
  while a project runs; a stopped project has *Start*.

### Fixed
- One-shot containers (templates, git clone/pull/status) could "finish" with
  exit code 0 before their command had run: the exit wait was registered
  with Docker's default *not-running* condition, which a freshly created
  container already satisfies, and the cleanup then killed the still-running
  scaffold. The wait now asks for the next exit. Found by the Vite template
  smoke; the effect was a template that left the project directory empty.
- Git clone, pull and status work on projects without PHP: the one-shot
  container now comes from the project's Node image (or the default Node
  image for static sites) instead of failing for want of a PHP service.
- The starter page of a project without PHP is an `index.html` the web
  server can serve; previously an `index.php` was written that only ever
  showed its source.
- Existing Node-only dev-server projects (created with the *Enable PHP* box
  unticked or MCP `phpVersion: "none"`): the project URL now reaches the dev
  server. The node and web containers are recreated once at the next start
  (new command wrapper, unpublished web port); `<project>-dev.<base>` and
  the node host port keep working.

## [0.4.0] – 2026-09-20

### Added
- Rescue commands for a lost login: `envoryx admin reset-password`,
  `logout-all`, `revoke-tokens`, `reset` and `users`, run from the container
  shell (`docker exec -it envoryx envoryx admin …`). They act on the live
  database, need no restart and land in the audit log as `cli`. The sign-in
  page links to the instructions.
- The interface speaks eight more languages: French, Spanish, Italian, Dutch,
  Polish, Portuguese (Brazil), Russian and Ukrainian. Pick one in the sidebar;
  the browser language is used on first visit.
- Envoryx now shows what it is doing. Long actions – creating, starting,
  restarting, applying settings, deleting, backups and restores – report their
  current step (image pull with download progress, container recreation,
  template scaffolding …) in a panel at the bottom right, in the project list
  and on the project page; the outcome appears there as well. The wizard shows
  the same steps while a project is being created, and enabling JetBrains
  Gateway explains that the container is recreated instead of pausing silently.
  Actions started in another tab or by another user are visible too, and their
  buttons stay disabled until they finish. (`GET /api/v1/operations`,
  `status.operation` on projects)

### Fixed
- Signing in after being sent to the sign-in page (session expired, direct
  link) now returns to the page you wanted instead of the dashboard.

## [0.3.0] – 2026-09-20

### Added
- Envoryx has its logo: the `<E>` mark and wordmark replace the placeholder
  icon in the sidebar, on the sign-in page, as favicon and as the Unraid
  template icon. Mint is the default accent colour; **Settings → General →
  Appearance** offers ocean, violet, amber and rose – buttons, highlights and
  the logo follow. The choice is stored per browser, like the theme.
- Settings are organised in tabs (Diagnostics, General, Domains & HTTPS,
  Access, Notifications, Backups, Tools, Audit log). The new **Diagnostics**
  tab runs 14 set-up checks – Docker, storage, host paths, disk space, backup
  directory, database integrity, host for project links, proxy ports, wildcard
  DNS, SSH, HTTPS, version, project/Docker consistency, notifications – and
  lists each finding with a fix; some fix themselves at the click of a button.
  The dashboard shows a banner while something needs attention.
  (`GET /api/v1/system/diagnostics`)
- Project backups include the object storage bucket: every object as a plain
  file in `storage.tar.gz` (content type kept as an extended attribute),
  restorable with or without emptying the bucket first. Scheduled backups and
  the MCP `create_backup` tool include it automatically.

### Fixed
- The chosen theme was ignored on the sign-in and set-up pages (always light)
  and the app briefly flashed light on every load: the script applying the
  stored theme was blocked by Envoryx's own Content Security Policy.
- When Envoryx runs with an IP of its own (Unraid `br0`, macvlan) and no host
  for project links is configured, links to published ports – project URLs,
  Mailpit, the object storage console, database ports – silently pointed at
  Envoryx's address, where nothing listens. The dashboard, the affected
  project tabs and the settings now say so and offer the Docker host that
  Docker reports as a one-click fix.

## [0.2.0] – 2026-09-19

### Added
- S3-compatible object storage as an optional project service: one RustFS
  container per project with a persistent volume, generated access keys, a
  bucket named after the project created at start-up, `S3_*` and Laravel/AWS
  SDK `AWS_*` variables injected, the S3 API served by the embedded proxy as
  `<project>-s3.<base domain>` for presigned URLs and public assets, a web
  console, and a switch for anonymous reads (bucket policy standing in for
  public-read ACLs). Wizard, Services tab and MCP `create_project` know it.
- API tokens have scopes: `read` (look, no secrets), `operate` (work with
  existing projects – start/stop, actions, backups, databases, git, SSH) and
  `admin` (everything a browser session may do). A token can also be limited
  to particular projects; it then sees and touches only those and cannot
  create new ones. Scopes apply to the REST API, the MCP server and SSH/SFTP
  alike; refusals say which scope the operation needs. New tokens default to
  `operate`; tokens issued before this release keep full access.

### Fixed
- Deleting a project whose network is still used by a container Envoryx did
  not create (for example one attached through Unraid's network dropdown) is
  now refused up front, naming that container, instead of removing the
  project's containers and volumes first and then leaving it in `failed`.
- The image a project can roll back to is kept under a tag
  (`envoryx-rollback/<project>:<image>`) instead of lying around untagged, so
  `docker image prune` – Unraid's "remove unused images", clean-up plugins –
  no longer deletes it. Existing rollback targets are tagged at the next start;
  the tag moves on when a newer image supersedes it and goes with the project.
- A backup interrupted by a crash or `kill -9` no longer lingers: at start-up
  Envoryx removes project backup directories that never got their metadata
  and adopts complete ones whose database record was not written yet, so
  they show up and can be restored. Instance backups are written under a
  temporary name and renamed when complete, so a truncated archive can never
  be mistaken for a good one; leftovers are removed at start-up as well.

## [0.1.0] – 2026-09-19

First tagged release. Everything below is new.

### Projects
- Single-container deployment for Unraid and Linux Docker hosts, embedded
  web UI (English, German), local admin account, sessions, audit log.
- Per-project stacks from a fixed catalogue: PHP 8.1–8.6 (Envoryx images with
  toggleable extensions, Xdebug), Caddy/Apache/Nginx, MariaDB/MySQL/
  PostgreSQL/MongoDB, Redis, Mailpit, Node.js toolchain with dev-server mode.
- Templates (Laravel, Symfony, WordPress), git clone with deploy key or
  token, environment variables, php.ini settings, workers (queues,
  schedulers, scripts), project actions (composer, artisan, npm …), live
  logs, browser terminal.
- Embedded reverse proxy with local CA or Let's Encrypt (Cloudflare DNS)
  wildcard: `https://<project>.test` plus custom domains.
- Project backups (database dump, files, configuration) with schedules and
  retention, restore, download; optional separate `/backups` mount.
- Instance backups of Envoryx itself (database, CA, keys, settings), taken
  automatically before schema upgrades and restores; restore with in-place
  restart; import on another host.
- Embedded SSH/SFTP server for IDE remote interpreters, IDE tab with Xdebug
  and JDBC settings, optional JetBrains Gateway support.
- Notifications (ntfy, Discord, Slack, Telegram, e-mail, webhook) for
  unhealthy projects, failed creations, backups and certificate renewals.
- MCP server for AI assistants with personal API tokens.
- Image rollback: after a rebuilt image tag was applied by a restart, the
  Overview offers *Roll back* to the previous image; the previous image is
  kept out of pruning.
- Database browser: optional Adminer container shared by all projects,
  opened from the Database tab already logged in, served under the Envoryx
  UI behind the session.

### Reliability
- Startup refuses corrupt databases and network filesystems for `/config`
  (FUSE warning for `/mnt/user/appdata` on Unraid), watches disk space,
  restarts crashed background tasks and reports failed starts.
- Project operations run to completion when the browser tab closes or the
  connection drops; on `docker stop` running operations get
  `ENVORYX_SHUTDOWN_GRACE` (8 s) to finish and are otherwise recorded as
  interrupted instead of failed.
- SQLite in `synchronous=FULL` mode for power-loss safety.

### API
- REST API accepts personal API tokens as `Authorization: Bearer` for
  scripts and CLIs; tokens cannot change the password or manage tokens.
- Daily update check against GitHub releases (`ENVORYX_UPDATE_CHECK=false`
  disables it); the dashboard and Settings show when a newer release exists.

[Unreleased]: https://github.com/envoryx/envoryx/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/envoryx/envoryx/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/envoryx/envoryx/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/envoryx/envoryx/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/envoryx/envoryx/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/envoryx/envoryx/releases/tag/v0.1.0
