import type { TFunction } from "i18next";
import type { ProjectState, UpdateStatus } from "@/api/types";
import type { Tone } from "@/components/ui";
import i18n from "@/i18n";

/**
 * The identifier derived from a project name, mirroring the server's Slugify: lower case,
 * everything but letters and digits becomes a hyphen. It is a preview – the server has
 * the last word.
 */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

export function formatPercent(v: number): string {
  if (!Number.isFinite(v)) return "0 %";
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} %`;
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
    default:
      return "gray";
  }
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

export function serviceLabel(kind: string, version?: string, variant?: string): string {
  const dbNames: Record<string, string> = { mariadb: "MariaDB", mysql: "MySQL", postgresql: "PostgreSQL", mongodb: "MongoDB" };
  const webNames: Record<string, string> = { caddy: "Caddy", apache: "Apache", nginx: "Nginx" };
  // Product names stay as they are; only the generic fallbacks are translated.
  const name: Record<string, string> = { php: "PHP", web: webNames[variant ?? ""] ?? i18n.t("Web server"), node: "Node.js", python: "Python", database: dbNames[variant ?? ""] ?? i18n.t("Database"), redis: "Redis", mailpit: "Mailpit", rabbitmq: "RabbitMQ", storage: i18n.t("Object storage") };
  const base = name[kind] ?? kind;
  return version ? `${base} ${version}` : base;
}

/** "0.4.0 · up to date" – the version with the outcome of the update check. */
export function formatVersion(t: TFunction, version: string, u?: UpdateStatus): string {
  if (!u || !u.enabled) return version;
  if (!u.release) return `${version} · ${t("development build")}`;
  if (u.available && u.latest) return `${version} · ${t("{{latest}} available", { latest: u.latest })}`;
  if (u.latest) return `${version} · ${t("up to date")}`;
  if (u.error) return `${version} · ${t("update check failed")}`;
  return version;
}
