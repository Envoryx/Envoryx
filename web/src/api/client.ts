import type {
  AuditEntry,
  CreateProjectRequest,
  Dashboard,
  DatabaseCredentials,
  DatabaseInfo,
  ActionInfo,
  BackupInfo,
  DockerOverview,
  ExtraServiceInfo,
  GitRequest,
  GitResult,
  GitStatus,
  LogLine,
  Preview,
  Project,
  PruneResult,
  RuntimesResponse,
  Settings,
  UnusedImage,
  UpdateProjectRequest,
  Usage,
  User,
} from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: unknown;

  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

type UnauthorizedHandler = () => void;
let onUnauthorized: UnauthorizedHandler | null = null;

/** Registers a callback invoked whenever the API answers 401 (used to redirect to login). */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  onUnauthorized = handler;
}

interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  /** Skip the unauthorized handler (used by the login page itself). */
  silent401?: boolean;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    // CSRF defence in depth: the backend requires this header on state-changing requests.
    "X-Requested-With": "Staqio",
  };
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const init: RequestInit = {
    method: opts.method ?? "GET",
    headers,
    credentials: "same-origin",
  };
  if (opts.body !== undefined) init.body = JSON.stringify(opts.body);
  if (opts.signal) init.signal = opts.signal;

  const res = await fetch(`/api/v1${path}`, init);

  if (res.status === 204) {
    return undefined as T;
  }
  let payload: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = null;
    }
  }
  if (!res.ok) {
    const err = (payload as { error?: { code?: string; message?: string; details?: unknown } } | null)?.error;
    if (res.status === 401 && !opts.silent401) {
      onUnauthorized?.();
    }
    throw new ApiError(res.status, err?.code ?? "http_error", err?.message ?? `Request failed (${res.status})`, err?.details);
  }
  return payload as T;
}

