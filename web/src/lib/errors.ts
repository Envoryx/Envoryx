import type { TFunction } from "i18next";
import i18n from "@/i18n";
import { ApiError } from "@/api/client";

/**
 * Messages the backend produces with values embedded ("project has no web service").
 * Each template is also the German dictionary key; the placeholders become capture groups
 * so the value is carried over into the translation.
 */
const templates = [
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
  "git clone failed: {{error}}",
  "git {{command}} failed: {{error}}",
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
  "node_modules is missing - the dev server runs \"{{install}}\" before it starts; if that fails, it waits until you run \"npm install\" (or pnpm / yarn install) from Actions",
  // A service's trouble in the project status (status.go serviceProblem).
  "{{service}} keeps restarting - its main process exits shortly after each start; its logs say why",
  "the {{service}} server waits because bundle install failed - its logs show the error; it tries again every 30 seconds",
  "the {{service}} dev server waits for node_modules - run npm install (or pnpm / yarn install) from Actions",
  "the {{service}} server waits because .venv was built for another Python version - run pip install -r requirements.txt or uv sync from Actions to rebuild it",
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
];

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
// Only an {{error}} or {{reason}} (a wrapped backend message) may span ": ". Any other
// value stops there, so a template doesn't swallow the context Go put in front of it
// ("not included in the plan: PHP 8.6 is a preview version; ...").
const compiled = templates.map((tpl) => ({
  tpl,
  re: new RegExp("^" + escape(tpl).replace(/\\\{\\\{(\w+)\\\}\\\}/g, (_, name: string) => (name === "error" || name === "reason" ? `(?<${name}>.+?)` : `(?<${name}>(?:(?!: ).)+?)`)) + "$"),
}));

function translateSegment(s: string, t: TFunction): string | undefined {
  if (i18n.exists(s)) return t(s);
  for (const { tpl, re } of compiled) {
    const m = re.exec(s);
    if (m) {
      const groups: Record<string, string> = { ...m.groups };
      // A {{reason}} is itself a backend message.
      if (groups.reason) groups.reason = translateSegment(groups.reason, t) ?? groups.reason;
      return t(tpl, groups);
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
  const parts = msg.split(": ").map((p) => ({ p, tr: translateSegment(p, t) }));
  // The bare kind of error ("conflict: …", "…: not found") adds nothing to the message that
  // explains it. Next to a part that stays English it would only glue a translated word to
  // an English sentence ("Konflikt: a project named …"), so it is left out then.
  const mixed = parts.some(({ p, tr }) => tr === undefined && !sentinels.has(p));
  return parts
    .filter(({ p }) => !mixed || !sentinels.has(p))
    .map(({ p, tr }) => tr ?? p)
    .join(": ");
}

const sentinels = new Set(["conflict", "not found", "invalid input"]);


/**
 * Text for an error caught from the API client: the server's message, translated, or the
 * fallback when the request never reached the server.
 */
export function errorText(err: unknown, t: TFunction, fallback?: string): string {
  if (err instanceof ApiError) return translateMessage(err.message, t);
  return fallback ?? t("The server could not be reached.");
}
