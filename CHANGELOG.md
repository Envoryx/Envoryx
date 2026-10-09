# Changelog

All notable changes to Envoryx are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (0.x: minor versions may change
behaviour, patch versions are fixes only).

Release images: `ghcr.io/envoryx/envoryx:<version>` and `:latest` (newest
release). `:main` follows the development branch.

## [Unreleased]

### Fixed
- Saving project settings stopped and started every container of a running
  project, the database and OpenSearch included, even when only Xdebug or
  another PHP setting changed. Envoryx now recreates just the containers
  the change concerns: a PHP setting restarts the PHP container, the PHP
  workers and the web server, the database and the other services keep
  running. PHP projects
  ask for a restart once after the update, and a changed Xdebug host in the
  settings now does the same.

## [0.21.0] - 2026-10-08

A **security fix** (see *Security*): PHP files that got into an upload
directory (WordPress' `wp-content/uploads`, Drupal's `sites/*/files` and
the like) could be run in the browser; restart your PHP projects after the
update to apply the new web server rules. The PHP images now ship
**Node.js**, so Shopware's administration and storefront build in the PHP
container. A restore empties the project's Redis, new Laravel projects run
their migrations, and Envoryx no longer hands out host ports that a program
on the host already uses. Crash-looping workers show as restarting and can
be restarted on their own.

### Added
- Restoring a backup empties the project's Redis afterwards: sessions,
  object and page caches and queued jobs referred to the data from before
  the restore (WordPress' object cache, Shopware's HTTP cache). The
  restore dialog has a box for it, ticked by default; a stopped project's
  Redis is started for it and stopped again. An external Redis is left
  alone. API: `flushRedis`, default true.
- The PHP images ship Node.js 24 LTS with npm and yarn (pnpm through
  Corepack on first use). Shopware's `bin/build-administration.sh` and
  `bin/build-storefront.sh`, which call php and npm in turn, run in the PHP
  container now, and so do the npm builds of Laravel's Vite, Drupal and
  TYPO3 themes or WordPress blocks from the PHP terminal. Shopware projects
  get both build scripts as actions. A Node service still runs dev servers.
  The images grow by about 150 MB.
- Workers can be restarted on their own from the Workers section (a queue
  worker keeps the code it loaded at its start), also by developers. The
  rest of the project keeps running.

### Changed
- New Laravel projects run `artisan migrate` once they're up, as Rails
  projects run `db:prepare`: the sessions, cache and jobs tables are there
  before the first request, and the queue worker no longer restarts in a
  loop until someone ran the migration. A project created stopped still
  gets it from Actions.

### Fixed
- Envoryx handed out host ports that a program on the Docker host already
  listened on (a JetBrains IDE on 30000 and 30001, a database installed on
  the host), and the project's start failed in Docker. Envoryx now looks at
  the host's listening ports through a short-lived container in the host
  network and skips them. A stopped project whose HTTP port was taken
  since moves to a free one at its next start; any other port in use is
  named in the error instead of Docker's raw message.
- A worker or service whose process exited a few seconds after each start
  showed as running, since Docker lists it as running between its
  restarts. Containers Docker restarted three times or more, the last time
  within the last minute, now show as restarting, the project as partly
  running, and the worker says to look at the logs.

### Security
- PHP files in upload directories ran like application code: a script
  that got into `wp-content/uploads` or Drupal's `sites/default/files`
  through an upload form could be called in the browser. The generated
  Caddy, Nginx and Apache configs now refuse PHP below the upload and
  cache directories of WordPress (also Bedrock and subdirectory
  installs), Drupal, TYPO3, Shopware, Magento and Laravel and below
  `/uploads`, as the hardening guides of these CMSs recommend. Everything
  else there is served as before, Drupal's image styles included. After the
  update every running project with a web server reports an older setup
  for its web container once; restart it to apply the rules. From now on
  a changed web server config always asks for that restart.

## [0.20.0] - 2026-10-08

The rest of the fixes from the application test round. The templates work
behind the proxy out of the box (WordPress, Drupal, TYPO3, Rails, Django,
Flask, FastAPI, Next.js, Spring Boot, Quarkus), Shopware and Craft get worker
presets, and the **SSH lockout** no longer locks a whole office out after a
few offered keys. **SFTP and scp now start in the project directory**
instead of the tool home (JetBrains IDEs keep the home for their helpers).

### Added
- Worker presets for Shopware's message queue and scheduled tasks and for
  Craft's queue. The add form starts with the preset of the project's
  framework instead of Laravel's queue worker.
- Static sites get SFTP for their files; a shell explains why there is none.

### Changed
- SSH lockout: only guesses count (a wrong or unknown API token), not
  offered keys or empty passwords; failures expire after ten minutes; the
  lockout is per address and project, with a looser limit for the whole
  address. A locked-out client gets a banner saying why.
- SFTP and scp start in the project directory, like an ssh login.
- New WordPress, Drupal and TYPO3 projects and imported sites of these get
  configuration that follows the project: WordPress takes its address from
  the request (or the project URL) and finds the project's Redis, Drupal
  gets `trusted_host_patterns` and a `drush/drush.yml` with the project URL,
  TYPO3 trusts the proxy, has a sender address and mails to Mailpit.
- New Rails projects run `db:prepare` once they're up, use Solid Cable in
  development, mail to Mailpit, log to stdout and let web-console answer
  through the proxy. Django mails to Mailpit, Flask uses ProxyFix, Next.js
  lists the project's host names in `allowedDevOrigins`, Spring Boot creates
  its tables (`ddl-auto=update`), Quarkus builds the test database's tables,
  Gin trusts the proxy, and the Python templates write a `.gitignore`.
- uvicorn and gunicorn trust the proxy's forwarded headers
  (`FORWARDED_ALLOW_IPS=*`).
- The IDE section's debugpy examples start debugpy in the reloader's child
  process, so they work next to the running app server.
- Audit entries keep the target's name, so they read right after the target
  is gone, and every action has a label.
- Docker resources of another instance on the same host, or from before
  0.18, are counted apart from this instance's orphans and no longer show a
  warning.

### Fixed
- A duplicated WordPress sent wp-admin to the original's address.
- `drush uli` printed `http://default/...` links.
- `db:prepare` left the Rails queue database empty when the site had been
  opened first, and a `rails new` whose `bundle install` failed was reported
  as ready.
- Turbo broadcasts from Rails jobs never reached the browser in development.
- Next.js Fast Refresh never connected behind the proxy.
- FastAPI's redirects left HTTPS.
- The Composer script worker preset stopped after 300 seconds.
- scp copied the file but exited with 1.
- Viewers saw Start/Stop/Restart on the dashboard.
- A branch environment couldn't be given to a developer after it was created.
- The site import failed on `GRANT` and `CREATE USER` lines in MySQL dumps.
- Deleting an unhealthy project sent a "recovered" notice with its id.
- A new cron job skipped the minute the UI showed when it was saved.
- Restoring a backup failed on read-only files when Envoryx doesn't run as
  root.
- Updating the Node service without `inspect` unpublished the inspector.
- The IDE section showed `<projects share>` instead of the host path on bare
  metal.
- The Tests page grew wider than the window.
- Container logs showed raw terminal colour codes.

## [0.19.0] - 2026-10-07

More fixes from the application test round. New projects get the current
runtime images, the PHP images bring the tools CMSs expect, and a dev server
that can't start yet waits instead of crash-looping and locking you out of
the actions that would fix it. **The Node container no longer sets
`NODE_ENV=development`** for everything, only for the dev server, and the dev
server **installs from your lockfile** when `node_modules` is missing.

### Added
- The PHP images bring ImageMagick (TYPO3 processes images with it), the
  MariaDB and PostgreSQL clients (`drush sql:*`, `wp db`, `artisan db`),
  WP-CLI as `wp`, common UTF-8 locales and the `exif` extension. PHP 8.5
  grows from 1.07 to 1.24 GB.
- Every runtime image has rsync, so `rsync -e ssh` works.
- The Node dev server installs the dependencies of a fresh checkout before it
  starts: `npm ci`, `pnpm install --frozen-lockfile` or
  `yarn install --frozen-lockfile`, whichever lockfile the project has.
  Without a lockfile it waits for an install from Actions.

