import type { TFunction } from "i18next";
import i18n from "@/i18n";
import { ApiError } from "@/api/client";
import { formatNumber } from "@/lib/format";

/**
 * Messages the backend produces with values embedded ("project has no web service").
 * Each template is also the German dictionary key; the placeholders become capture groups
 * so the value is carried over into the translation. A {{count}} picks the plural form; the
 * server writes the singular for 1, which a [message, key] pair maps to the same key.
 */
const templates: (string | [string, string])[] = [
  "Request failed ({{status}})",
  "Upload failed ({{status}})",
  "unknown project {{name}}",
  "unknown template \"{{name}}\"",
  "unknown worker preset \"{{name}}\"",
  "unsupported web server \"{{name}}\"",
  "database type \"{{type}}\" is not supported",
  "project has no {{service}} service",
  "project {{project}} has no {{service}} service",
  "project {{project}} has no application container",
  "the {{service}} container is not running",
  "the {{service}} container does not exist; start the project",
  "template {{template}} needs Node.js",
  "template {{template}} needs Node.js {{min}} or newer, not {{version}}",
  "template {{template}} needs Python",
  "template {{template}} needs a Node service",
  "template {{template}} needs a Python service",
  "template {{template}} needs Go",
  "template {{template}} needs a Go service",
  "template {{template}} needs Ruby",
  "template {{template}} needs a Ruby service",
  "template {{template}} needs Java",
  "template {{template}} needs a Java service",
  "template {{template}} needs .NET",
  "template {{template}} needs a .NET service",
  "template {{template}} needs a PHP service",
  "template {{template}} needs a database service",
  "template {{template}} needs a database",
  "template {{template}} needs PHP",
  "template {{template}} failed at \"{{step}}\": {{error}}",
  "git clone failed: {{reason}}",
  "git {{command}} failed: {{reason}}",
  "invalid branch name \"{{name}}\"",
  "invalid host name \"{{name}}\"",
  "invalid version \"{{version}}\"",
  "confirmation must equal the project identifier \"{{slug}}\"",
  "no free port in range {{from}}-{{to}}",
  "no previous image is known for {{service}}",
  "the previous image {{image}} is no longer on this host",
  "downgrading {{service}} from {{from}} to {{to}} is not supported by the data format",
  "{{service}} cannot upgrade an existing data directory from {{from}} to {{to}} in place; export, remove and re-add the database",
  "the dump is for {{dump}} but the project uses {{project}}",
  "the database browser cannot open {{type}} databases; use the published port with a desktop client",
  "the backup was made with a newer Envoryx (schema {{backup}}, this build supports {{supported}})",
  "this backup was made by Envoryx {{version}} in a newer format; update this Envoryx to restore it",
  "the database of {{project}} refuses Envoryx's login; was the password changed inside the database?",
  "{{host}} is already the default host name of this project",
  "{{host}} is reserved for a service of project \"{{project}}\"",
  "{{host}} is reserved for Envoryx",
  "{{host}} is reserved for the database container",
  "{{host}} is reserved for the object storage container",
  "{{host}} is the default host name of project \"{{project}}\"",
  "hostname {{host}} is already used",
  "a worker named \"{{name}}\" already exists",
  "{{disk}} has {{free}} free, the operation needs about {{needed}} plus a {{reserve}} reserve",
  "this token has {{scope}} scope, the operation needs {{needed}}",
  "your role allows at most {{scope}} tokens",
  "network {{network}} is still used by {{users}} - disconnect or remove that container first",
  "network {{network}} is still used by {{users}} - disconnect or remove these containers first",
  "\"{{name}}\" is the project's primary database; remove the database service instead",
  "refusing to delete {{path}}",
  "preset {{preset}} takes no argument",
  "PHP {{version}} does not ship {{extension}} yet; switch it off or choose another PHP version",
  "PHP {{version}} does not ship {{extensions}} yet; switch them off or choose another PHP version",
  "the {{preset}} preset runs from the {{image}} image - this project has no {{runtime}} service",
  "duplicate environment variable {{name}}",
  "environment variable name \"{{name}}\" must match [A-Z_][A-Z0-9_]*",
  "path segment \"{{segment}}\" contains unsupported characters",
  "path may have at most {{max}} segments",
  "slug \"{{slug}}\" must be 1-40 lower-case letters, digits or hyphens and start/end alphanumeric",
  "project name \"{{name}}\" does not yield a usable identifier",
  "image choice must be \"{{a}}\" or \"{{b}}\"",
  "upload exceeds {{bytes}} bytes",
  "archive entry \"{{entry}}\" escapes the project directory",
  "expected running but observed {{state}}",
  "{{service}} is rolled back to the previous image but the container runs another one; restart to apply",
  "{{service}} container uses image {{image}} but {{configured}} is configured; restart to apply",
  "a newer {{service}} image was pulled; restart to apply",
  "a newer {{service}} image is available; restart to apply",
  "{{name}} {{version}} is a preview version, which this instance's plan doesn't include; switch to a released version",
  "{{name}} {{version}} has reached its end of life; this instance's plan includes supported versions only",
  "{{name}} {{version}} is a preview version; this instance's plan includes released versions only",
  "{{name}} is not available on this instance",
  "this instance's plan runs at most {{count}} projects at the same time; stop another project first",
  ["this instance's plan runs at most {{count}} project at the same time; stop another project first", "this instance's plan runs at most {{count}} projects at the same time; stop another project first"],
  "this instance's plan allows at most {{cpus}} CPU cores per container",
  "this instance's plan allows at most {{max}} MiB of memory per container",
  "{{name}} {{version}} has reached its end of life and gets no security fixes; switch to a newer version",
  "the {{service}} container runs with an older setup; restart the project to update it",
  "container for removed service \"{{service}}\" still exists",
  "{{lifecycle}} was interrupted by an Envoryx restart; review the project and retry or delete it",
  "{{container}} ran out of memory {{times}} times, last at {{time}} (limit {{limit}} MiB); processes were killed",
  "{{container}} ran out of memory at {{time}} (limit {{limit}} MiB); a process was killed",
  "{{container}} ran out of memory {{times}} times, last at {{time}}; processes were killed",
  "{{container}} ran out of memory at {{time}}; a process was killed",
  "the health check {{path}} has failed since {{time}}: {{reason}}",
  "HTTP {{got}} instead of {{want}} (redirect to {{location}})",
  "HTTP {{got}} instead of {{want}}",
  "no answer within {{seconds}} s",
  "the interval must be {{min}} to {{max}} seconds",
  "the timeout must be 1 to {{max}} seconds",
  "the failures before an alarm must be 1 to {{max}}",
  "\"{{name}}\" is not a model name (e.g. llama3.2 or qwen3:8b)",
  "{{model}} is already being downloaded",
  "no download of {{model}} is running",
  "{{host}} is the container itself; a server on the Docker host is reached as host.docker.internal",
  "{{host}} can't be a server's address (link-local, multicast or unspecified)",
  "cannot connect to {{type}} at {{address}} as {{user}}",
  "{{type}} at {{address}} has no database \"{{name}}\" that {{user}} can see",
  "cannot connect to Redis at {{address}}",
  "cannot use the external database at {{address}}",
  "{{service}} runs in a container of the project; remove it first to connect an external server instead",
  "port {{port}} is out of range",
  "{{file}} connects to an external server, and its password is never in the file; create the project without the manifest and add the connection in Envoryx",
  "cannot start {{version}} on Linux kernel {{kernel}} and newer (this host runs {{host}}); switch to {{other}}",
  "cannot start {{version}} on Linux kernel {{kernel}} and newer (this host runs {{host}}); upgrade the database to {{other}}, which takes over its data",
  // Before the plain one below, which would swallow it (both end in a parenthesis).
  "cannot start {{version}} on Linux kernel {{kernel}} and newer (this host runs {{host}}), and {{other}} cannot take over its data; export it on a host with an older kernel, or remove and re-add the database (its data is lost)",
  "cannot start {{version}} on Linux kernel {{kernel}} and newer (this host runs {{host}})",
  "the backup holds a copy of {{type}} {{from}} data, which {{service}} {{to}} cannot open",
  "{{file}} needs {{wanted}} but the project runs {{have}}, so the build or the start fails; select a newer version in the settings or lower the requirement in the file",
  "{{file}} targets {{wanted}} but the project runs {{have}}, which does not include it, so the build or the start fails; select that version in the settings, or raise it in the file and restart the project",
  "this instance has reached the plan's project limit ({{max}})",
  "restoring the backup of {{date}} was interrupted by an Envoryx restart, so the project holds only part of it; restore the backup again",
  "the containers of this project carry the instance ID {{id}}, not this instance's; if this Envoryx lost its /config, write that ID into /config/instance-id and restart Envoryx",
  "the archive is incomplete or damaged (cut off during the upload?): {{error}}",
  "the archive is incomplete (cut off during the upload?): {{name}} is missing",
  "this instance has reached the plan's user limit ({{max}}); disable a user first",
  "the projects take {{used}} of the {{limit}} disk space in the plan; free some space (old backups, unused projects) first",
  "the runtime {{runtime}} is not available on this instance; the plan includes {{list}}",
  "the setting {{key}} is fixed by the hoster",
  "a project named \"{{name}}\" exists already",
  "a project named \"{{name}}\" already exists",
  "the identifier \"{{slug}}\" belongs to project \"{{project}}\" already",
  "the directory \"{{path}}\" belongs to project \"{{project}}\" already",
  "port {{port}} belongs to project \"{{project}}\" already",
  "{{path}} already exists and is not empty",
  // Why an action can't run (ActionsTab), after "the {{service}} container is not running".
  "{{file}} not found in project",
  "{{service}} container is not running",
  "{{service}} container keeps restarting - its main process exits, the logs say why; switch its server off to run commands in it",
  "several solution files at the top of the project ({{list}}) - keep one, or run dotnet with the one you want in the terminal",
  "found {{n}} web or worker projects - choose the server's project in the .NET settings, or run dotnet ef with --startup-project in the terminal",
  "several projects hold EF Core migrations ({{list}}) - run dotnet ef with --project in the terminal",
  // Why a test suite can't run (TestsTab).
  "{{n}} test projects and no solution file at the top of the project - add one (dotnet new sln, dotnet sln add)",
  "{{file}} not found - run {{command}}",
  // Project status warnings (venv.go, nodedeps.go); the {{reason}} names the way out.
  "the virtual environment was built for Python {{built}} but the container runs {{running}} - its packages are invisible to the new interpreter, so the application server and the workers wait; {{reason}}",
  "the virtual environment is broken (its python links to itself), so the application server and the workers wait; {{reason}}",
  "the virtual environment is broken (an install in it was interrupted), so its packages may fail to import; {{reason}}",
  "node_modules is missing - the dev server runs \"{{install}}\" before it starts; if that fails, it waits until you run \"{{manager}} install\" from Actions",
  // A service's trouble in the project status (status.go serviceProblem).
  "{{service}} keeps restarting - its main process exits shortly after each start; its logs say why",
  "the {{service}} server waits because bundle install failed - its logs show the error; it tries again every 30 seconds",
  "the {{service}} dev server waits for node_modules - run npm install (or pnpm / yarn install) from Actions",
  "the {{service}} server waits for go.mod - scaffold with a Go template, clone a repository or run go mod init in the Go terminal",
  "the {{service}} server waits because .venv was built for another Python version or its python does not run - run pip install -r requirements.txt or uv sync from Actions to rebuild it",
  "the {{service}} server waits for {{module}} in .venv - run pip install -r requirements.txt or uv sync from Actions",
  "the {{service}} server exited while an action was running - it starts again once the action has finished; its logs say why it exited",
  // Details of the diagnostics checks (DiagnosticsTab). The answered-by-the-proxy line
  // comes before the plain one, which would swallow it.
  "Docker {{version}} (API {{api}}) on {{os}}, {{running}} containers running",
  "Envoryx has its own IP ({{address}}) but no host for project links is set; links to published ports (project URLs, Mailpit, object storage console, database ports) point at Envoryx instead of the Docker host.",
  "Links use {{host}}",
  "Reachable directly at {{address}} (ports 80/443)",
  "Published on host ports {{http}} (HTTP) and {{https}} (HTTPS)",
  "*.{{domain}} does not resolve from inside the Envoryx container (its DNS: {{dns}}). Your devices may use a different DNS server - the check from this browser above is what counts.",
  "*.{{domain}} → {{addresses}}, answered by this Envoryx's proxy (DNS: {{dns}})",
  "*.{{domain}} → {{addresses}}, set up by your hoster (DNS: {{dns}})",
  "*.{{domain}} does not resolve (DNS: {{dns}}). Your hoster sets up the DNS of this base domain.",
  "*.{{domain}} → {{address}} (DNS: {{dns}})",
  "*.{{domain}} resolves to {{addresses}}, but the proxy is at {{proxy}} (DNS: {{dns}}).",
  "*.{{domain}} resolves to {{addresses}}, but another server answers there, not this Envoryx (DNS: {{dns}}).",
  "*.{{domain}} resolves to {{addresses}} (DNS: {{dns}}), unverified: Envoryx could not reach its proxy there, and without a host for project links it has no address to compare.",
  "Port {{port}}, fingerprint {{fingerprint}}",
  "{{version}} - update check disabled",
  "{{version}} - the release check has not succeeded yet: {{error}}",
  "{{version}} is the newest release",
  "{{version}} (development build)",
  "{{provider}}: last delivery failed - {{error}}",
  "{{provider}}, last sent {{time}}",
  // A refused request (the 403 "no access" answers).
  "your role does not allow this (it needs {{scope}} access)",
  // Image pull progress, the {{status}} of the "Pulling the image" step.
  "downloading {{percent}}% ({{done}} of {{total}})",
  "downloading ({{size}} so far, {{done}} of {{total}} layers done)",
  "extracting ({{done}} of {{total}} layers done)",
  // Projects, copies, renames and their conflicts.
  "the directory \"{{path}}\" is already used by project \"{{project}}\"",
  "{{project}} is still being set up and can't be copied yet",
  "{{project}} is being deleted and can't be copied",
  "{{project}} didn't finish setting up and can't be copied",
  "{{project}} is still being set up and can't be renamed yet",
  "{{project}} is being deleted and can't be renamed",
  "{{project}} didn't finish setting up and can't be renamed",
  "the source runs {{source}}, the target {{target}}",
  "the backup directory {{path}} already exists",
  "service {{service}} already exists",
  "environment variable {{name}} already exists",
  "network {{network}} is still used by {{users}}",
  "every /{{bits}} of the network pool {{pool}} is in use; delete projects you no longer need or give ENVORYX_NETWORK_POOL a larger range",
  "{{port}} is taken by another program or container on the Docker host - stop it or give the service another port ({{error}})",
  // Branch environments, git, guest links and shares.
  "at most {{max}} automatic branch environments",
  "no free identifier for the branch \"{{branch}}\"",
  "{{project}} is a branch environment itself; create the environment from its parent",
  "{{project}} has no git checkout to start from; clone the repository first",
  "the branch {{branch}} already has the environment {{project}}",
  "{{project}} is not a branch environment",
  "\"{{command}}\" exited with {{code}}",
  "invalid branch pattern \"{{pattern}}\"",
  "at most {{max}} branch patterns",
  "at most {{max}} deploy commands",
  "{{project}} still has branch environments ({{list}}); delete them first",
  "could not point the checkout's origin at the new URL: {{error}}",
  "a project has at most {{max}} guest links; revoke one first",
  "the tunnel did not come up: {{error}}",
  // Databases, dumps and snapshots.
  "database name \"{{name}}\": 1-24 lowercase letters, digits and dashes, starting with a letter",
  "\"{{name}}\" is taken by another container of the project; choose another database name",
  "\"{{name}}\" is a system database",
  "two databases are named \"{{name}}\"",
  "the project has no database \"{{name}}\"",
  "upgrade refused: the database must be backed up first and that failed ({{error}})",
  "this snapshot holds no dump of {{database}}",
  "{{project}} has no database {{database}} to copy",
  "{{source}} runs {{sourceType}}, {{target}} runs {{targetType}} - a dump of one is not a dump of the other",
  "clone refused: the database of {{project}} could not be snapshotted first ({{error}}); take the snapshot out of the request to clone anyway",
  "dump failed (exit {{code}}): {{error}}",
  "import failed (exit {{code}}): {{error}}",
  // Workers, server settings, PHP settings.
  "invalid goal, task or property \"{{value}}\"",
  "the worker {{name}} is switched off",
  "invalid main package \"{{value}}\" (use . or a path like ./cmd/server)",
  "invalid jar \"{{value}}\" (use a relative path such as target/app.jar)",
  "invalid project \"{{value}}\" (use a relative path such as src/Shop/Shop.csproj)",
  "invalid DLL \"{{value}}\" (use a relative path such as {{dir}}/Worker.dll)",
  "invalid script name \"{{name}}\"",
  "invalid build script name \"{{name}}\"",
  "invalid application path \"{{value}}\" (use module:attribute, e.g. app:app)",
  "{{runtime}} version \"{{version}}\" is not supported",
  "invalid size value \"{{value}}\" (use e.g. 256M)",
  "unknown PHP extension \"{{name}}\"",
  "PHP extension \"{{name}}\" is not available in this Envoryx version",
  // Cron jobs; a {{field}} (minute, hour, ...) is translated on its own.
  "\"{{expression}}\" never fires",
  "empty entry in the {{field}} field \"{{text}}\"",
  "step \"{{step}}\" in the {{field}} field must be 1-{{max}}",
  "range \"{{range}}\" in the {{field}} field runs backwards",
  "\"{{value}}\" is not a valid {{field}} ({{min}}-{{max}})",
  "unknown macro \"{{macro}}\" (use @hourly, @daily, @weekly, @monthly or @yearly)",
  "expected 5 fields (minute hour day-of-month month day-of-week), got {{number}}",
  "the job runs in the {{container}} container - this project has no {{service}} service",
  "at most {{max}} cron jobs per project",
  "this project has no {{service}} service",
  "the command must be 1-{{max}} characters",
  "a cron job named \"{{name}}\" already exists",
  "cron job \"{{name}}\"",
  // Resource limits and histories.
  "CPU limit of the application containers is {{cpus}} cores, the host has {{host}}",
  "CPU limit of the services is {{cpus}} cores, the host has {{host}}",
  "memory limit of the application containers must be at least {{min}} MiB",
  "memory limit of the services must be at least {{min}} MiB",
  "memory limit of the application containers is {{memory}} MiB, the host has {{host}} MiB",
  "memory limit of the services is {{memory}} MiB, the host has {{host}} MiB",
  "the process limit must be between {{min}} and {{max}}",
  "keep the log history for 1 to {{max}} days",
  "the log history may use {{min}} MB to {{max}} MB",
  "keep the resource history for 1 to {{max}} days",
  "keep the audit log for {{min}} to {{max}} days, or 0 for all of it",
  "folder name must be at most {{max}} characters without control characters",
  "{{suite}} takes no filter",
  // Services of a project.
  "{{service}} keeps no data",
  "{{service}} is part of the project; remove it with its data instead",
  "no kept {{service}} data",
  "the request to Ollama failed (exit {{code}}): {{error}}",
  // Website uploads, backups, restores, secret keys, offsite targets.
  "archive entry \"{{entry}}\" escapes the archive",
  "the archive unpacks to more than {{size}} GiB",
  "the upload exceeds {{size}} GiB",
  "the dump comes from {{source}} and cannot be imported into {{target}}",
  "an SQL dump cannot be imported into {{database}}",
  "the dump could not be imported (exit {{code}}): {{error}}",
  "the project has none of the databases this backup holds ({{list}})",
  "the dump is for {{dump}} but the database is {{database}}",
  "the backup belongs to project \"{{backup}}\", not \"{{project}}\"",
  "a backup directory {{name}} exists already",
  "the hoster's support can't restore this backup: {{reason}}; the instance's admins can",
  "the backup's project settings are sealed with secret key {{key}}, which this Envoryx does not hold (a key replaced by a rotation, or the key of another Envoryx); to use this backup, run Envoryx with that key (ENVORYX_SECRET_KEY or /config/secret.key)",
  "the backup's project runs {{name}} {{version}}, which this Envoryx no longer offers",
  "the backup's project runs {{service}} on a custom image ({{image}})",
  "the backup's project uses the addon {{name}}, which is not installed here in the same version",
  "the backup's secrets are encrypted with key {{key}}; give that key to restore it",
  "that is key {{key}}, the backup needs key {{needed}}",
  "not an Envoryx instance backup (first entry is \"{{entry}}\")",
  "unsupported backup format {{format}}",
  "the key comes from {{env}}; set a new key there and the old one as {{oldEnv}} for one start",
  "not a key: expected {{bytes}} bytes as base64 or 64 hex digits (create one with: openssl rand -base64 32)",
  "a key has {{got}} bytes, not {{want}}",
  "the target {{name}} is switched off",
  "\"{{key}}\" is not a backup on this target",
  "expected {{expected}}, got {{got}} - if the server was replaced on purpose, clear the pinned key in the target",
  // Addons and the project manifest.
  "the addon file is larger than {{size}} KB",
  "addon name \"{{name}}\": 2 to 30 lower-case letters, digits and single dashes, starting with a letter",
  "addon name \"{{name}}\" is taken by Envoryx",
  "addon {{name}} has no version \"{{version}}\"",
  "\"{{url}}\" is not an http(s) URL",
  "download {{url}}",
  "the addon {{name}} is not installed",
  "{{name}} is used by {{projects}}; remove it from those projects first",
  "{{name}} is not installed on this Envoryx (Settings, Addons)",
  "the project directory has no {{file}}",
  "healthcheck.{{key}} \"{{value}}\" is not a duration in whole seconds like 30s or 2m",
  "memory \"{{value}}\" is not a size like 512M or 2G",
  "timeout \"{{value}}\" is not a duration like 10m or 1h",
  "{{file}} is larger than {{size}} KiB",
  "{{file}} holds more than one document",
  "the project needs a name - set name in {{file}} or pass one",
  "{{name}} is not declared under secrets in {{file}}",
  "the package cache holds nothing of \"{{name}}\"",
  // Custom images and what their probe finds missing; a {{feature}} is translated on its own.
  "\"{{ref}}\" is not an image reference (like ghcr.io/acme/php:8.4)",
  "{{registry}} images are Envoryx's own",
  "the build context, the whole project directory, is {{reason}}: put the Dockerfile in a directory of its own, like .envoryx/, with just the files the image needs",
  "the build context {{dir}}/ (the Dockerfile's directory) is {{reason}}: keep only the files the image needs in it",
  "too large (more than {{files}} files or {{size}} MB)",
  "the {{service}} service does not build a Dockerfile",
  // Before the plain one, which would swallow it (both end in a parenthesis).
  "the image {{image}} could not be pulled; if the registry is private, add a login under Settings, Private registries ({{error}})",
  "the image {{image}} could not be pulled; the registry refused the stored login for {{host}}, check it under Settings, Private registries ({{error}})",
  "{{host}} refused the login for {{user}}; check the username and the password or token",
  "the image {{image}} could not be pulled ({{error}})",
  "\"{{host}}\" is not a registry host (like ghcr.io or registry.example.com:5000)",
  "{{host}} is listed twice",
  "the login for {{host}} needs a username",
  "the login for {{host}} needs a password or token",
  "the Dockerfile {{path}} does not exist",
  "build the {{service}} image from {{path}}",
  "the image is based on Envoryx's {{runtime}} image, not the {{kind}} one",
  "the image's ENTRYPOINT {{entrypoint}} runs before every command Envoryx starts; it must end with exec \"$@\"",
  "{{bin}} is missing; Envoryx needs it",
  "{{bin}} is missing: no {{feature}}",
  // Sign-in, single sign-on, passkeys, SSH keys.
  "the Envoryx account {{name}} is connected to another single sign-on account already",
  "the provider's {{claim}} claim \"{{value}}\" is not a usable username",
  "an Envoryx account named {{name}} exists already; to connect it to single sign-on, open its invitation link from an admin and sign in from there",
  "there is no Envoryx account for {{name}}; ask an admin for an invitation",
  "{{name}} is in none of the groups that may use Envoryx",
  "reach the OpenID Connect provider {{issuer}}",
  "the name is longer than {{max}} characters",
  "a passkey's name has 1 to {{max}} characters",
  "none of this account's passkeys was registered at {{host}}",
  "line {{line}} is not a valid public key",
  "line {{line}} is not a public key",
  "username \"{{name}}\"",
  "the username \"{{name}}\" is already taken",
  // Diagnostics details.
  "Docker {{version}} (API {{api}}) on {{os}}, {{running}} containers running - host {{host}}",
  "{{latest}} available, running {{current}}",
  "{{number}} orphaned Docker resources of this instance belong to no project",
  "{{count}} projects consistent with Docker (checked {{time}})",
  ["{{count}} project consistent with Docker (checked {{time}})", "{{count}} projects consistent with Docker (checked {{time}})"],
  "{{number}} Docker resources are from another instance or from before 0.18",
  "releases endpoint answered {{status}}",
  // Let's Encrypt and the DNS providers (after the provider's name: "cloudflare: token rejected ...");
  // a {{label}} is a field of the provider form, translated on its own.
  "{{label}} is required for {{provider}}",
  "token rejected ({{error}}); it needs Zone:Read and Zone:DNS:Edit for the zone",
  "no zone found for {{domain}} (is the domain on this account and does the token allow Zone:Read?)",
  "token rejected ({{error}}); it needs Read & Write in the project that holds the zone",
  "no zone found for {{domain}} in the project of this token",
  "token rejected ({{error}}); it needs the domain scopes",
  "no domain found for {{domain}} on this account",
  "no domain found for {{domain}} on this customer account",
  "no domain found for {{domain}} (is API access switched on for it?)",
  "access denied ({{error}}); the IAM user needs the Route 53 permissions listed in the settings",
  "no public hosted zone found for {{domain}}",
  "no dns-01 challenge offered for {{domain}}",
  "TXT record {{record}} did not become visible at the zone's name servers within {{duration}}",
  "unexpected response ({{status}})",
  // The fleet manager connection (PlanCard).
  "the fleet manager refused the enrollment (HTTP {{status}}): {{error}}",
  "the fleet manager's address must be an https address, got \"{{value}}\"",
  "the fleet manager's address \"{{value}}\" must use https: over plain http anyone on the network can pose as the manager (set ENVORYX_FLEET_INSECURE=1 to allow it anyway)",
  "{{path}} is damaged; remove it and enroll again with a new token",
  // Warnings before a project is created (NewProjectPage) and the image update check.
  "the dev server runs \"{{command}}\" but nothing creates a package.json - pick a Node template, clone a repository or scaffold from the Node terminal; until then the container waits",
  "the application server runs \"{{command}}\" but nothing creates the application - pick a Python template, clone a repository or scaffold from the Python terminal; until then the container waits",
  "the server builds {{package}} but nothing creates a go.mod - pick a Go template, clone a repository or run go mod init in the Go terminal; until then the container waits",
  "the server runs \"{{command}}\" but nothing creates the application - pick a Ruby template, clone a repository or scaffold from the Ruby terminal; until then the container waits",
  "the {{preset}} server waits for a pom.xml or build.gradle but nothing creates one - pick a Java template, clone a repository or scaffold from the Java terminal; until then the container waits",
  "image {{image}} will be pulled on first start",
  "{{failed}} of {{total}} images could not be checked ({{list}}): {{error}}",
  // Notes Envoryx gives the backups it takes itself (backupNote).
  "before restoring {{id}}",
  "before schema {{from}} → {{to}} (Envoryx {{version}})",
  // Wide patterns last, so the specific ones above win.
  "{{path}} already exists",
  "{{name}} is still running",
  "{{file}} is empty",
  "{{label}} is too long",
  "{{path}}: {{free}} free of {{total}}",
  "container {{name}}",
  "network {{name}}",
  "volume {{name}}",
];

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
// Only an {{error}} or {{reason}} (a wrapped backend message) may span ": ". Any other
// value stops there, so a template doesn't swallow the context Go put in front of it
// ("not included in the plan: PHP 8.6 is a preview version; ...").
const compiled = templates.map((entry) => {
  const [pattern, tpl] = typeof entry === "string" ? [entry, entry] : entry;
  return {
    tpl,
    re: new RegExp("^" + escape(pattern).replace(/\\\{\\\{(\w+)\\\}\\\}/g, (_, name: string) => (name === "error" || name === "reason" ? `(?<${name}>.+?)` : `(?<${name}>(?:(?!: ).)+?)`)) + "$"),
  };
});

