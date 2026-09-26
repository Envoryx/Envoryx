# Changelog

All notable changes to Envoryx are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (0.x: minor versions may change
behaviour, patch versions are fixes only).

Release images: `ghcr.io/envoryx/envoryx:<version>` and `:latest` (newest
release). `:main` follows the development branch.

## [Unreleased]

### Added
- Rules for a project's host names, applied by the proxy (*Domains → Rules*,
  `PUT /projects/{id}/proxy-rules`): an address allowlist, HTTP basic
  authentication, redirects (paths and prefixes, to paths or other hosts),
  response headers to set or remove, and CORS with preflight answers. They
  also apply to a share, which now goes through the proxy – a password
  protects the public address.
- Share a project on a temporary public https address: a Cloudflare quick
  tunnel (`*.trycloudflare.com`, no account, no port forwarding) started from
  the project header, `envoryx project share` or the API, for 5 minutes to 24
  hours. It ends when its time is up, when the project stops, or by hand.
- Templates for Drupal (with Drush), TYPO3, Shopware and Craft CMS. Each is
  created with `composer create-project` and wired to the project database:
  Drupal's `settings.php`, TYPO3's `config/system/additional.php`, Craft's
  `.env` and Shopware's `APP_URL` read the variables Envoryx injects. Their
  installers need the running database, so they are actions: *drush
  site:install*, *typo3 setup* and *craft install* create the administrator and
  print a generated password, *system:install* sets Shopware up (admin /
  shopware). The web installers of Drupal, TYPO3 and Craft work too.
- `ENVORYX_URL`: the address a project answers at, injected into its
  containers and recomputed with every plan (after a rename, too) – for
  `APP_URL=${ENVORYX_URL}` in a `.env`.

### Fixed
- PostgreSQL no longer logs `FATAL: role "root" does not exist` every ten
  seconds: the health check logs in as the project's database user, and `psql`
  in the container's terminal now does too.
- A project with PHP and a PostgreSQL database got no `pdo_pgsql` extension,
  so the Symfony template (PostgreSQL by default) and every PostgreSQL
  application found no driver. It is now switched on with the database,
  also when one is added later.
- Deleting a project with its files failed when an application had made a
  directory read-only (Drupal locks `web/sites/default`) and Envoryx does not run
  as root.
- Template steps run with the project's `php.ini`, so the extensions an
  application's `composer.json` asks for (`gd`, `intl` …) are available during
  `composer create-project`.

## [0.9.0] – 2026-09-26

### Added
- `.env` import and export for a project's variables (*Environment* tab and the
  wizard): *Import .env* reads a pasted or chosen file (`export`, comments,
  quotes, `${VAR}` references), selects new and changed variables, leaves out
  the ones Envoryx sets for the project's services (so `DB_HOST=127.0.0.1` of
  the old setup does not win over the project database), refuses reserved
  names and multi-line values, and marks secrets by name or by a password in a
  URL. *Export .env* downloads the variables as a file.
- Import an existing website: *New project → Start from: Existing website*
  takes a ZIP or tar.gz of a site's files and optionally a database dump
  (`.sql`, `.sql.gz`). Envoryx recognises WordPress, Laravel, Symfony, Drupal,
  TYPO3, Joomla, Shopware, Craft CMS and plain PHP, static, Node.js and Python
  sites and fills the wizard with PHP version, extensions, document root, web
  server (Apache when the site relies on `.htaccess`) and database. Optionally
  the site's configuration is wired to the project database – `wp-config.php`,
  Drupal's `settings.php`, TYPO3's additional configuration, Joomla's
  `configuration.php`; each original is kept as `*.envoryx-original.php` that
  answers 404 – and the old server's configuration caches are removed. The dump
  is imported without the statements that tie it to the old server (`USE`,
  `CREATE DATABASE`, owners and grants); a failed import rolls the project back.
  `envoryx import <folder|archive> [name] --db dump.sql` does the same from the
  command line and packs a local folder on the fly. See DEPLOYMENT.md,
  *Importing an existing website*.