### Changed
- `NODE_ENV=development` is set only for the Node dev server, no longer for
  the whole container. Builds from Actions, the terminal and cron jobs run
  without it; workers and cron jobs follow the dev server's mode.
- The generated php.ini sets `max_input_vars = 5000` (PHP's 1000 silently
  drops fields of big backend forms) and sizes OPcache for large vendor
  directories.
- A project cloned from a repository takes the dev server script and preset
  from its package.json instead of guessing `dev` with Vite.

### Fixed
- New projects, runtime version switches and new services pulled no image,
  so they used whatever older build of the tag was on the host: PHP without
  msmtp (`mail()` failed), Ruby without libvips. They now get the current
  image; offline the local one is still used.
- Adding a worker or changing a limit recreated every container whose image
  had been updated meanwhile, which restarted the database and emptied
  Mailpit. Only start and restart apply image updates now.
- Switching a Python, Go, Ruby, Java or .NET server off and on gave it a new
  host port.
- A dev server that couldn't start crash-looped, and a crash-looping
  container refused every action, including the install that would fix it.
  The Node dev server now waits for package.json and node_modules, Python
  servers and workers wait while `.venv` was built for another Python
  version, and an action on a restarting container says what's going on.
  Rebuilding the venv lists the packages the old one had.
- `npm run build` from Actions failed for Next.js and shipped React's
  development bundle from Vite because of the container's `NODE_ENV`.

## [0.18.1] - 2026-10-07

Fixes from the application test round. The important one: **running PHPUnit
from the Tests section in a Laravel project emptied the development
database.** PHP projects behind the proxy now know they're on HTTPS, and
Node, Python, Go, Java and .NET get a PostgreSQL URL their drivers accept.
Restart running projects so their containers pick the changes up.

### Fixed
- Tests no longer run against the development database. The `DB_*`
  variables Envoryx injects beat the `<env>` entries of `phpunit.xml`, so
  Laravel's `RefreshDatabase` ran `migrate:fresh` on your data. PHPUnit and
  Pest now run against `<database>_test`, which Envoryx creates as the
  administrator before the run; Symfony, whose Doctrine recipe adds `_test`
  itself, ends at the same database instead of failing with error 1044 on
  MySQL and MariaDB. Django's runner and pytest-django get `test_<database>`
  created up front for the same reason. Java runs with a database URL that
  has parameters (`?useSSL=false`) used the development database too.
- PHP sees requests that came in over HTTPS as HTTPS. TLS ends at the
  proxy, and PHP-FPM saw plain http: Laravel's login form posted to http,
  Symfony's toolbar and redirects were blocked as mixed content, the
  Shopware installer and the TYPO3 backend redirected to http. The
  generated Caddy, Nginx and Apache configs now set `HTTPS=on`,
  `REQUEST_SCHEME=https` and the browser's port for requests the proxy
  marks as https, so no trusted-proxy setup is needed.
- Node, Python, Go, Java and .NET containers get PostgreSQL URLs as
  `postgresql://`, which SQLAlchemy, Prisma, pgx and lib/pq accept, instead
  of `pgsql://`. PHP keeps `pgsql://` for Laravel and Doctrine, and
  `DB_CONNECTION` stays `pgsql` everywhere.

## [0.18.0] - 2026-09-30

Five security fixes (see *Security*), the most serious in the database
browser, which let anyone who could operate one project open every
project's database. Several Envoryx instances can now share a Docker host
without deleting each other's containers. **New object storage buckets
are private** and removing a service **keeps its data** unless you choose
otherwise. Ruby projects report an older setup once after the update
(new database variables); restart them when it suits you.

### Added
- Go, Java and .NET projects warn when a project file needs a newer
  version than the service runs, as Python does for a stale `.venv`:
  the `go` line of `go.mod`, the Java level in `pom.xml` or
  `build.gradle(.kts)` (for example the Spring Boot template's Java 25
  on a project switched to Java 21), and the target framework of the
  project file or the SDK in `global.json`. Values Envoryx cannot read
  for sure (variables, parent POMs, `Directory.Build.props`, several
  target frameworks) give no warning.

### Changed
- Orphans without an instance label (created by an older Envoryx, maybe
  another instance's) are no longer removed automatically; the *Docker*
  page marks them *older Envoryx* and removes them on request. Orphans
  an instance labelled itself are still cleared after two passes.
- Django projects from the template serve their static files in
  production mode: the settings get `STATIC_ROOT` (`staticfiles/`) and
  WhiteNoise, so gunicorn no longer answers 404 for `/static/` and the
  "manage.py collectstatic" action works. Projects created before need
  `whitenoise` installed and those settings added by hand.
- The Workers & cron section shows the command a .NET "Project" worker
  really runs (`dotnet publish`, then `dotnet <dll>`) instead of
  `dotnet run --project`.
- **New object storage buckets are private.** `storage: {}` in the API,
  the *Add object storage* card, the MCP tool and `storage: true` in
  `envoryx.yml` used to create a bucket anyone could read through the
  project's S3 host name; now only presigned URLs and authenticated
  requests work until *Anyone may read objects* is switched on
  (`"publicRead": true`, `storage: {publicRead: true}`). Existing buckets
  keep their setting, and an `envoryx.yml` without `publicRead` leaves an
  existing bucket alone. An exported manifest now names a public bucket
  with `publicRead: true`.
- Removing Redis, RabbitMQ, Meilisearch, Typesense or OpenSearch keeps
  its data volume unless *Delete the data* is chosen in the dialog: adding
  the service again later reuses the data with the credentials it was
  created with. The card that adds the service shows kept data with
  *Delete kept data*; kept data is not backed up, moves along with a
  rename and is deleted with the project. In the API a removal without
  `removeData` now keeps the data instead of answering 422;
  `"removeData": true` still deletes it. `GET .../extras` lists kept
  volumes under `keptData`, `DELETE .../extras/{kind}/data` deletes one.
- The API refuses `"exposePort": false` for Mailpit and Meilisearch with
  a 422 instead of ignoring it: their port carries the inbox and the
  dashboard and is always published.
- The documentation of the database browser no longer claims that the
  database password never reaches the browser: the automatic login puts
  it into a hidden field of the Adminer login page. The page is only
  served to an Envoryx session with operate access.

### Security
- An admin API token could show and replace the secret key that decrypts
  every stored secret. Like user and token management, both now need an
  admin's browser session; tokens are refused, even with admin scope.
- Restoring an instance backup brought back the browser sessions the
  backup held: a cookie from back then worked again, and a logout or a
  disabled account since was undone for that session. The restore now
  ends every session. API tokens are the backup's and keep working; a
  token revoked after the backup was taken has to be revoked again.
- The dashboard showed users with access to only some projects the
  instance-wide count of orphaned resources, the reconcile issues of
  every project (with its name) and the resource usage of every project.
  It now lists issues and usage of the caller's projects only, and the
  orphan count only for instance admins.
- The database browser opened the database of any project for anyone who
  could operate a single one: its credentials file holds the logins of
  every project, root included, and `/dbtool/` did not check which
  database a request named. Envoryx now checks every request and opens
  only databases of projects the user or token may operate; other servers
  are refused. The Adminer container also logged in anyone who reached it
  directly, which the containers of every project whose database had been
  opened in it could do over the shared network; it now answers only
  requests that come through Envoryx. An existing container is replaced
  on the next reconcile or open.
- The project list, a project and the dashboard handed the service
  credentials to everyone who could see the project, viewers and read
  tokens included: the object storage access and secret key, the RabbitMQ
  password, the Meilisearch and Typesense keys and the password of an
  external Redis. Like database passwords, they now come only from the
  credentials endpoints, which need operate. A service kind Envoryx adds
  later shows no settings in these responses until they are known to be
  free of secrets.

### Fixed
- Several Envoryx instances on one Docker host (a test instance next to
  production, two instances on an Unraid server) no longer delete each
  other's containers and networks as orphans. Every instance now has an
  ID (`/config/instance-id`, created at the first start, kept out of
  instance backups like the secret key) and labels everything it creates
  with it (`envoryx.instance`); what another instance labelled is never
  listed, stopped, changed or removed, and a project name whose
  containers, network or volumes the other instance holds is refused
  instead of shared. Existing containers need no restart: unlabelled
  ones of known projects stay the instance's own and get the label when
  they are next recreated, and the label is not part of the container
  fingerprint. The network pool still steps around every network on the
  host.
- Only one Envoryx instance per Docker host could use the database
  browser: its container and network were both called `envoryx-dbtool`,
  and a second instance got "the name is taken by another Envoryx
  instance". They are now named after the instance ID
  (`envoryx-dbtool-` plus its first 8 characters). An existing
  `envoryx-dbtool` of the instance is removed at the start and the next
  **Open database** creates the new one; another instance's is left
  alone.
- The project overview and the Docker page listed each published port
  twice (":28001 → 3000" for IPv4 and for IPv6) when ports are published
  on all interfaces. Each port is listed once now.
- Turning the Node dev server off dropped its preset, script and port,
  and turning it back on through the API or CLI started a Vite server on
  a new host port. The settings and the host port are now kept while the
  dev server is off, and an update that leaves fields empty keeps the
  stored values.
- Changing a project's repository URL stored the new URL but left the
  checkout's `origin` on the old one, so pull kept using the old
  repository. Saving a new URL now moves `origin` along.
- Error messages of an external Redis or database no longer garble words
  that contain the password: a password like `WRONG` turned Redis's
  `WRONGPASS invalid username-password pair` into `***PASS ...`. The
  password is still hidden where it appears on its own, e.g. in
  `redis://:***@host`.
- `envoryx project list` and `project show` printed the project URL
  without the proxy's port (`https://shop.test` where the proxy listens
  on 18443). Projects in the API now carry their full `url`, and the CLI
  prints it.
