import type {
  AuditEntry,
  CreateProjectRequest,
  Dashboard,
  DatabaseCredentials,
  DatabaseInfo,
  ActionInfo,
  DockerOverview,
  LogLine,
  Preview,
  Project,
  RuntimesResponse,
  Settings,
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
  method?: "GET" | "POST" | "PATCH" | "DELETE";
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
    logs: (id: string, kind: string, tail = 500) =>
      request<{ lines: LogLine[] }>(`/projects/${encodeURIComponent(id)}/services/${encodeURIComponent(kind)}/logs?tail=${tail}`),
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
