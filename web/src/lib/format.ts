import type { TFunction } from "i18next";
import type { ProjectState, UpdateStatus } from "@/api/types";
import type { Tone } from "@/components/ui";
import i18n, { currentLanguage } from "@/i18n";

// Letters with accents and Cyrillic ones spelled in a-z, the same table as the server's
// validate.Slugify: each group of lower-case letters maps to one spelling.
const transliterations: Record<string, string> = Object.fromEntries(
  Object.entries({
    àáâãåāăą: "a", äæ: "ae", çćĉċč: "c", ďđð: "d", èéêëēĕėęě: "e",
    ĝğġģ: "g", ĥħ: "h", ìíîïĩīĭįı: "i", ĳ: "ij", ĵ: "j", ķ: "k",
    ĺļľŀł: "l", ñńņňŋ: "n", òóôõōŏő: "o", öøœ: "oe", ŕŗř: "r",
    śŝşšș: "s", ß: "ss", ťţțŧ: "t", þ: "th", ùúûũūŭůűų: "u", ü: "ue",
    ŵ: "w", ýÿŷ: "y", źżž: "z",
    а: "a", б: "b", в: "v", гґ: "g", д: "d", еёэ: "e", є: "ye", ж: "zh",
    з: "z", иі: "i", ї: "yi", йы: "y", к: "k", л: "l", м: "m", н: "n",
    о: "o", п: "p", р: "r", с: "s", т: "t", уў: "u", ф: "f", х: "kh",
    ц: "ts", ч: "ch", ш: "sh", щ: "shch", ю: "yu", я: "ya",
  }).flatMap(([letters, spelling]) => [...letters].map((l) => [l, spelling])),
);

/**
 * The identifier derived from a project name, mirroring the server's Slugify: lower case,
 * letters with accents and Cyrillic ones spelled in a-z, separators become hyphens and
 * anything else is dropped. It is a preview; the server has the last word.
 */
export function slugify(name: string): string {
  let out = "";
  let lastHyphen = true;
  for (const ch of name.trim().toLowerCase()) {
    if (/[a-z0-9]/.test(ch)) {
      out += ch;
      lastHyphen = false;
    } else if (" -_./".includes(ch)) {
      if (!lastHyphen) out += "-";
      lastHyphen = true;
    } else if (Object.hasOwn(transliterations, ch)) {
      out += transliterations[ch];
      lastHyphen = false;
    }
  }
  out = out.replace(/^-+|-+$/g, "");
  return out.length > 40 ? out.slice(0, 40).replace(/-+$/, "") : out;
}

const numberFormats = new Map<string, Intl.NumberFormat>();

/**
 * A number as the UI language writes it (3,4 in German, 3.4 in English); `digits` fixes
 * the decimals, other options go to Intl.NumberFormat as they are.
 */
export function formatNumber(v: number, digits?: number | Intl.NumberFormatOptions): string {
  const opts = typeof digits === "number" ? { minimumFractionDigits: digits, maximumFractionDigits: digits } : (digits ?? {});
  const lang = currentLanguage();
  const key = lang + JSON.stringify(opts);
  let f = numberFormats.get(key);
  if (!f) {
    f = new Intl.NumberFormat(lang, opts);
    numberFormats.set(key, f);
  }
  return f.format(v);
}

/** Sizes in powers of 1024, named as such (GiB, not GB), as Diagnostics and the server do. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${formatNumber(v, v < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

export function formatPercent(v: number): string {
  if (!Number.isFinite(v)) return "0 %";
  return `${formatNumber(v, v < 10 ? 1 : 0)} %`;
}

/**
 * A duration the server wrote in Go's form ("12h0m0s", "1h30m0s") in words: whole days as
 * days ("168h0m0s" → "7 days"), else hours and minutes. Anything else stays as it is.
 */
export function formatGoDuration(s: string, t: TFunction): string {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(s);
  if (!s || !m || Number(m[3] ?? 0) !== 0) return s;
  const hours = Number(m[1] ?? 0);
  const minutes = Number(m[2] ?? 0);
  if (minutes === 0 && hours > 0 && hours % 24 === 0) return t("{{count}} days", { count: hours / 24 });
  const parts = [hours ? t("{{count}} hours", { count: hours }) : "", minutes ? t("{{count}} minutes", { count: minutes }) : ""].filter(Boolean);
  return parts.length ? parts.join(" ") : s;
}

export function formatRelative(iso: string, t: TFunction): string {
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return "";
  const diff = Date.now() - at;
  const s = Math.round(diff / 1000);
  if (s < 60) return t("just now");
  const m = Math.round(s / 60);
  if (m < 60) return t("{{count}} min ago", { count: m });
  const h = Math.round(m / 60);
  if (h < 24) return t("{{count}} h ago", { count: h });
  const d = Math.round(h / 24);
  if (d < 30) return t("{{count}} d ago", { count: d });
  return new Date(at).toLocaleDateString();
}

export function formatDateTime(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  return new Date(t).toLocaleString();
}

export const stateMeta: Record<ProjectState, { label: string; tone: Tone; pulse?: boolean }> = {
  running: { label: "Running", tone: "green" },
  stopped: { label: "Stopped", tone: "gray" },
  partial: { label: "Partially running", tone: "amber" },
  missing: { label: "Containers missing", tone: "amber" },
  error: { label: "Error", tone: "red" },
  creating: { label: "Creating", tone: "blue", pulse: true },
  deleting: { label: "Deleting", tone: "blue", pulse: true },
};