- The CLI showed only "an internal error occurred" for server errors and
  dropped the cause the server sends along; it prints the cause now.
- The single sign-on client secret could not be removed: an empty field
  keeps the stored one, also when single sign-on is switched off. The
  settings card has *Remove the stored client secret when saving*, the
  API takes `"clearSecret": true`.
- The Symfony Messenger consumer no longer restarts in a loop until the
  `messenger_messages` table exists: it runs `messenger:setup-transports`
  for its transports first, which creates the table the recipe's
  `doctrine://default?auto_setup=0` leaves to the application, and waits
  with a hint while the database cannot be reached. Existing Messenger
  workers are recreated once on the next start.
- Imported Symfony sites no longer fail with `Invalid platform version`:
  with "Adapt the configuration" on, `config/packages/doctrine.yaml` reads
  `server_version` from `DB_SERVER_VERSION`, as in the Symfony template,
  unless it sets a version itself. The site's `.env` usually names it in
  `DATABASE_URL`, which the injected URL replaces without one.
- Ruby containers get `CACHE_DATABASE_URL` and `CABLE_DATABASE_URL`
  (`<database>_cache`, `<database>_cable`) next to `QUEUE_DATABASE_URL`, so
  the Solid Cache and Solid Cable entries of a Rails 8 production
  `database.yml` reach the project's database server. Envoryx creates those
  databases when `database.yml` has the entries, like the queue database.
  Ruby containers report an older setup once after the update; restart the
  project when it suits you.
- The Ruby images ship libvips, which Active Storage's default `:vips`
  processor needs for image variants (the images grow by about 180 MB).
- `rails test` runs in the Tests section show their counts and failed
  tests (name, message, file and line), read from the minitest output;
  before, only the exit code counted.
- A new cron job ran once right away, in the minute it was created,
  instead of first at its next scheduled time. Only jobs due in the
  minute Envoryx starts still run in that minute.
- A user whose global role is none could not see the schedule preview
  in a project's cron editor (403). The Database tab no longer asks
  viewers for the database browser's state, which they may not read.
- Asking for a token scope above your role answered "scope must be read,
  operate or admin: your role allows at most read tokens", as if the
  scope name were wrong. It now reads "your role allows at most read
  tokens".
- A read token confined to one project that called an operate tool over
  MCP (e.g. `stop_project`) was told "limited to particular projects"
  instead of "this token has read scope, the operation needs operate",
  as the REST API says. The same applied to the database browser routes.
- The Go debugging texts (IDE tab, Go server settings, deployment guide)
  said breakpoints survive every rebuild. When air rebuilds, it restarts
  Delve and the IDE's debug session ends; the texts now say to attach
  again, after which the breakpoints are hit as before.
- The diagnostics check "Wildcard DNS as seen by Envoryx" reported OK for
  any answer when no host for project links was set, even when the names
  pointed at another machine. It now asks that address for the probe
  host and only reports OK when this instance's proxy answers; another
  server there is a warning, no answer is reported as unverified.
- `PATCH /projects/{id}` with a service object that leaves out `enabled`
  (e.g. `{"memcached": {"exposePort": false}}`) removed the service; a
  missing `enabled` or `exposePort` now keeps what the service has. A
  version change in the Services section no longer sends the port along.
- The certificates the local CA issued for a project's host names stayed
  in `/config/ca/certs` after the project was deleted and went into every
  instance backup. Deleting a project now removes the certificates of
  host names nothing routes to any more, and the first start after the
  update clears those left behind by earlier deletions.

## [0.17.0] - 2026-09-30

Fixes from a full system test, four of them for security (see
*Security*). **After the update the dashboard reports every running
container once** as running with an older setup, because the container
fingerprint now includes the environment; restart each project when it
suits you. Ruby projects install their bundle once more on that restart,
and PHP projects pull the new PHP image. New project networks come from
`ENVORYX_NETWORK_POOL` (`10.213.0.0/16`); set another range if that one is
used in your network.

### Added
- Envoryx sets `DB_SERVER_VERSION` for the MariaDB, MySQL and PostgreSQL
  servers it runs, in the form Doctrine expects (`18`, `8.4.0`,
  `mariadb-11.4.0`); additional servers get it with their prefix.
- A MongoDB 8.0 database can move to 8.2 in place, which MongoDB
  supports; 8.2 takes over the data as it is (the feature compatibility
  version stays at 8.0). On Docker hosts where the old version cannot
  start, the backup taken first is a copy of the data volume instead of
  a dump; restore, download and offsite copies handle it.
- PHP's `mail()` reaches Mailpit: the PHP images ship msmtp, and while
  Mailpit is on, `sendmail_path` sends through it. Without Mailpit
  `mail()` returns false as before.

### Changed
- New project networks get a /24 out of `ENVORYX_NETWORK_POOL`
  (`10.213.0.0/16` unless set, 256 projects) instead of one of Docker's
  default ranges, which ran out after about 30 projects: every project
  keeps its network while stopped, and creating the next one then failed
  with HTTP 500. Existing networks keep their range. If the range is used
  in your LAN or VPN, set another one; `off` keeps Docker's choice, and
  its running out now says what to do.
- A container's environment variables now count when Envoryx decides
  whether it is up to date. Workers stayed in development after a switch
  to production mode (Ruby), and containers kept newer variables after an
  instance restore; a restart now recreates them. **After this update the
  dashboard reports every running container once** as running with an
  older setup; restart each project when it suits you.
- `GEM_HOME` in Ruby containers now names the Ruby version
  (`~/.gem/ruby/3.4.0`), the directory RubyGems uses for user installs.
  RubyMine's debugger gems are found without a `GEM_PATH` workaround, and
  gems built for one Ruby version no longer meet another. Existing Ruby
  projects install their bundle once more after the restart; the old
  contents of `~/.gem/ruby` in a project home can be deleted.
- Runtime and worker containers always resolve `host.docker.internal` to
  the Docker host, not only with an external service. Xdebug's default
  and PyCharm's debug server work when the IDE runs on the Docker host;
  the IDE section explains `ssh -R` for an IDE on another machine.
- The interface leaves out what your role may not do in a project:
  viewers see no start, restore or delete buttons, and forms they may
  only read are shown read-only. `/docker` and `/projects/new` send
  non-admins to the project list. Whether you are an admin now comes
  from the server, so a session with a limited API token shows the right
  controls.
- The Rails template gives Solid Queue its own database in development,
  as Rails does in production: `database.yml` gets a `queue` entry,
  Envoryx creates `<database>_queue` and sets `QUEUE_DATABASE_URL`, which
  also reaches the production `queue` entry. A Solid Queue worker whose
  tables are missing waits with a hint instead of restarting in a loop.