- Several databases per project. Next to the primary database (host
  `database`, `DB_*`) a project can have any number of additional ones with a
  name of their own – for example PostgreSQL `analytics` next to MariaDB: its
  own container (`envoryx-<project>-db-analytics`), volume and credentials,
  reached as host `analytics`, injecting `ANALYTICS_DB_*` and
  `ANALYTICS_DATABASE_URL`. The wizard adds them under *Additional databases*,
  the Database tab switches between them and adds or removes one; version,
  published port, password rotation, Adminer, snapshots (per database) and
  cloning (from any database of the same engine, of another project or the same
  one) work for each. Backups dump every database (`database-<name>.sql.gz`
  next to `database.sql.gz`), duplicating and renaming carry them along.
  `envoryx.yml` lists them under `databases:`; the CLI has
  `project create --add-database NAME=TYPE[:VERSION]` and `--db NAME` on every
  `db` command; the API takes `?db=<name>` on the database routes, lists them
  at `GET /projects/{id}/databases` and changes them with `PATCH` and
  `"databases"`; MCP tools take `db` and `additionalDatabases`.
- Shared package cache: Composer, npm, Yarn, pip and uv keep their downloads in
  `/config/cache`, which the application containers, the workers and the
  template scaffolds of every project share, so a package is downloaded once –
  a second Laravel project is created in about a quarter of the time.
  *Settings → Tools → Package cache* shows its size per tool and empties it.
  Instance backups leave it out.
