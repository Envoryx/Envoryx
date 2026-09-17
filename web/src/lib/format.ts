import type { ProjectState } from "@/api/types";
import type { Tone } from "@/components/ui";

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

export function formatRelative(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const diff = Date.now() - t;
  const s = Math.round(diff / 1000);
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d} d ago`;
  return new Date(t).toLocaleDateString();
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

/** Builds the URL a project is reachable at from the browser's point of view. */
export function projectUrl(port: number): string {
  if (!port) return "";
  const host = window.location.hostname;
  return `http://${host}:${port}`;
}

export function serviceLabel(kind: string, version?: string, variant?: string): string {
  const name: Record<string, string> = { php: "PHP", web: variant === "caddy" ? "Caddy" : "Web", node: "Node", database: variant ?? "Database", redis: "Redis" };
  const base = name[kind] ?? kind;
  return version ? `${base} ${version}` : base;
}