/**
 * A time of day the server wrote in UTC ("16:02 UTC", a warning's "since" or "at") as the
 * browser's time, like the rest of the UI shows times. It lies in the past, so a clock time
 * later than now was yesterday. Times in another zone keep their name.
 */
export function localClock(s: string, now = new Date()): string {
  const m = /^(\d{1,2}):(\d{2}) UTC$/.exec(s);
  if (!m) return s;
  const at = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), Number(m[1]), Number(m[2])));
  if (at.getTime() > now.getTime() + 60_000) at.setUTCDate(at.getUTCDate() - 1);
  return at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function translateSegment(s: string, t: TFunction): string | undefined {
  if (i18n.exists(s)) return t(s);
  for (const { tpl, re } of compiled) {
    const m = re.exec(s);
    if (m) {
      const groups: Record<string, string> = { ...m.groups };
      // A {{reason}} is itself a backend message.
      if (groups.reason) groups.reason = translateSegment(groups.reason, t) ?? groups.reason;
      // A {{label}}, {{feature}} or {{field}} is a fixed phrase of the server's ("API token",
      // "live reload", "day of month"); anything else (names, paths) stays as it is.
      for (const name of ["label", "feature", "field"]) {
        if (groups[name] && i18n.exists(groups[name])) groups[name] = t(groups[name]);
      }
      if (groups.time) groups.time = localClock(groups.time);
      // Sizes the server wrote ("533.8 GiB") get the language's decimal separator.
      for (const [name, value] of Object.entries(groups)) {
        const size = /^(\d+)(?:\.(\d+))? ([KMGT]iB|B)$/.exec(value);
        if (size) groups[name] = `${formatNumber(Number(`${size[1]}.${size[2] ?? 0}`), size[2]?.length ?? 0)} ${size[3]}`;
      }
      return t(tpl, { ...groups, ...(groups.count ? { count: Number(groups.count) } : {}) } as Record<string, unknown>);
    }
  }
  return undefined;
}