- Test runner (*Tests* tab): Envoryx finds a project's test suites – Pest,
  PHPUnit (also Symfony's `bin/phpunit`), the `test`/`test:*` scripts of
  `package.json`, Playwright, Cypress, pytest and Django – and runs them in the
  runtime container with live output and an optional filter. Where the runner
  writes a JUnit report, the result lists every failed test with its message,
  file and line; the last 50 runs of a project are kept.

### Fixed
- Renaming a project whose database is PostgreSQL failed with "session user
  cannot be renamed": Envoryx logged in as the very login it renamed, the only
  superuser there is. The rename now runs through a short-lived helper login
  that is removed right afterwards.

## [0.8.0] – 2026-09-25

### Added
- Application health checks (*Overview* tab): a path such as `/health` must
  answer with the expected status. Envoryx asks the web server the way the
  proxy does (project network, the project's host name), every 30 s by
  default; after 3 failures in a row the application counts as down, the
  project shows a warning and a notification goes out (new event
  `project.down`, on by default), and another one when it answers again.
  Checks pause while a project is stopped, busy or its application container
  is not running. *Test* tries a check before saving it. Part of
  `envoryx.yml` (`healthcheck:`) and `envoryx project show`.
- New notification events (`project.oom`, `project.down`) are on after the
  update also where the event selection was saved before: an event type the
  selection did not offer yet follows its default until it is saved again.
- Resource history: every running project container is sampled once a minute
  (CPU, memory, network, disk I/O) and each project's disk space – volumes,
  project directory, backups – once an hour. The new *Resources* tab of a
  project charts it from one hour to one year (with a table view per chart),
  and the dashboard lists every project's average and peak usage, busiest
  first. Values are kept in full for a day, as 5-minute averages for a week
  and hourly after that; *Settings → General → Resource history* sets the
  retention (default 90 days, up to a year) and deletes the history.
- Resource limits per project (*Resources* tab): CPU cores and memory for the
  application containers (web server, PHP, Node.js, Python, workers) and,
  separately, for the services (database, caches, search, storage), each per
  container, plus a process limit that every container now has (default 4096)
  against fork bombs and runaway worker pools. Changes reach running
  containers at once; only lifting a limit recreates a container. The card
  shows each container's CPU and memory against its limit. When a container
  runs out of memory – also when only a child process is killed and the
  container keeps running – the project shows a warning for a day and a
  notification goes out (new event `project.oom`, on by default). Limits are part of `envoryx.yml`
  (`limits:`) and of `envoryx project show`.

## [0.7.1] – 2026-09-24

### Fixed
- Recreating a running container – after a version or port change, a rename –
  killed it outright. A database, Redis or RabbitMQ lost what it had not
  written yet: Redis everything since its last snapshot, MongoDB the latest
  writes. Envoryx now stops the container first, as `docker stop` would, and
  gives it its stop timeout to shut down cleanly.
- Database commands right after a database's very first start could fail with
  "terminating connection due to administrator command". The images
  initialise through a temporary server that listens on the unix socket only
  and is shut down right afterwards; Envoryx's clients used that socket.
  `psql`, `pg_dump`, `mysql`, `mysqldump`, `mariadb` and `mariadb-dump` now
  connect over TCP to 127.0.0.1, which only the real server answers (MongoDB
  did already).

## [0.7.0] – 2026-09-24

### Added
- Project manifest `envoryx.yml`: runtimes (with PHP extensions and ini
  settings), web server, database, services, domains, environment (secrets by
  name only), workers and cron jobs as a file in the repository. `envoryx up`
  in a clone creates the project from it – the server clones the repository's
  origin at the checked-out branch – or brings an existing project in line;
  `--dry-run` shows the changes, removals need `--prune`, missing secret values
  are asked for. `envoryx project manifest <project>` writes the file for an
  existing project. The *Git* tab shows the project as `envoryx.yml`, saves it
  into the project directory and applies a changed file after a pull; the
  wizard applies the manifest of a repository it clones. API:
  `…/manifest`, `…/manifest/file`, `…/manifest/plan`, `…/manifest/apply` and
  `POST /projects/from-manifest`.
- Log history in the Logs tab: pick a time range (last 15 minutes to 30 days,
  everything, or from/to), search and filter to warnings or errors on the
  server, see the error frequency as a chart (click a bar to zoom into that
  slot) and the most frequent errors and warnings grouped with numbers, ids and
  times masked. *Download* now saves every matching line instead of the last
  10,000. Envoryx guesses the level from the text (PHP, nginx, Caddy,
  PostgreSQL, MySQL, Python tracebacks, JSON and logfmt `level` fields, 5xx in
  access logs) and colours live lines by it. The API takes `since`, `until`,
  `q`, `level` and `stream` on `…/logs` and the live stream, plus
  `…/logs/stats` and `…/logs/download`; the CLI gets `envoryx project logs
  --since/--until/--grep/--level` and `-o FILE`, MCP `get_logs` the same
  filters and a new `get_log_stats` tool.
- Persistent log history: Envoryx copies the output of every project
  container into daily files under `/config/logs`, so the History view still
  finds it after a container was restarted or recreated (image or version
  change, rename, new password …) and when Docker's 30 MB per container are
  long rotated away. Output written while Envoryx was down is picked up
  afterwards, nothing is stored twice. Finished days are gzipped; retention (7
  days) and a size limit for all projects (1 GB) are set in *Settings →
  General → Log history*, which also shows the space used and can delete the
  history. Deleting a project deletes its history; instance backups leave it
  out.
- Offsite backups: *Settings → Backups* takes targets – S3-compatible storage
  (AWS, Backblaze B2, Wasabi, Hetzner Object Storage, Cloudflare R2, MinIO),
  SFTP (Hetzner Storage Box, NAS; the server key is pinned) or WebDAV
  (Nextcloud, ownCloud) – with a connection test. Scheduled project backups go
  up by themselves, a daily instance backup at a chosen hour too, everything
  else with *Copy offsite* or *Also copy offsite*; each target keeps its own
  number of scheduled copies. Archives are optionally encrypted with age
  (scrypt passphrase) before they leave the host. Failed uploads are retried
  with growing pauses and reported through the *Backup failed* notification.
  The Backups tab shows each copy's state and lists what a target holds for the
  project, deleted backups included; *Fetch* brings one back. A fresh Envoryx
  fetches its instance backup from the target the same way – disaster
  recovery in four steps (DEPLOYMENT.md → *Offsite backups*). CLI: `envoryx
  backup create --offsite`, `backup offsite`, `backup remote`, `backup fetch`.
- More DNS providers for the Let's Encrypt wildcard certificate: Hetzner
  (through the Hetzner Cloud API – the old DNS Console API was shut down in May
  2026), netcup, Amazon Route 53, DigitalOcean and Porkbun, next to
  Cloudflare. The settings ask for each provider's own credentials (netcup:
  customer number, API key and password; Route 53: access key and secret,
  optionally the hosted zone) and keep the secret ones out of every answer. A
  stored Cloudflare token is taken over as it is.

### Fixed
- `envoryx project create --database postgres` (as the help text suggests)
  was refused by the server, which only knows `postgresql`. The CLI now maps
  `postgres`, `pg` and `pgsql` to it, also in `--from-json`.
- Downloading a project backup left out the object storage archive
  (`storage.tar.gz`); the download now carries everything the backup holds.
- Starting a project whose directory was missing – on a fresh host after a
  recovery, or removed by hand – let Docker create it for the bind mount,
  owned by root, so the application could not write to it. Envoryx now
  creates it as the project user first.
- The certificate check waited for the challenge record at 1.1.1.1; asking
  before the record existed could get the "does not exist" cached for as long
  as the zone allows. Envoryx now asks the zone's own name servers and waits
  until all of them serve the record.

## [0.6.0] – 2026-09-24

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
  MCP `create_project` takes `pythonVersion`, `pythonServer`,
  `pythonPreset`, `pythonApp`, `pythonPort`, `pythonMode`. Backups skip
  `.venv` and `__pycache__` with the other dependency caches. The weekly
  runtime-version workflow and the image builds cover Python like PHP and
  Node. The debugpy port does not depend on the application server: a
  tooling container publishes it too, because what a developer steps
  through is usually a management command or a script started from the
  terminal, and debugpy attaches to whatever process you launch.
- A project says so when its virtual environment no longer matches its Python
  version. The `.venv` lives in the project directory and survives a container
  recreate, but it is built for one minor version – after a change from 3.13 to
  3.14 its packages sit in `lib/python3.13/site-packages`, where the new
  interpreter does not look, and the application server starts only to fail on
  its first import. site-packages cannot be moved across minors, so the project
  carries a warning naming both versions and the action that rebuilds it
  (`uv sync` for a uv project, else `pip install -r requirements.txt`). It
  disappears by itself once the environment is rebuilt.
- Cron jobs: any command on a schedule, not only the Laravel and Symfony worker
  presets. The new Cron tab builds the schedule (every few minutes, hourly, daily,
  weekly, monthly) or takes a cron expression and shows the next runs; templates
  start from the Laravel scheduler, a Symfony or Django command, an npm script or a
  script. Envoryx runs the command in the project's PHP, Python or Node.js container
  as the project owner, through `sh -c` and bounded by a timeout (default 10
  minutes), only while the project runs and never twice at once. "Run now", the last
  20 runs with their output, and a `cron.failed` notification. Schedules use
  Envoryx's time zone (`TZ`). Duplicating a project with its workers copies the jobs.
- RabbitMQ as an optional service next to Redis and Mailpit (4.3, or 4.2), in the
  wizard, on the Services tab, in `envoryx project create --rabbitmq` and the MCP
  `create_project` tool. The broker keeps its data in a volume, the management UI is
  published on a port of its own, the AMQP port on request. Envoryx generates a login
  and injects `RABBITMQ_HOST`, `RABBITMQ_PORT`, `RABBITMQ_USER`, `RABBITMQ_PASSWORD`,
  `RABBITMQ_VHOST` and `RABBITMQ_URL` (`amqp://…@rabbitmq:5672/%2f`) – the names
  laravel-queue-rabbitmq reads, and a URL for Symfony Messenger, php-amqplib, amqplib
  and Celery. `MESSENGER_TRANSPORT_DSN` stays yours to set – in Symfony's `.env`,
  `MESSENGER_TRANSPORT_DSN=${RABBITMQ_URL}/messages` – because injecting it would
  quietly move a Doctrine transport to AMQP. The password is shown on the Services tab
  on request (operate scope, like database credentials); the IDE tab lists the
  connection for desktop clients next to the database and Mailpit.
- Memcached as an optional cache next to Redis – in the wizard, on the Services tab, in
  `envoryx project create --memcached` and the MCP `create_project` tool. It keeps
  everything in memory (no volume, so removing it needs no confirmation), publishes
  its port on the host on request and injects `MEMCACHED_HOST`, `MEMCACHED_PORT` (what
  Laravel's memcached store reads) and `MEMCACHED_URL` (`memcached://memcached:11211`
  for Symfony's MemcachedAdapter).
- The PHP images carry the `redis`, `memcached` and `amqp` extensions, switched off like
  the others. Laravel talks to Redis through phpredis unless told otherwise, and
  Symfony's AMQP transport and MemcachedAdapter need their extension, so Redis,
  Memcached and RabbitMQ were only half usable from PHP. The wizard switches the
  extension on together with the service, adding a service on the Services tab does
  the same, and a service whose extension is off offers to switch it on. The images
  are rebuilt when this reaches `main`; until a project runs the new image, PHP logs
  that it cannot load the extension.
- Meilisearch and Typesense as optional search engines – in the wizard, on the Services
  tab, in `envoryx project create --meilisearch`/`--typesense`, the MCP
  `create_project` tool and the logs endpoints. Both keep their index in a volume and
  run with a generated key that only leaves the backend through the operate-scoped
  `/meilisearch/credentials` and `/typesense/credentials` endpoints ("Show master
  key"/"Show API key" on the Services tab). Meilisearch 1.54 always publishes its port,
  where its web dashboard lives; Typesense 30.2 publishes on request. The application
  gets the names Laravel Scout reads (`MEILISEARCH_HOST`/`MEILISEARCH_KEY`,
  `TYPESENSE_HOST`/`TYPESENSE_PORT`/`TYPESENSE_PROTOCOL`/`TYPESENSE_API_KEY`) plus
  `MEILISEARCH_URL`/`MEILISEARCH_API_KEY` (Symfony's meilisearch-bundle) and
  `TYPESENSE_URL`. `SCOUT_DRIVER` is left to the application, so a project indexing
  with another driver does not silently switch. The IDE tab lists both connections.
- OpenSearch as an optional Elasticsearch-compatible search engine (3.8, or 2.19) – in
  the wizard, on the Services and IDE tabs, in `envoryx project create --opensearch`,
  the MCP `create_project` tool and the logs endpoints. It runs as a single development
  node with its indices in a volume, over plain HTTP without login (security plugin
  off) and with a 512 MB heap (about 1 GB of RAM); the port is published on request.
  The application gets `OPENSEARCH_HOST`, `OPENSEARCH_PORT`, `OPENSEARCH_SCHEME` and
  `OPENSEARCH_URL` (`http://opensearch:9200`). `ELASTICSEARCH_*` is left to the
  application: current Elasticsearch clients refuse to talk to OpenSearch.
- OpenSearch Dashboards as an option of OpenSearch – a checkbox in the wizard and on the
  OpenSearch card of the Services tab, `envoryx project create --opensearch-dashboards`
  and `opensearchDashboards` in the MCP `create_project` tool. The web UI (Dev Tools
  console, index management, Discover) gets a host port of its own and a link on the
  Services and IDE tabs; it always runs OpenSearch's version and goes when OpenSearch
  goes. The image is about 2.6 GB, the container needs roughly 400 MB of RAM.
- Command line. The Envoryx binary is now its own client: `envoryx project
  list/show/create/start/stop/restart/delete/logs/exec/run`, `envoryx backup
  list/create/restore/download/delete`, `envoryx git status/pull/checkout` and
  `envoryx login/logout/whoami`. Everything goes through the REST API with an
  API token, so a token's scope and project restriction apply exactly as they
  do in the web interface and for MCP, and every command lands in the audit
  log under the token's name – the CLI has no database handle and no Docker
  socket of its own. On the host the container's binary is enough (`docker
  exec -it envoryx envoryx project list`): inside the container the address is
  known and only the token is missing. Elsewhere `envoryx login --url …`
  checks the token before storing it in `~/.config/envoryx/cli.json` (mode
  0600), and `ENVORYX_URL`/`ENVORYX_TOKEN` work without a file at all, which
  is what CI wants. A project is named by its name, its slug or its id;
  `--json` hands the API's own answer to `jq`; `--ca-cert` trusts the local
  CA on a workstation. Commands exit 0 on success, 1 on failure and 2 on a
  usage error.
- `envoryx project exec <project> -- <command>` runs a command in a project
  container and hands its exit code to the calling shell, so
  `envoryx project exec shop -- php artisan migrate --force || rollback` does
  what it reads like. stdout and stderr stay apart, stdin is piped in (up to
  512 KiB), and the command runs as the project owner in the project
  directory – where the browser terminal also starts. It is backed by a new
  endpoint, `POST /api/v1/projects/{id}/services/{kind}/exec`, which runs
  without a pseudo-terminal and answers with newline-delimited JSON frames
  (`stdout`, `stderr`, then `exit`); nothing is written before the first
  frame, so a container that is not running is still an ordinary HTTP error.
  The terminal WebSocket stays what it is – a PTY for humans, where the
  streams are merged and the exit code is lost; scripts need the opposite,
  and a big pipe or an interactive shell still belongs in SSH.
- Duplicate a project. *Duplicate* on the project page copies an existing
  project into a new one – `shop` → `shop-test` – with its configuration:
  runtimes and their settings, web server, services, environment variables,
  workers and the repository binding. The parts that hold data are checkboxes
  and default to on: the project directory (without `vendor/`,
  `node_modules/` and the other regenerable directories unless asked), the
  contents of the database and the objects of the bucket. Extra domains and
  the backup schedule are never copied – host names are unique, and a copy
  made to try something out should not inherit the original's scheduled
  backups.
  The copy is its own project in every way that has to be: new id, slug,
  directory, network, volumes, containers, and a fresh host port wherever the
  original published one. What it keeps are the generated credentials –
  database name, user and passwords, the bucket and its keys – because each
  project has its own server, network and volume anyway, while a `.env` that
  lives in the project files would otherwise point into the void, and the dump
  restores one to one (a renamed database would need `--nsFrom/--nsTo` for
  MongoDB and rewritten grants elsewhere).
  Nothing goes through a temporary file: the files are copied straight across,
  the dump of the original is piped into the client of the copy, and the
  bucket is read object by object. A database or storage container that is
  not running is started for the transfer and stopped again afterwards, so a
  stopped project can be copied as it is, and a copy is not started unless
  that was asked for. Any failure rolls the copy back completely, including
  the directory it created. `POST /api/v1/projects/{id}/duplicate` is the
  endpoint (admin scope; a token confined to particular projects may not
  create new ones), `envoryx project duplicate shop "Shop Test"
  [--no-files|--no-database|…]` the command, `duplicate_project` the MCP tool.
- Rename a project. Until now the identifier a project was created with was final: the
  displayed name could be edited, but `shop.test`, `envoryx-shop-php` and the database
  `shop` stayed whatever they were. *Rename* on the project page (and `envoryx project
  rename shop "Acme Blog" --yes`, and `rename_project` over MCP) now moves the lot:
  identifier and display name, the URL and the `-dev`/`-s3` host names, container,
  network and volume names, the SSH users the IDE connects with, the project directory,
  the backup directory and the rollback image tags. The database, its login and the
  object storage bucket travel too unless *Keep the database and bucket names* says
  otherwise – handy when a committed `.env` or an external client has the old name
  written into it.
  Docker can rename none of these, so the containers and the network are recreated from
  the new plan, the volumes are copied into their new names with a throw-away container
  from the project's own web image (nothing is pulled) and the old ones removed. The
  database moves the way it has to: PostgreSQL renames database and role in place, the
  others create the new database and stream the dump of the old one into it before
  dropping it, and MongoDB maps the namespace on the way. The bucket's objects are
  copied into the new bucket and the old one is dropped.
  The order is chosen so that a failure costs as little as possible: everything that can
  be checked is checked before the first container stops, the project record is renamed
  before any data moves and put back when the move fails, and old data is only dropped
  once the new copy is complete. A project that was running is running again at the end,
  and the current identifier has to be typed out to start any of it.
- Database snapshots and cloning – the two things a day of development keeps asking for:
  the dump you take before a migration, and the data of another project in your own.
  *Snapshots* on the project's Database tab dumps the primary database and nothing else,
  with a note like "before the orders migration", and puts it back with one click. The
  project does not have to be running for either: a stopped database container is started
  for the dump or the import and stopped again afterwards. Snapshots are ordinary backups
  under `/config/backups/<slug>/` – they show up in the Backups tab, can be downloaded and
  restore through the same verified path – so the dump a database version upgrade insists
  on appears among them too, ready to be put back. They roll: the ten newest of a project
  are kept, so taking one before every migration does not fill the disk, and scheduled
  backups and anything made by hand are never touched by that.
  *Clone from another project* replaces this project's database contents with another's –
  staging into local – as long as both run the same engine. The dump is piped straight
  from one container's client into the other's, so nothing is written to disk in between
  and a project of a few hundred megabytes is done in seconds. The source is only read,
  both projects are locked for the duration, and the target is snapshotted first unless
  that is switched off, so there is a way back. Overwriting a database asks for the
  project's identifier to be typed out, as restoring does.
  On the command line: `envoryx db snapshot shop --note "before the migration"`,
  `envoryx db snapshots shop`, `envoryx db restore shop <id> --yes` and
  `envoryx db clone local --from staging --yes` (`--no-snapshot` skips the safety net).
  Over MCP an assistant can take and list snapshots (`create_snapshot`, `list_snapshots`);
  putting one back and cloning over a database stay out, like restoring a backup.
- SSH remote forwarding (`ssh -R`) into project containers. A process in the container
  reaches the client through a port on the container's localhost: socat listens there
  and connects each connection to Envoryx on the project network, which accepts only the
  container's address and hands the connection to the client. PyCharm's SSH interpreter
  needs this to run anything. The listener ends with the forward or the SSH connection.
- The IDE tab explains how to open a project in PhpStorm, WebStorm & co. over SFTP.
  The embedded SSH server has served SFTP all along, but nothing said so, and the
  obvious route was the network share. A new card lists the deployment settings
  (host, port, user, root path `/var/www/html`, web server URL) and walks through
  PhpStorm's *New Project from Existing Files* wizard, checked step by step against
  PhpStorm 2026.2: a local copy that uploads on save, and how to fetch what
  `composer install` or `npm install` changed in the container. The host key is now
  also shown as MD5, the form PhpStorm asks you to confirm – the SHA256 value alone
  could not be compared.
- Project containers resolve the project domains. `http://shop.test` used to fail
  inside a container unless the Docker host itself asked a DNS server with the
  wildcard entry, and with Envoryx on its own `br0` IP the address was unreachable
  from there anyway. Envoryx now carries every host name the proxy serves as a
  network alias on each project network, so Docker's DNS answers them with Envoryx's
  address on that network. An application can call its own URL, and projects reach
  each other. Names added meanwhile arrive when a project next starts.
- DNS setup guide under *Settings → Domains & HTTPS*. A single wildcard entry
  sends every project name to Envoryx. The card shows that entry for AdGuard Home,
  Pi-hole, dnsmasq/OpenWrt and Unbound (pfSense, OPNsense), with Envoryx's address
  already filled in and a copy button. For routers without wildcard records, such as
  a FritzBox, it shows the hosts-file line with every current project name.
  DEPLOYMENT.md no longer sends Pi-hole users to *Local DNS records*, which cannot
  hold a wildcard.
- Tidier Unraid Docker tab. Envoryx's containers now carry the Envoryx icon
  (`net.unraid.docker.icon`) instead of the question mark, and can go into a
  [FolderView3](https://github.com/kennymc-c/folder.view3) folder automatically: enter
  the folder name under *Settings → General → Unraid Docker page* and every project
  container, and the database browser, gets the label `folder.view3=<name>`. Create
  the folder in FolderView3 with the same name. Labels are set when a container is
  created, so projects move into the folder the next time they start. Without the
  setting nothing is recreated. The icon appears whenever a container is next created
  anyway, for example after a runtime update. A FolderView3 folder with the regex
  `^envoryx-` works too, with no setting at all.

### Fixed
- PyCharm's SSH interpreter could not be set up. It uploads the project to
  `/tmp/pycharm_project_*` before its sync folder can even be changed, and SFTP only knew
  the bind mounts; its probes also ran `$SHELL -l -c …` with an empty `$SHELL`. Outside
  the mounts SFTP now behaves like a server on the container (stat, list, read, write,
  mkdir, rename, delete, chmod run there as the project user, so a client gets what a
  shell over the same access gets), failures carry the codes a real server sends, and SSH
  sessions get `SHELL=/bin/sh`. The IDE tab describes PyCharm's wizard (sync folder
  `/var/www/html`) and WebStorm's "JavaScript Runtime" page as they are in 2026.2.
- SSH logins from an IDE failed with "Invalid credentials" (JetBrains Gateway) or a broken
  connection when the password took more than 30 seconds to type. The IDE connects first and
  asks for the host key and the password afterwards, and Envoryx allowed only 30 seconds for
  the whole login. It now allows two minutes, the default of OpenSSH's `LoginGraceTime`.
- The IDE tab's interpreter hints for PyCharm and WebStorm named menus that 2026.2 no
  longer has: PyCharm's SSH interpreter sits under Settings → Python → Interpreter and
  needs PyCharm Pro, WebStorm calls the field "Node runtime". Projects without a `.venv`
  get `/usr/local/bin/python` as the interpreter path next to the `.venv` one.
- PhpStorm's remote interpreter over the embedded SSH server reported "PHP version:
  Not installed". PhpStorm checks over SFTP that the interpreter exists, and SFTP
  only knew the bind mounts, so `/usr/local/bin/php` was missing; it then uploads its
  helpers to `~/.phpstorm_helpers`, and SFTP started in the unwritable `/`. SFTP now
  answers stat requests outside the mounts from the running container (metadata
  only; reading and writing stay confined to the mounts) and starts in
  `/home/envoryx`. SFTP errors no longer reveal where a file lives on the Envoryx
  side. The IDE tab now mentions the "+" in the interpreter dialog and the path
  mapping a remote interpreter needs.
- "I have no name!" in the terminal of a PHP container, and git over SSH failing there
  with "No user exists for uid 99". The PHP container runs as root and php-fpm switches
  users itself, but terminal, actions, `envoryx exec` and SSH work in it as PUID:PGID.
  That uid only got a passwd entry in containers that run as it, and a newly created
  project got none in any container until its second start. The entry is now made on
  the first start, for the uid Envoryx works as, and made again before a terminal,
  action or command runs, so existing containers are healed without a restart.
- Templates and git on Unraid. The one-shot containers that scaffold a template or run
  git ran as PUID/PGID (99:100 on Unraid) without a passwd entry for that uid, so the
  Next.js template died in create-next-app ("template next failed at create-next-app:
  Node.js v24…") and git over SSH failed with "No user exists for uid 99". They now start
  as root, add the entry and drop to PUID/PGID before the command runs – the same entry
  the long-running containers already got.
- A failed template now says why. The error showed the last line of the output, which
  for a crashing Node process is just "Node.js v24.x" and for npm the path of its log
  file. It now shows the thrown error or npm's cause, and the last 40 lines of the
  output go to the Envoryx log.
- MongoDB is offered as 8.2 and that is what a new project gets. The previous
  default 8.0 – and 7.0 – refuse to start on Linux 6.19 and newer ("MongoDB
  cannot start: Linux kernel versions 6.19 and newer has a known
  incompatibility with this version", SERVER-121912), which is every current
  desktop and server kernel: the container went into a restart loop and the
  project never came up. Both older series stay selectable for hosts that run
  them; an existing project keeps its version, as a MongoDB major cannot be
  upgraded in place anyway.
- An application server (Python, Node dev server) raced the database on every
  start: it came up while the database was still initialising, and anything
  that connects at boot died on the first try. The restart policy hid that for
  most servers, but Django's `runserver` does not exit – its autoreload parent
  survives the failed child, so the container stayed *running* and answered
  nothing until it was restarted by hand. The server now waits for the
  database port (socat, two-second retries, visible in the container log)
  before it starts, on a host reboot too. A project without a database keeps
  the exact command it had, so nothing is recreated for it.
- A new project with the default PostgreSQL 18 came up with a database
  container in a restart loop: the data volume was mounted at
  `/var/lib/postgresql/data`, and the 18 image – which keeps its cluster in
  `/var/lib/postgresql/<major>/docker` – refuses to start when it finds a
  volume on the old path, even an empty one. From 18 on the volume takes
  `/var/lib/postgresql` (the layout `pg_upgrade --link` expects); 16 and 17
  keep the data directory itself, so existing volumes stay where they are.
  A major upgrade was already refused for PostgreSQL, so no data moves.

- A rollback target that had left the host (`docker rmi`, a prune on an
  installation that predates the rollback tags) made every reconcile – once
  every 30 seconds – retry the tag and log *rollback image not protected*.
  The image cannot come back, so the history now forgets it on the first
  miss (one info line), and the project stops offering a rollback that
  could only fail.
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

[Unreleased]: https://github.com/envoryx/envoryx/compare/v0.9.0...HEAD
[0.9.0]: https://github.com/envoryx/envoryx/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/envoryx/envoryx/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/envoryx/envoryx/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/envoryx/envoryx/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/envoryx/envoryx/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/envoryx/envoryx/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/envoryx/envoryx/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/envoryx/envoryx/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/envoryx/envoryx/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/envoryx/envoryx/releases/tag/v0.1.0