- PHP 8.6 (preview): extensions that don't build for it yet (redis,
  imagick, memcached, amqp, mongodb, Xdebug) are greyed out and refused
  instead of offered and silently missing. mbstring, which the upstream
  8.6 image no longer compiles in, is installed in every PHP image.
- MongoDB 8.0 and 7.0 cannot be chosen on Docker hosts with Linux 6.19 or
  newer, where they don't start. Existing projects with such a database
  get a hint (8.0: upgrade to 8.2; 7.0 has no way to 8.2 on such a host)
  and start without that database instead of restarting it in a loop.

### Security
- With JetBrains Gateway on, every project mounted the whole shared
  `~/.cache/JetBrains`. Besides the downloaded IDE backends it holds index
  caches with the source, local history and join links with their tokens,
  so one project could read another project's source and join links, in
  its container and over SFTP. Only `RemoteDev/dist` with the backends is
  shared now; the rest stays in each project home. The old shared data in
  `/config/jetbrains` is no longer mounted; everything except
  `RemoteDev/dist` there can be deleted.
- Adding an IP allowlist to a project that was shared publicly was
  accepted, but the tunnel reaches the proxy from Envoryx itself, so the
  share stayed public (and an allowlist naming 127.0.0.1 let the internet
  in). An allowlist is now refused while a share runs.

- The resource comparison (`GET /metrics/overview`, the dashboard's
  resource card) listed every project's name and usage to users who have
  a role in only some projects; it now shows only theirs.
- Viewers and read-only API tokens received the values of secret
  environment variables in the project list, the project view and the
  dashboard. Below operate these values are now left out.

### Fixed
- The diagnostics check "Project domains from this browser" could never
  pass: the page's own content security policy blocked the request, and
  the check then blamed DNS. The policy now lets that one probe host
  through.
- A failed create left the files of a template, clone or starter page in
  the project directory, and trying again with the same name failed with
  "templates need an empty directory". The rollback now removes a
  directory the create made and empties one it found empty; a directory
  that already held files is still never touched.
- MongoDB snapshots, project backups, clones and branch copies failed
  with HTTP 500: mongodump 100.17 and later refuse the connection string
  Envoryx passed.
- The periodic reconcile could set a project whose create had just
  finished to failed ("creating was interrupted by an Envoryx restart").
- Behind HTTPS, PHP projects on Caddy saw `X-Forwarded-Proto: http`, so
  TYPO3 answered 404 and Shopware "Sales Channel Not Found" after setup.
  Caddy also served dotfiles such as `.env` and `.htaccess` in PHP
  projects; like nginx and Apache it now answers 404.
- Next.js failed to build in production mode (it ran under
  `NODE_ENV=development`), and Nuxt's production mode started a `start`
  script its starter doesn't have; it now uses `preview`.
- The *pip install* action failed after a Python version change ("No
  module named pip"); it now rebuilds the stale `.venv`.
- An error from a failed start with one custom image stayed on the
  project after a later image started fine.
- Rider's "Attach to Remote Process" over SSH failed ("cancel-tcpip-forward
  failed"): a forward asked for port 0 couldn't be cancelled.
- The IDE section gave DataGrip a MongoDB URL with the user in it, so the
  password was ignored; the Node inspector example named the wrong script;
  the Gateway steps named WebStorm for every project without PHP; the Ruby
  text didn't say RubyMine's debugger needs *Allow JetBrains Gateway*.
- `gradle test` output in the Tests section ended in console control
  sequences.
- Backup downloads, offsite copies and archive imports left out the
  archives of addon volumes, so a backup restored from a download or
  from offsite brought back no addon data.
- Symfony's Doctrine migrations failed with "Invalid platform version"
  because the injected `DATABASE_URL` carries no server version; the
  template now reads `DB_SERVER_VERSION` in `doctrine.yaml`. Existing
  Symfony projects can add `server_version: '%env(DB_SERVER_VERSION)%'`
  under `doctrine.dbal` themselves.
- A project's stats took up to 18 s with many containers on the host,
  because every container on the host was sampled. Only the project's
  containers are sampled now.
- On a phone the project's database card was wider than the screen.

## [0.16.2] - 2026-09-29

### Added
- The dashboard notes containers that an older Envoryx created from another
  spec (a changed command or health check) and asks for a restart of the
  project, which recreates them. Such containers kept running as they were;
  a PostgreSQL container from before 0.10.0, for example, kept logging
  `FATAL: role "root" does not exist` every ten seconds.

### Fixed
- A project whose name starts with "pg" and a separator (e.g. "PG Probe")
  got the database user `pg_…`, which PostgreSQL refuses, so the database
  never started. Such names, the system databases (`postgres`, `mysql`,
  `admin` …) and `root` now get an `app_` prefix. Existing projects keep
  their names.

## [0.16.1] - 2026-09-29

### Changed
- Pages use the full width of the window. *Settings → Profile → Appearance*
  switches back to the centred column (*Boxed*); like the accent colour, the
  choice is stored per browser.

### Fixed
- The status warnings of a project ("project should be running but is
  missing", the notes on images that a restart applies and on containers
  of removed workers or services) showed in English in every language.

## [0.16.0] - 2026-09-29

The interface has a new layout. Projects, the settings and the Docker page
use a grouped sidebar instead of long rows of tabs, and the new-project
wizard starts with where a project comes from. Several settings moved (see
*Changed*); links to the old places still land on the right one.

### Added
- New-project wizard in three steps. It starts from a gallery of all
  templates, with a search and a runtime filter, or from a Git repository,
  an existing website or an empty project; the runtime follows from that
  choice, and a repository names the project after itself. The second step
  holds the name, the database and the services; runtime versions, more
  runtimes as tools, the web server, the directories and the environment
  wait under *Advanced settings*. A summary next to every step shows each
  choice and jumps to where it's set.
- Python 3.15 as a preview version.

### Changed
- A project's sections sit in a sidebar: *Overview*, then *Develop*
  (Terminal, Actions, Tests, IDE), *Code* (Git, Branches), *Configuration*
  (Runtime, Environment, Domains, Workers & cron), *Data* (Database,
  Services, Backups) and *Observe* (Logs, Resources, History), with a select
  on phones. The chosen section is kept in the address (`?tab=`). Workers
  and cron jobs share one section. *Share publicly*, *Rename*, *Duplicate*,
  *Delete* and the Docker plan (formerly the *Advanced* tab) moved into the
  *More actions* menu in the header; a *Shared* badge stays visible while a
  project is shared.
- The settings use the same sidebar. Everyone has *My account* (Profile
  with appearance and password, API tokens & MCP, SSH keys). Admins also
  get *Instance* (Diagnostics, General, Domains & HTTPS), *Access &
  security* (Users, SSH access, Git deploy key, Secret key), *Operations*
  (Backups, Notifications, Retention, Audit log) and *Extensions* (Addons,
  Database browser, Package cache, Private registries). The host for
  project links and the Xdebug host moved to *Domains & HTTPS*, log,
  resource and audit log retention to *Retention*, and the secret key from
  *Access* to *Settings → Secret key*.
- The dashboard shows one box of notices (set-up check, update,
  inconsistencies, what Envoryx did on its own), the recent projects with
  start, stop and open, and a system card with Docker, CPU, memory and disk
  space. Users who aren't admins no longer get links to the Docker page.
- The Docker page is split into *Overview*, *Containers* (grouped by
  project), *Networks & volumes* (with the project each one belongs to) and
  *Images*. Host paths are checked in the diagnostics.
- Your username at the bottom of the navigation opens your profile. The
  German interface calls the dashboard "Dashboard".
- Python 3.14 is the default for new projects.
- The Envoryx image is built on Alpine 3.24.

### Fixed
- Dialogs could announce the title of another dialog to screen readers.
- On phones, project rows squeezed the project name to one letter, and the
  dashboard scrolled sideways.

## [0.15.0] - 2026-09-29

**Keep a copy of your secret key.** This version encrypts the stored secrets
at its first start. Afterwards, open *Settings → Access → Secret key* and
store the key somewhere safe, or set it as `ENVORYX_SECRET_KEY` (the Unraid
template has an optional field for it). Without the key, an instance backup
can't be restored on another host.

### Added
- Secrets at rest are encrypted (AES-256-GCM): Git tokens, secret project
  variables, service credentials, addon secrets, the single sign-on client
  secret and registry logins in the database, the notification, offsite and
  Let's Encrypt settings files, and the project export in every project
  backup. The key comes from `ENVORYX_SECRET_KEY` or, without it, from
  `/config/secret.key`, which Envoryx creates. *Settings → Access → Secret
  key* shows the key (keep a copy) and replaces it; with the variable,
  `ENVORYX_SECRET_KEY_OLD` carries the previous key for one start. Instance
  backups never contain the key and record its ID; restoring one made with
  another key asks for that key. A start whose key doesn't fit the database
  is refused with the key ID it needs.

### Changed
- The first start of this version encrypts the existing secrets, files and
  project backups in place and, unless `ENVORYX_SECRET_KEY` is set, creates
  `/config/secret.key`. Keep a copy of the key: without it an instance
  backup can't be restored on another host. Instance backups made with
  earlier versions still hold the secrets unencrypted.

## [0.14.0] - 2026-09-29

### Added
- Custom runtime images. A runtime (PHP, Node.js, Python, Go, Ruby, Java,
  .NET) can run an image from a registry or one Envoryx builds from a
  Dockerfile in the project (*Runtime → Runtime images*). The Dockerfile's
  directory is the build context; when a file there changes, the next start
  builds a new image, and *Rebuild without cache* fetches fresh base images.
  Workers and cron jobs follow their runtime; databases, services and web
  servers keep the vetted images. Every image is checked when it's set or
  built, and what it lacks (a shell, git, socat, the runtime's tools, Xdebug
  …) is shown as a warning, but the image is used anyway. `envoryx.yml`
  takes `image:` or `dockerfile:` per runtime.
- Private registry logins under *Settings → Tools → Private registries*,
  used for pulls and for the `FROM` images of builds. Passwords are
  write-only.
- Built images (`envoryx-build/…`) are listed and removed with the other
  unused images.
- Addons: services described in a YAML file instead of Envoryx's code. An
  addon is one container per project with selectable image versions,
  environment (with secrets generated per project), named volumes, a port
  with an optional web UI at `<slug>-<name>.<base domain>` and a host port, a
  health check, the variables it injects into the application and the
  credentials the UI shows. Admins install files under *Settings → Addons*
  (write one, paste one, install from a URL, or start from the shipped
  pgAdmin, phpMyAdmin, Elasticsearch, Soketi and Keycloak examples);
  developers add installed addons on a project's *Services* tab. The format
  has no way to ask for privileges, host paths, the host network or devices.
  A new version of a file reaches the projects using it at their next start.
  Addon volumes go into database backups (the container stops while they are
  read) and come back on restore. `envoryx.yml` takes `addons:`.

## [0.13.0] - 2026-09-28

### Added
- Java runtime. The wizard's first step offers *Java application*; a Java
  container (`ghcr.io/envoryx/envoryx-java:<17|21|25>`, Eclipse Temurin plus
  Maven and Gradle, LTS releases only) is added on any project from the
  Runtime tab, too. *Run the Java server* runs Spring Boot (`spring-boot:run`
  or `bootRun`, DevTools restarts on compiled changes), Quarkus
  (`quarkus:dev` or `quarkusDev`, live reload) or a plain jar; production mode
  builds once and runs the jar. Maven or Gradle follows the project's build
  file, and its `mvnw`/`gradlew` wins. Without PHP and without a Python, Go or
  Ruby server the project URL reaches it. *Debug with JDWP* publishes a JDWP
  port for IntelliJ IDEA's Remote JVM Debug and VS Code (the IDE tab has
  both). Maven's repository and Gradle's caches are shared by all projects.