/**
 * Translates a message the backend produced. Go wraps errors as "context: cause", so when
 * the whole text is unknown each segment is tried on its own; unknown parts stay English.
 */
export function translateMessage(msg: string | undefined | null, t: TFunction): string {
  if (!msg) return "";
  const whole = translateSegment(msg, t);
  if (whole !== undefined) return whole;
  // A message may itself contain ": " ("database name "x": 1-24 lowercase letters ..."), so
  // from each position the longest run of segments that is one known message is taken.
  const split = msg.split(": ");
  const parts: { p: string; tr: string | undefined }[] = [];
  for (let i = 0; i < split.length; ) {
    let j = split.length;
    let tr: string | undefined;
    for (; j > i + 1; j--) {
      tr = translateSegment(split.slice(i, j).join(": "), t);
      if (tr !== undefined) break;
    }
    if (j === i + 1) tr = translateSegment(split[i]!, t);
    parts.push({ p: split.slice(i, j).join(": "), tr });
    i = j;
  }
  // The bare kind of error ("conflict: …", "…: not found") adds nothing to the message that
  // explains it. Next to a part that stays English it would only glue a translated word to
  // an English sentence ("Konflikt: a project named …"), so it is left out then.
  // In English nothing is glued together, so the message stays whole, prefix and all, as it
  // does in the other languages.
  const mixed = !english() && parts.some(({ p, tr }) => tr === undefined && !sentinels.has(p));
  return parts
    .filter(({ p }) => !mixed || !sentinels.has(p))
    .map(({ p, tr }) => tr ?? p)
    .join(": ");
}