export function containerStateTone(state: string): Tone {
  switch (state) {
    case "running":
      return "green";
    case "restarting":
    case "paused":
      return "amber";
    case "dead":
      return "red";
    case "external":
      return "blue";
    default:
      return "gray";
  }
}

/**
 * English labels of the container states Docker and Envoryx report ("missing" for a
 * container that does not exist, "external" for a server Envoryx does not run).
 */
export const containerStateLabels: Record<string, string> = {
  created: "created",
  running: "running",
  paused: "paused",
  restarting: "restarting",
  removing: "removing",
  exited: "exited",
  dead: "dead",
  missing: "missing",
  external: "external",
};

/** A container state as people read it; an unknown one stays as it is. */
export function containerStateLabel(state: string, t: TFunction): string {
  const label = containerStateLabels[state];
  return label ? t(label) : state;
}

/** English labels of a container's health as Docker reports it. */
export const healthLabels: Record<string, string> = { healthy: "healthy", unhealthy: "unhealthy", starting: "starting" };

/** A container's health as people read it; an unknown one stays as it is. */
export function healthLabel(health: string, t: TFunction): string {
  const label = healthLabels[health];
  return label ? t(label) : health;
}

/** English labels of the backup kinds: a project backup's parts, an instance backup's origin. */
export const backupKindLabels: Record<string, string> = {
  full: "full",
  database: "database",
  files: "files",
  storage: "object storage",
  manual: "manual",
  upload: "uploaded",
  "pre-migrate": "before migration",
  "pre-restore": "before restore",
  scheduled: "scheduled",
};

/** A backup's kind as people read it; an unknown one stays as it is. */
export function backupKindLabel(kind: string, t: TFunction): string {
  const label = backupKindLabels[kind];
  return label ? t(label) : kind;
}

/**
 * Builds the URL a project is reachable at. Project ports are published on the Docker host,
 * which is not necessarily the address Envoryx itself is reached at (macvlan IP, reverse
 * proxy), so an explicitly configured host wins over the browser's address bar.
 */
export function projectUrl(port: number, publicHost?: string): string {
  if (!port) return "";
  const host = publicHost?.trim() || window.location.hostname;
  const h = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `http://${h}:${port}`;
}

/** Where a published port is, said so that one only open on the server isn't taken for one on the reader's machine. */
export function reachable(t: TFunction, address: string, local: boolean): string {
  return local ? t("Only on the server itself, at {{address}}", { address }) : t("Reachable at {{address}}", { address });
}

export function serviceLabel(kind: string, version?: string, variant?: string): string {
  const dbNames: Record<string, string> = { mariadb: "MariaDB", mysql: "MySQL", postgresql: "PostgreSQL", mongodb: "MongoDB" };
  const webNames: Record<string, string> = { caddy: "Caddy", apache: "Apache", nginx: "Nginx" };
  // Product names stay as they are; only the generic fallbacks are translated.
  const name: Record<string, string> = { php: "PHP", web: webNames[variant ?? ""] ?? i18n.t("Web server"), node: "Node.js", python: "Python", go: "Go", ruby: "Ruby", java: "Java", dotnet: ".NET", database: dbNames[variant ?? ""] ?? i18n.t("Database"), redis: "Redis", memcached: "Memcached", mailpit: "Mailpit", rabbitmq: "RabbitMQ", meilisearch: "Meilisearch", typesense: "Typesense", opensearch: "OpenSearch", "opensearch-dashboards": "OpenSearch Dashboards", ollama: "Ollama", storage: i18n.t("Object storage") };
  // An additional database: its name, then the engine ("analytics · PostgreSQL 18").
  const extraDB = kind.startsWith("db-") ? `${kind.slice(3)} · ${dbNames[variant ?? ""] ?? i18n.t("Database")}` : undefined;
  const base = extraDB ?? name[kind] ?? kind;
  return version ? `${base} ${version}` : base;
}

/**
 * A project container's short name ("web", "node", "database", "worker-mails") where the UI
 * shows it as a tag. They are names, not words, except "database", which reads in the UI
 * language.
 */
export function containerLabel(name: string): string {
  return name === "database" ? i18n.t("database") : name;
}

/** "0.4.0 · up to date" - the version with the outcome of the update check. */
/**
 * A build's version as people read it: releases without the "v", a build of main as the
 * release it follows, how many commits later and its commit (git describe's
 * v0.10.0-3-g73304d6 → "0.10.0+3 (73304d6)", a * for uncommitted changes), older main-<sha>
 * builds by their short sha.
 */
export function displayVersion(version: string): string {
  const described = /^v?(\d+\.\d+\.\d+)-(\d+)-g([0-9a-f]+)(-dirty)?$/.exec(version);
  if (described) {
    const [, release, ahead, sha, dirty] = described;
    return `${release}${ahead !== "0" ? `+${ahead}` : ""} (${sha!.slice(0, 7)}${dirty ? "*" : ""})`;
  }
  const main = /^main-([0-9a-f]{7,40})$/.exec(version);
  if (main) return `main (${main[1]!.slice(0, 7)})`;
  return version.replace(/^v(?=\d)/, "");
}

export function formatVersion(t: TFunction, raw: string, u?: UpdateStatus): string {
  const version = displayVersion(raw);
  if (!u || !u.enabled) return version;
  if (!u.release) return `${version} · ${t("development build")}`;
  if (u.available && u.latest) return `${version} · ${t("{{latest}} available", { latest: displayVersion(u.latest) })}`;
  if (u.latest) return `${version} · ${t("up to date")}`;
  if (u.error) return `${version} · ${t("update check failed")}`;
  return version;
}