- `SPRING_DATASOURCE_*`, `QUARKUS_DATASOURCE_*` and `JDBC_URL` (plus
  `<NAME>_JDBC_URL` per additional database) point Java apps at the project
  database; MongoDB, Redis and Mailpit get the Spring and Quarkus variables,
  too. Quarkus Dev Services are switched off.
- Java templates *Spring Boot* (start.spring.io) and *Quarkus REST*
  (code.quarkus.io), with JPA or Hibernate and the driver of the project
  database; Maven and Gradle actions; `mvn test`/`gradle test` on the Tests
  tab against `<database>_test`, with results per test; *Jar file* and *Build
  tool goal* workers; cron jobs, SSH (`<project>.java`), the terminal, logs,
  the manifest (`java:`), the CLI (`--java`, `--java-server`,
  `--java-preset`) and the MCP tools know Java.
- Site import recognises `pom.xml` and `build.gradle`.
- .NET runtime. The wizard's first step offers *.NET application*; a .NET
  container (`ghcr.io/envoryx/envoryx-dotnet:<8|10>`, the official SDK image
  plus `dotnet-ef` and the netcoredbg debugger, LTS releases only) is added on
  any project from the Runtime tab, too. *Run the .NET server* runs ASP.NET
  Core under `dotnet watch` (hot reload, restart when an edit can't be
  applied) or publishes once and runs the DLL in production mode; the *DLL*
  preset publishes and runs any other application (worker services, console
  hosts). The project file is found on its own (the one at the top, else the
  one web or worker project) or set by hand. Without PHP and without a Python,
  Go, Ruby or Java server the project URL reaches it. NuGet packages are
  shared by all projects.
- `ConnectionStrings__DefaultConnection` (ADO.NET form for Npgsql and the
  MySQL providers) points .NET apps at the primary SQL database,
  `ConnectionStrings__<name>` at each additional one, and
  `ConnectionStrings__MongoDB` and `ConnectionStrings__Redis` at those
  services.
- Debugging .NET needs no port: VS Code starts `netcoredbg` in the container
  through `pipeTransport` over the SSH user `<project>.dotnet`, Rider and
  Visual Studio attach to the remote process over SSH.
- .NET templates *ASP.NET Core Web API* (EF Core with a sample `/todos`
  endpoint on PostgreSQL, MySQL or MariaDB), *ASP.NET Core MVC*, *Blazor Web
  App* and *ASP.NET Core Razor Pages*, all from `dotnet new`; `dotnet` and
  `dotnet ef` actions; `dotnet test` on the Tests tab against
  `<database>_test`, with results per test from the TRX report; *Project* and
  *DLL* workers; cron jobs, SSH (`<project>.dotnet`), the terminal, logs, the
  manifest (`dotnet:`), the CLI (`--dotnet`, `--dotnet-server`,
  `--dotnet-preset`) and the MCP tools know .NET.
- Site import recognises solution and project files (`.sln`, `.slnx`,
  `.csproj`, `.fsproj`, `.vbproj`).
- Branch environments. The new *Branches* tab makes a copy of a project on
  another branch of its repository: files (with `.env`, `vendor/` and
  `node_modules/`), database, bucket, workers and cron jobs are copied, the
  working tree switches to the branch, and the copy starts under its own URL
  (`<project>-<branch>.test`). Deploy commands set on the parent (say
  `composer install` and `php artisan migrate --force`) run after the copy
  and after every pull, in the environment's application container.
  *Watch the repository* asks the remote with `git ls-remote`, so it works
  behind NAT without a webhook: a push is pulled and deployed, a deleted
  branch takes its environment with it, and a new branch matching a pattern
  like `feature/*` gets one (up to five by default). Environments nobody
  opened for a set number of days are stopped. A project with environments
  can't be deleted until they're gone.
- `branches:` in `envoryx.yml`, `envoryx project branches|branch|deploy`, the
  MCP tools `list_branch_environments`, `create_branch_environment` and
  `deploy_branch_environment`, and the notifications `branch.failed` (on by
  default) and `branch.changed` (off by default).
- Users and roles. *Settings → Users* invites people: you pick a name and a
  role, Envoryx hands you a link that works once within 48 hours (it sends no
  mail), and the new user sets a password through it. A new link resets a
  forgotten password. The roles match the token scopes: *Viewer* looks,
  *Developer* works with projects (start, stop, terminal, actions, git,
  backups), *Admin* does everything including users and settings, and *No
  access* reaches only the projects a user is given. A role per project
  replaces the global one there, so someone can be a viewer everywhere and a
  developer in one shop. A project a user has no access to doesn't show up
  anywhere (lists, dashboard, MCP, SSH); branch environments take over their
  parent's project roles. Users can be disabled, and the last admin can't be
  demoted, disabled or deleted.
