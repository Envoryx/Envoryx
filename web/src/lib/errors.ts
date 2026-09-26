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
  "network {{network}} is still used by {{users}} – disconnect or remove that container first",
  "network {{network}} is still used by {{users}} – disconnect or remove these containers first",
  "removing {{service}} deletes its data volume; confirm with removeData",
  "\"{{name}}\" is the project's primary database; remove the database service instead",
  "refusing to delete {{path}}",
  "preset {{preset}} takes no argument",
  "the {{preset}} preset runs from the {{image}} image – this project has no {{runtime}} service",
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
];

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const compiled = templates.map((tpl) => ({
  tpl,
  re: new RegExp("^" + escape(tpl).replace(/\\\{\\\{(\w+)\\\}\\\}/g, "(?<$1>.+?)") + "$"),
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
  const parts = msg.split(": ");
  return parts.length > 1 ? parts.map((p) => translateSegment(p, t) ?? p).join(": ") : msg;
}

/**
 * Text for an error caught from the API client: the server's message, translated, or the
 * fallback when the request never reached the server.
 */
export function errorText(err: unknown, t: TFunction, fallback?: string): string {
  if (err instanceof ApiError) return translateMessage(err.message, t);
  return fallback ?? t("The server could not be reached.");
}
