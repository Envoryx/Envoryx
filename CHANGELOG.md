# Changelog

All notable changes to Envoryx are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (0.x: minor versions may change
behaviour, patch versions are fixes only).

Release images: `ghcr.io/envoryx/envoryx:<version>` and `:latest` (newest
release). `:main` follows the development branch.

## [Unreleased]

### Added
- Project backups include the object storage bucket: every object as a plain
  file in `storage.tar.gz` (content type kept as an extended attribute),
  restorable with or without emptying the bucket first. Scheduled backups and
  the MCP `create_backup` tool include it automatically.

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

[Unreleased]: https://github.com/envoryx/envoryx/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/envoryx/envoryx/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/envoryx/envoryx/releases/tag/v0.1.0