- Single sign-on with OpenID Connect (Authentik, Keycloak, Authelia, Google,
  Dex …): authorization code with PKCE, a *Sign in with …* button on the login
  page, and optionally the role set from the provider's groups at every
  sign-in. Users can be created at their first sign-in, or only invited ones
  get in, and an existing account is never taken over by its name.
- Every user has their own SSH keys (*My SSH keys*), which act with that
  user's roles, and manages their own API tokens.

### Changed
- An API token never does more than the user it belongs to, whatever its
  scope; a scope above the owner's role is refused when the token is created.
  Admins see everyone's tokens with their owner, everyone else only their own.
  Existing accounts stay admins, so existing tokens keep working as before.
- The public keys under *Settings → Access → SSH access* are now the *Admin
  keys*: they still open every project. Keys that should follow a user's
  roles go under that user's *My SSH keys*.
- README, DEPLOYMENT, ARCHITECTURE, DEVELOPMENT, SECURITY and CONTRIBUTING
  are rewritten in plainer words, and every code comment with them.

## [0.12.0] - 2026-09-27

### Added
- Go runtime. The wizard's first step offers *Go application* next to PHP,
  Python, Node.js and static; a Go container
  (`ghcr.io/envoryx/envoryx-go:<1.26-1.27>`, official `golang:<v>-bookworm`
  image plus air, Delve and gotestsum) is added on any project from the
  Runtime tab, too. *Build and run the server* builds the main package (`.` or
  e.g. `./cmd/server`) and runs it as the container's main process - rebuilt
  by air on every change in development mode (a project's `.air.toml` wins),
  built once in production mode - on `$PORT` (8080). Without PHP and without
  a Python server the project URL reaches it; before a `go.mod` exists the
  container waits instead of crash-looping. *Debug with Delve* runs the
  server under a headless Delve on a published port for GoLand and VS Code
  (the IDE tab has both configurations); without the server the port serves
  a `dlv` started in the terminal. The module and build caches are shared by
  all projects (and listed under *Settings → Package cache*).
- Go templates *Go (net/http)*, *Gin* and *Echo*; Go actions (`go version`,
  `go build`, `go vet`, `gofmt -l`, `go mod tidy`/`download`, `go generate`);
  the Tests tab runs `go test ./...` through gotestsum; worker preset *Go
  program* (builds a package, runs the binary); cron jobs, SSH (`<project>.go`), the site
  import (`go.mod`), `envoryx.yml` (`go:`), the API (`go`), MCP (`goVersion`,
  `goServer`, `goPackage`, `goPort`, `goMode`) and the CLI (`--go`,
  `--go-server`, `--go-package`) cover Go. The image builds and the weekly
  runtime-version check include Go.
- Ruby runtime. The wizard's first step offers *Ruby application*; a Ruby
  container (`ghcr.io/envoryx/envoryx-ruby:<3.3-4.0>`, official
  `ruby:<v>-slim-bookworm` image plus the build dependencies of common gems
  and the debug gem, no Node.js) is added on any project from the Runtime
  tab, too. *Run the Ruby server* has two presets: *Rails* (`bin/rails server` in
  development mode, Puma in production mode) and *Rack* (Puma on
  `config.ru` - Sinatra, Roda, Hanami …), on `$PORT` (3000/9292). Before it
  starts, the server waits for its `Gemfile` and runs `bundle install` when
  the bundle is incomplete, so a cloned application comes up on its own; the
  gems live in the project home, Bundler's download cache is shared by all
  projects. `RAILS_ENV`, `RACK_ENV`, `APP_ENV` and `HANAMI_ENV` follow the
  mode, and Rails in development accepts the project's host names. Without
  PHP and without a Python or Go server the project URL reaches it. *Debug
  with rdbg* runs the server under `rdbg --open` on a published port for VS
  Code's rdbg extension or `rdbg -A` (the IDE tab has the configuration);
  without the server the port serves an `rdbg` started in the terminal.
  RubyMine debugs through its SSH remote interpreter (`<project>.ruby`) with
  its own debugger - its *Ruby remote debug* speaks only `ruby-debug-ide`. The Ruby containers get
  PostgreSQL's `DATABASE_URL` as `postgresql://`, which Active Record
  understands.
- Ruby templates *Rails* (Hotwire with importmap), *Rails (API only)* and
  *Sinatra*; Bundler and Rails actions (`bundle install`/`update`/`outdated`,
  `rubocop`, `rails db:prepare`/`migrate`/`rollback`/`seed`, `routes`,
  `assets:precompile`, `tmp:clear`, `about`); the Tests tab runs `rspec`
  (with a JUnit report when `rspec_junit_formatter` is in the bundle) and
  `rails test` against `<database>_test`, which Envoryx creates - never
  against the development database; worker presets *Solid Queue*, *GoodJob*,
  *Sidekiq*, *Rake task* and *Ruby script*, running in the server's
  environment; cron jobs, SSH (`<project>.ruby`), the site import
  (`Gemfile`), `envoryx.yml` (`ruby:`), the API (`ruby`), MCP (`rubyVersion`,
  `rubyServer`, `rubyPreset`, `rubyPort`, `rubyMode`) and the CLI (`--ruby`,
  `--ruby-server`, `--ruby-preset`) cover Ruby. The image builds and the
  weekly runtime-version check include Ruby.

### Fixed
- SSH sessions into the Python container (`<project>.python`, also IDEs that
  probe over SSH) now find the project's `.venv` first on `PATH` - `python`,
  `pip`, `pytest` resolved to the image's interpreter, because the login shell
  reset `PATH`. Needs the updated `envoryx-python` images.

## [0.11.0] - 2026-09-26

### Added
- External databases and Redis: a database (primary or additional) or Redis
  can be an existing MariaDB, MySQL, PostgreSQL or Redis server instead of a
  container of the project - *On an external server* in the wizard, the
  Database tab and the Services tab, `external` in the API and in
  `envoryx.yml` (without the password). The connection is tested before it is
  stored (and with *Test connection* beforehand); `host.docker.internal`
  reaches a server on the Docker host. Backups, snapshots, restores, cloning,
  the site import and Adminer work against the server; Envoryx never drops
  databases there, never changes its password and only forgets the connection
  when it is removed. A duplicate gets local containers with the data.
- Ollama as a project service for local LLMs (*Services*, the wizard,
  `--ollama`, MCP, `ollama:` in `envoryx.yml`). The application gets
  `OLLAMA_HOST`, `OLLAMA_BASE_URL` and `OLLAMA_URL`. All projects share one model
  store, so a model is downloaded once; models are downloaded, followed,
  cancelled and deleted on the Ollama card. *Use the GPU* hands the host's
  NVIDIA GPUs to Ollama; Envoryx checks beforehand that Docker can (NVIDIA
  Container Toolkit, on Unraid the Nvidia Driver plugin) and says so when it
  cannot.
- The audit log can be searched and filtered by user (with its API tokens),
  category, project and date, pages back through older entries, and exports
  the filtered entries as CSV or JSON Lines. Every project has its own
  *History* tab. A project change now records each setting it changed with
  its value before and after, shown when the entry is opened. A retention
  (30 days to two years, or forever as before) is set under *Settings → Audit
  log*.

### Changed
- Builds of the development branch (`:main`) are named after the release they
  follow: `0.10.0+5 (73304d6)` - five commits after 0.10.0 - instead of
  `main-` and the full commit hash. Releases show their number without the
  `v`.

## [0.10.0] - 2026-09-26

### Added
- `ENVORYX_URL`: the address a project answers at, injected into its
  containers and recomputed with every plan (after a rename, too) - for
  `APP_URL=${ENVORYX_URL}` in a `.env`.
- Rules for a project's host names, applied by the proxy (*Domains → Rules*,
  `PUT /projects/{id}/proxy-rules`): an address allowlist, HTTP basic
  authentication, redirects (paths and prefixes, to paths or other hosts),
  response headers to set or remove, and CORS with preflight answers. They
  also apply to a share, which now goes through the proxy - a password
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

### Fixed
- pnpm failed in the Node container with `EACCES` under
  `/usr/local/lib/corepack`: the image's pnpm lacked its native binary and
  fetched it on first use, and a version pinned in `packageManager` could not
  be downloaded either, as the containers run as the project user. The Node
  images now carry the binary and let Corepack add versions for any user.
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