const sentinels = new Set(["conflict", "not found", "invalid input"]);

/**
 * A backup's note: the ones Envoryx writes itself ("scheduled daily", "daily, for the
 * offsite targets") in the UI language, a note someone typed as it is. Only a whole note is
 * looked up, so a typed one is never translated in parts.
 */
export function backupNote(note: string, t: TFunction): string {
  return translateSegment(note, t) ?? note;
}

const english = () => (i18n.resolvedLanguage ?? i18n.language ?? "en").startsWith("en");

/**
 * A translated server message shown on its own, as a sentence: with a capital first letter.
 * The translations of the server's messages start in lower case like the English originals,
 * so they also fit after a colon ("failed: {{error}}"); English keeps the server's wording.
 */
export function sentence(text: string, original?: string): string {
  if (english() || !text) return text;
  // Only a plain word: a value at the start ("composer.json …", "php8.4 …") keeps its case,
  // and so does a word the translation took over from the server's text unchanged.
  const first = /^\p{Ll}\p{L}*(?=[\s,;:]|$)/u.exec(text)?.[0];
  if (!first || (original !== undefined && /^\S+/.exec(original)?.[0] === first)) return text;
  return first.charAt(0).toLocaleUpperCase(i18n.resolvedLanguage) + text.slice(1);
}

/** translateMessage for a message shown on its own: as a sentence. */
export function translateSentence(msg: string | undefined | null, t: TFunction): string {
  return sentence(translateMessage(msg, t), msg ?? "");
}


/**
 * Like errorText, without the kind of error the server puts in front ("invalid input: that
 * is key …"): for dialogs where the message alone says what to fix.
 */
export function reasonText(err: unknown, t: TFunction, fallback?: string): string {
  if (err instanceof ApiError) return translateMessage(err.message.replace(/^(?:invalid input|conflict|not found): /, ""), t);
  return fallback ?? t("The server could not be reached.");
}

/**
 * Text for an error caught from the API client: the server's message, translated, or the
 * fallback when the request never reached the server.
 */
export function errorText(err: unknown, t: TFunction, fallback?: string): string {
  if (err instanceof ApiError) return translateSentence(err.message, t);
  return fallback ?? t("The server could not be reached.");
}