export const api = {
  health: () => request<{ status: string; version: string; docker: boolean; database: boolean }>("/health"),

  auth: {
    setupStatus: () => request<{ needsSetup: boolean }>("/setup"),
    setup: (username: string, password: string) =>
      request<{ user: User }>("/setup", { method: "POST", body: { username, password } }),
    login: (username: string, password: string) =>
      request<{ user: User }>("/auth/login", { method: "POST", body: { username, password }, silent401: true }),
    logout: () => request<void>("/auth/logout", { method: "POST" }),
    me: (silent401 = false) => request<{ user: User }>("/auth/me", { silent401 }),
    changePassword: (currentPassword: string, newPassword: string) =>
      request<void>("/auth/password", { method: "POST", body: { currentPassword, newPassword } }),
  },

  dashboard: () => request<Dashboard>("/dashboard"),
  runtimes: () => request<RuntimesResponse>("/runtimes"),
  docker: () => request<DockerOverview>("/docker"),
  unusedImages: () => request<{ images: UnusedImage[] }>("/docker/images/unused"),
  pruneImages: () => request<{ result: PruneResult }>("/docker/images/prune", { method: "POST" }),
  settings: () => request<Settings>("/settings"),
  updateSettings: (body: { publicHost?: string }) => request<Settings>("/settings", { method: "PATCH", body }),
  audit: (limit = 100) => request<{ entries: AuditEntry[] }>(`/audit?limit=${limit}`),
  reconcile: () => request<{ report: unknown }>("/system/reconcile", { method: "POST" }),

  projects: {
    list: () => request<{ projects: Project[] }>("/projects"),
    get: (id: string) => request<{ project: Project }>(`/projects/${encodeURIComponent(id)}`),
    preview: (body: CreateProjectRequest) => request<{ preview: Preview }>("/projects/preview", { method: "POST", body }),
    create: (body: CreateProjectRequest) => request<{ project: Project }>("/projects", { method: "POST", body }),
    update: (id: string, body: UpdateProjectRequest) =>
      request<{ project: Project }>(`/projects/${encodeURIComponent(id)}`, { method: "PATCH", body }),
    remove: (id: string, confirm: string, deleteFiles: boolean) =>
      request<void>(`/projects/${encodeURIComponent(id)}`, { method: "DELETE", body: { confirm, deleteFiles } }),
    start: (id: string) => request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/start`, { method: "POST" }),
    stop: (id: string) => request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/stop`, { method: "POST" }),
    restart: (id: string) =>
      request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/restart`, { method: "POST" }),
    plan: (id: string) => request<{ plan: Preview }>(`/projects/${encodeURIComponent(id)}/plan`),
    stats: (id: string) => request<{ stats: Usage; sampledAt: string }>(`/projects/${encodeURIComponent(id)}/stats`),
    actions: (id: string) => request<{ actions: ActionInfo[] }>(`/projects/${encodeURIComponent(id)}/actions`),
    extras: (id: string) => request<{ services: ExtraServiceInfo[] }>(`/projects/${encodeURIComponent(id)}/extras`),
    logs: (id: string, kind: string, tail = 500) =>
      request<{ lines: LogLine[] }>(`/projects/${encodeURIComponent(id)}/services/${encodeURIComponent(kind)}/logs?tail=${tail}`),
  },

  backups: {
    list: (id: string) => request<{ backups: BackupInfo[] }>(`/projects/${encodeURIComponent(id)}/backups`),
    create: (id: string, body: { database: boolean; files: boolean; includeDependencies: boolean; note: string }) =>
      request<{ backup: BackupInfo }>(`/projects/${encodeURIComponent(id)}/backups`, { method: "POST", body }),
    remove: (id: string, backupId: string) =>
      request<void>(`/projects/${encodeURIComponent(id)}/backups/${encodeURIComponent(backupId)}`, { method: "DELETE" }),
    restore: (id: string, backupId: string, body: { database: boolean; files: boolean; wipeFiles: boolean; confirm: string }) =>
      request<{ backup: BackupInfo }>(`/projects/${encodeURIComponent(id)}/backups/${encodeURIComponent(backupId)}/restore`, { method: "POST", body }),
  },

  git: {
    status: (id: string) => request<{ git: GitStatus }>(`/projects/${encodeURIComponent(id)}/git`),
    set: (id: string, body: GitRequest) => request<{ git: GitStatus }>(`/projects/${encodeURIComponent(id)}/git`, { method: "PUT", body }),
    clone: (id: string) => request<{ result: GitResult }>(`/projects/${encodeURIComponent(id)}/git/clone`, { method: "POST" }),
    pull: (id: string) => request<{ result: GitResult }>(`/projects/${encodeURIComponent(id)}/git/pull`, { method: "POST" }),
    checkout: (id: string, branch: string) =>
      request<{ result: GitResult }>(`/projects/${encodeURIComponent(id)}/git/checkout`, { method: "POST", body: { branch } }),
    deployKey: () => request<{ publicKey: string }>("/settings/deploy-key"),
    regenerateDeployKey: () => request<{ publicKey: string }>("/settings/deploy-key/regenerate", { method: "POST" }),
  },

  database: {
    info: (id: string) => request<{ database: DatabaseInfo }>(`/projects/${encodeURIComponent(id)}/database`),
    credentials: (id: string) =>
      request<{ credentials: DatabaseCredentials }>(`/projects/${encodeURIComponent(id)}/database/credentials`),
    rotate: (id: string) => request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/database/rotate`, { method: "POST" }),
    expose: (id: string, exposed: boolean) =>
      request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/database/expose`, { method: "POST", body: { exposed } }),
    list: (id: string) => request<{ databases: string[] }>(`/projects/${encodeURIComponent(id)}/database/databases`),
    create: (id: string, name: string) =>
      request<void>(`/projects/${encodeURIComponent(id)}/database/databases`, { method: "POST", body: { name } }),
    drop: (id: string, name: string) =>
      request<void>(`/projects/${encodeURIComponent(id)}/database/databases/${encodeURIComponent(name)}`, { method: "DELETE", body: { confirm: name } }),
  },
};