## [0.9.0] - 2026-09-26

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
  the site's configuration is wired to the project database - `wp-config.php`,
  Drupal's `settings.php`, TYPO3's additional configuration, Joomla's
  `configuration.php`; each original is kept as `*.envoryx-original.php` that
  answers 404 - and the old server's configuration caches are removed. The dump
  is imported without the statements that tie it to the old server (`USE`,
  `CREATE DATABASE`, owners and grants); a failed import rolls the project back.
  `envoryx import <folder|archive> [name] --db dump.sql` does the same from the
  command line and packs a local folder on the fly. See DEPLOYMENT.md,
  *Importing an existing website*.
- Several databases per project. Next to the primary database (host
  `database`, `DB_*`) a project can have any number of additional ones with a
  name of their own - for example PostgreSQL `analytics` next to MariaDB: its
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
  template scaffolds of every project share, so a package is downloaded once -
  a second Laravel project is created in about a quarter of the time.
  *Settings → Tools → Package cache* shows its size per tool and empties it.
  Instance backups leave it out.
- Test runner (*Tests* tab): Envoryx finds a project's test suites - Pest,
  PHPUnit (also Symfony's `bin/phpunit`), the `test`/`test:*` scripts of
  `package.json`, Playwright, Cypress, pytest and Django - and runs them in the
  runtime container with live output and an optional filter. Where the runner
  writes a JUnit report, the result lists every failed test with its message,
  file and line; the last 50 runs of a project are kept.

### Fixed
- Renaming a project whose database is PostgreSQL failed with "session user
  cannot be renamed": Envoryx logged in as the very login it renamed, the only
  superuser there is. The rename now runs through a short-lived helper login
  that is removed right afterwards.

## [0.8.0] - 2026-09-25

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
  (CPU, memory, network, disk I/O) and each project's disk space - volumes,
  project directory, backups - once an hour. The new *Resources* tab of a
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
  runs out of memory - also when only a child process is killed and the
  container keeps running - the project shows a warning for a day and a
  notification goes out (new event `project.oom`, on by default). Limits are part of `envoryx.yml`
  (`limits:`) and of `envoryx project show`.

## [0.7.1] - 2026-09-24

### Fixed
- Recreating a running container - after a version or port change, a rename -
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

## [0.7.0] - 2026-09-24

### Added
- Project manifest `envoryx.yml`: runtimes (with PHP extensions and ini
  settings), web server, database, services, domains, environment (secrets by
  name only), workers and cron jobs as a file in the repository. `envoryx up`
  in a clone creates the project from it - the server clones the repository's
  origin at the checked-out branch - or brings an existing project in line;
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
- Offsite backups: *Settings → Backups* takes targets - S3-compatible storage
  (AWS, Backblaze B2, Wasabi, Hetzner Object Storage, Cloudflare R2, MinIO),
  SFTP (Hetzner Storage Box, NAS; the server key is pinned) or WebDAV
  (Nextcloud, ownCloud) - with a connection test. Scheduled project backups go
  up by themselves, a daily instance backup at a chosen hour too, everything
  else with *Copy offsite* or *Also copy offsite*; each target keeps its own
  number of scheduled copies. Archives are optionally encrypted with age
  (scrypt passphrase) before they leave the host. Failed uploads are retried
  with growing pauses and reported through the *Backup failed* notification.
  The Backups tab shows each copy's state and lists what a target holds for the
  project, deleted backups included; *Fetch* brings one back. A fresh Envoryx
  fetches its instance backup from the target the same way - disaster
  recovery in four steps (DEPLOYMENT.md → *Offsite backups*). CLI: `envoryx
  backup create --offsite`, `backup offsite`, `backup remote`, `backup fetch`.
- More DNS providers for the Let's Encrypt wildcard certificate: Hetzner
  (through the Hetzner Cloud API - the old DNS Console API was shut down in May
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
- Starting a project whose directory was missing - on a fresh host after a
  recovery, or removed by hand - let Docker create it for the bind mount,
  owned by root, so the application could not write to it. Envoryx now
  creates it as the project user first.
- The certificate check waited for the challenge record at 1.1.1.1; asking
  before the record existed could get the "does not exist" cached for as long
  as the zone allows. Envoryx now asks the zone's own name servers and waits
  until all of them serve the record.

## [0.6.0] - 2026-09-24

### Added
- Python runtime. The wizard's first step offers *Python application* next
  to PHP, Node.js and static; a Python container
  (`ghcr.io/envoryx/envoryx-python:<3.10-3.14>`, official slim image plus
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
  `<project>-dev.<base>` - Django/FastAPI backend plus Vite frontend.
- Python templates: *Django* (`startproject config`, settings prepared for
  the proxy and `DATABASE_URL` via dj-database-url, psycopg and mysqlclient
  installed), *Flask* and *FastAPI* - each creates the `.venv`, installs
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
  recreate, but it is built for one minor version - after a change from 3.13 to
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
  `RABBITMQ_VHOST` and `RABBITMQ_URL` (`amqp://…@rabbitmq:5672/%2f`) - the names
  laravel-queue-rabbitmq reads, and a URL for Symfony Messenger, php-amqplib, amqplib
  and Celery. `MESSENGER_TRANSPORT_DSN` stays yours to set - in Symfony's `.env`,
  `MESSENGER_TRANSPORT_DSN=${RABBITMQ_URL}/messages` - because injecting it would
  quietly move a Doctrine transport to AMQP. The password is shown on the Services tab
  on request (operate scope, like database credentials); the IDE tab lists the
  connection for desktop clients next to the database and Mailpit.
- Memcached as an optional cache next to Redis - in the wizard, on the Services tab, in
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
- Meilisearch and Typesense as optional search engines - in the wizard, on the Services
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
- OpenSearch as an optional Elasticsearch-compatible search engine (3.8, or 2.19) - in
  the wizard, on the Services and IDE tabs, in `envoryx project create --opensearch`,
  the MCP `create_project` tool and the logs endpoints. It runs as a single development
  node with its indices in a volume, over plain HTTP without login (security plugin
  off) and with a 512 MB heap (about 1 GB of RAM); the port is published on request.
  The application gets `OPENSEARCH_HOST`, `OPENSEARCH_PORT`, `OPENSEARCH_SCHEME` and
  `OPENSEARCH_URL` (`http://opensearch:9200`). `ELASTICSEARCH_*` is left to the
  application: current Elasticsearch clients refuse to talk to OpenSearch.
- OpenSearch Dashboards as an option of OpenSearch - a checkbox in the wizard and on the
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
  log under the token's name - the CLI has no database handle and no Docker
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
  directory - where the browser terminal also starts. It is backed by a new
  endpoint, `POST /api/v1/projects/{id}/services/{kind}/exec`, which runs
  without a pseudo-terminal and answers with newline-delimited JSON frames
  (`stdout`, `stderr`, then `exit`); nothing is written before the first
  frame, so a container that is not running is still an ordinary HTTP error.
  The terminal WebSocket stays what it is - a PTY for humans, where the
  streams are merged and the exit code is lost; scripts need the opposite,
  and a big pipe or an interactive shell still belongs in SSH.
- Duplicate a project. *Duplicate* on the project page copies an existing
  project into a new one - `shop` → `shop-test` - with its configuration:
  runtimes and their settings, web server, services, environment variables,
  workers and the repository binding. The parts that hold data are checkboxes
  and default to on: the project directory (without `vendor/`,
  `node_modules/` and the other regenerable directories unless asked), the
  contents of the database and the objects of the bucket. Extra domains and
  the backup schedule are never copied - host names are unique, and a copy
  made to try something out should not inherit the original's scheduled
  backups.
  The copy is its own project in every way that has to be: new id, slug,
  directory, network, volumes, containers, and a fresh host port wherever the
  original published one. What it keeps are the generated credentials -
  database name, user and passwords, the bucket and its keys - because each
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
  otherwise - handy when a committed `.env` or an external client has the old name
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
- Database snapshots and cloning - the two things a day of development keeps asking for:
  the dump you take before a migration, and the data of another project in your own.
  *Snapshots* on the project's Database tab dumps the primary database and nothing else,
  with a note like "before the orders migration", and puts it back with one click. The
  project does not have to be running for either: a stopped database container is started
  for the dump or the import and stopped again afterwards. Snapshots are ordinary backups
  under `/config/backups/<slug>/` - they show up in the Backups tab, can be downloaded and
  restore through the same verified path - so the dump a database version upgrade insists
  on appears among them too, ready to be put back. They roll: the ten newest of a project
  are kept, so taking one before every migration does not fill the disk, and scheduled
  backups and anything made by hand are never touched by that.
  *Clone from another project* replaces this project's database contents with another's -
  staging into local - as long as both run the same engine. The dump is piped straight
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
  also shown as MD5, the form PhpStorm asks you to confirm - the SHA256 value alone
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
  as root, add the entry and drop to PUID/PGID before the command runs - the same entry
  the long-running containers already got.
- A failed template now says why. The error showed the last line of the output, which
  for a crashing Node process is just "Node.js v24.x" and for npm the path of its log
  file. It now shows the thrown error or npm's cause, and the last 40 lines of the
  output go to the Envoryx log.
- MongoDB is offered as 8.2 and that is what a new project gets. The previous
  default 8.0 - and 7.0 - refuse to start on Linux 6.19 and newer ("MongoDB
  cannot start: Linux kernel versions 6.19 and newer has a known
  incompatibility with this version", SERVER-121912), which is every current
  desktop and server kernel: the container went into a restart loop and the
  project never came up. Both older series stay selectable for hosts that run
  them; an existing project keeps its version, as a MongoDB major cannot be
  upgraded in place anyway.
- An application server (Python, Node dev server) raced the database on every
  start: it came up while the database was still initialising, and anything
  that connects at boot died on the first try. The restart policy hid that for
  most servers, but Django's `runserver` does not exit - its autoreload parent
  survives the failed child, so the container stayed *running* and answered
  nothing until it was restarted by hand. The server now waits for the
  database port (socat, two-second retries, visible in the container log)
  before it starts, on a host reboot too. A project without a database keeps
  the exact command it had, so nothing is recreated for it.
- A new project with the default PostgreSQL 18 came up with a database
  container in a restart loop: the data volume was mounted at
  `/var/lib/postgresql/data`, and the 18 image - which keeps its cluster in
  `/var/lib/postgresql/<major>/docker` - refuses to start when it finds a
  volume on the old path, even an empty one. From 18 on the volume takes
  `/var/lib/postgresql` (the layout `pg_upgrade --link` expects); 16 and 17
  keep the data directory itself, so existing volumes stay where they are.
  A major upgrade was already refused for PostgreSQL, so no data moves.

- A rollback target that had left the host (`docker rmi`, a prune on an
  installation that predates the rollback tags) made every reconcile - once
  every 30 seconds - retry the tag and log *rollback image not protected*.
  The image cannot come back, so the history now forgets it on the first
  miss (one info line), and the project stops offering a rollback that
  could only fail.
## [0.5.0] - 2026-09-22

### Added
- Projects without PHP. The first wizard step asks for the runtime - *PHP
  application*, *Node.js application* or *Static site* - and PHP is no
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
  build* - every start runs the build script, then the serve script
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
  in plain words - actions as labels instead of `docker.orphans_removed`,
  "Envoryx (automatic)" as the actor of automatic entries, and a details
  column with what was changed or removed.
- Orphaned Envoryx containers and networks - left behind by a restored
  instance backup or a wiped `/config` - are stopped and removed by the
  reconciler about a minute after they appear, instead of lingering in the
  host's Docker list. Volumes hold data and are never removed automatically;
  the Docker page lists them with a *Remove* button. Removals appear in the
  audit log as `docker.orphans_removed`.
- Settings → General → *Projects and the Envoryx container*: an opt-in that
  stops every running project when the Envoryx container is stopped (for
  maintenance, a host shutdown) and starts them again when Envoryx comes
  back - also after a reboot of the host. Off by default: the project
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
  not published - the document root would otherwise expose the project
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

## [0.4.0] - 2026-09-20

### Added
- Rescue commands for a lost login: `envoryx admin reset-password`,
  `logout-all`, `revoke-tokens`, `reset` and `users`, run from the container
  shell (`docker exec -it envoryx envoryx admin …`). They act on the live
  database, need no restart and land in the audit log as `cli`. The sign-in
  page links to the instructions.
- The interface speaks eight more languages: French, Spanish, Italian, Dutch,
  Polish, Portuguese (Brazil), Russian and Ukrainian. Pick one in the sidebar;
  the browser language is used on first visit.
- Envoryx now shows what it is doing. Long actions - creating, starting,
  restarting, applying settings, deleting, backups and restores - report their
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

## [0.3.0] - 2026-09-20

### Added
- Envoryx has its logo: the `<E>` mark and wordmark replace the placeholder
  icon in the sidebar, on the sign-in page, as favicon and as the Unraid
  template icon. Mint is the default accent colour; **Settings → General →
  Appearance** offers ocean, violet, amber and rose - buttons, highlights and
  the logo follow. The choice is stored per browser, like the theme.
- Settings are organised in tabs (Diagnostics, General, Domains & HTTPS,
  Access, Notifications, Backups, Tools, Audit log). The new **Diagnostics**
  tab runs 14 set-up checks - Docker, storage, host paths, disk space, backup
  directory, database integrity, host for project links, proxy ports, wildcard
  DNS, SSH, HTTPS, version, project/Docker consistency, notifications - and
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
  for project links is configured, links to published ports - project URLs,
  Mailpit, the object storage console, database ports - silently pointed at
  Envoryx's address, where nothing listens. The dashboard, the affected
  project tabs and the settings now say so and offer the Docker host that
  Docker reports as a one-click fix.

## [0.2.0] - 2026-09-19

### Added
- S3-compatible object storage as an optional project service: one RustFS
  container per project with a persistent volume, generated access keys, a
  bucket named after the project created at start-up, `S3_*` and Laravel/AWS
  SDK `AWS_*` variables injected, the S3 API served by the embedded proxy as
  `<project>-s3.<base domain>` for presigned URLs and public assets, a web
  console, and a switch for anonymous reads (bucket policy standing in for
  public-read ACLs). Wizard, Services tab and MCP `create_project` know it.
- API tokens have scopes: `read` (look, no secrets), `operate` (work with
  existing projects - start/stop, actions, backups, databases, git, SSH) and
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
  `docker image prune` - Unraid's "remove unused images", clean-up plugins -
  no longer deletes it. Existing rollback targets are tagged at the next start;
  the tag moves on when a newer image supersedes it and goes with the project.
- A backup interrupted by a crash or `kill -9` no longer lingers: at start-up
  Envoryx removes project backup directories that never got their metadata
  and adopts complete ones whose database record was not written yet, so
  they show up and can be restored. Instance backups are written under a
  temporary name and renamed when complete, so a truncated archive can never
  be mistaken for a good one; leftovers are removed at start-up as well.

## [0.1.0] - 2026-09-19

First tagged release. Everything below is new.

### Projects
- Single-container deployment for Unraid and Linux Docker hosts, embedded
  web UI (English, German), local admin account, sessions, audit log.
- Per-project stacks from a fixed catalogue: PHP 8.1-8.6 (Envoryx images with
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

[Unreleased]: https://github.com/envoryx/envoryx/compare/v0.21.0...HEAD
[0.21.0]: https://github.com/envoryx/envoryx/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/envoryx/envoryx/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/envoryx/envoryx/compare/v0.18.1...v0.19.0
[0.18.1]: https://github.com/envoryx/envoryx/compare/v0.18.0...v0.18.1
[0.18.0]: https://github.com/envoryx/envoryx/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/envoryx/envoryx/compare/v0.16.2...v0.17.0
[0.16.2]: https://github.com/envoryx/envoryx/compare/v0.16.1...v0.16.2
[0.16.1]: https://github.com/envoryx/envoryx/compare/v0.16.0...v0.16.1
[0.16.0]: https://github.com/envoryx/envoryx/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/envoryx/envoryx/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/envoryx/envoryx/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/envoryx/envoryx/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/envoryx/envoryx/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/envoryx/envoryx/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/envoryx/envoryx/compare/v0.9.0...v0.10.0
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
