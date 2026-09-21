# Changelog

All notable changes to Envoryx are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (0.x: minor versions may change
behaviour, patch versions are fixes only).

Release images: `ghcr.io/envoryx/envoryx:<version>` and `:latest` (newest
release). `:main` follows the development branch.

## [Unreleased]

### Added
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
- The project list is a table again: name, stack, state, resources and
  actions sit in the same columns on every row, however many services a
  project has. Badges follow a fixed order (runtime, web server, database,
  extras), the address is shown without its scheme, and small screens get a
  two-row layout with the state under the actions. *Restart* is offered only
  while a project runs; a stopped project has *Start*.

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

[Unreleased]: https://github.com/envoryx/envoryx/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/envoryx/envoryx/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/envoryx/envoryx/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/envoryx/envoryx/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/envoryx/envoryx/releases/tag/v0.1.0
