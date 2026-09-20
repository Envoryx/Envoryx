import type {
  ACMEInfo,
  ACMERequest,
  ActionInfo,
  APIToken,
  AuditEntry,
  BackupInfo,
  BackupSchedule,
  CreateProjectRequest,
  Dashboard,
  DatabaseCredentials,
  DatabaseInfo,
  DBToolLink,
  DBToolStatus,
  Diagnostics,
  DockerOverview,
  DomainEntry,
  ExtraServiceInfo,
  GitRequest,
  GitResult,
  GitStatus,
  InstanceBackup,
  InstanceBackupsResponse,
  LogLine,
  NotifyConfig,
  NotifyInfo,
  Operation,
  Preview,
  Project,
  ProxyInfo,
  PruneResult,
  RuntimesResponse,
  Settings,
  StorageInfo,
  TLSInfo,
  TokenScope,
  UnusedImage,
  UpdateProjectRequest,
  UpdateSettingsRequest,
  Usage,
  User,
  Worker,
  WorkerPreset,
  WorkerRequest,
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
    "X-Requested-With": "Envoryx",
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

/** Multipart upload; the JSON error envelope is handled like any other request. */
async function upload<T>(path: string, field: string, file: File): Promise<T> {
  const form = new FormData();
  form.append(field, file, file.name);
  const res = await fetch(`/api/v1${path}`, {
    method: "POST",
    headers: { Accept: "application/json", "X-Requested-With": "Envoryx" },
    credentials: "same-origin",
    body: form,
  });
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
    if (res.status === 401) onUnauthorized?.();
    throw new ApiError(res.status, err?.code ?? "http_error", err?.message ?? `Upload failed (${res.status})`, err?.details);
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
  operations: () => request<{ operations: Operation[] }>("/operations"),
  runtimes: () => request<RuntimesResponse>("/runtimes"),
  docker: () => request<DockerOverview>("/docker"),
  unusedImages: () => request<{ images: UnusedImage[] }>("/docker/images/unused"),
  pruneImages: () => request<{ result: PruneResult }>("/docker/images/prune", { method: "POST" }),
  settings: () => request<Settings>("/settings"),
  updateSettings: (body: UpdateSettingsRequest) => request<Settings>("/settings", { method: "PATCH", body }),
  notifications: {
    get: () => request<NotifyInfo>("/settings/notifications"),
    set: (body: NotifyConfig) => request<NotifyInfo>("/settings/notifications", { method: "PUT", body }),
    test: (body: NotifyConfig) => request<void>("/settings/notifications/test", { method: "POST", body }),
  },
  tokens: {
    list: () => request<{ tokens: APIToken[]; mcpUrl: string }>("/tokens"),
    create: (body: { name: string; scope: TokenScope; projects: string[] }) => request<{ token: APIToken; secret: string; mcpUrl: string }>("/tokens", { method: "POST", body }),
    revoke: (id: string) => request<void>(`/tokens/${encodeURIComponent(id)}`, { method: "DELETE" }),
  },
  tls: {
    info: () => request<TLSInfo>("/settings/tls"),
    caUrl: "/api/v1/settings/tls/ca.crt",
    setCustom: (certificate: string, key: string) => request<TLSInfo>("/settings/tls/custom", { method: "PUT", body: { certificate, key } }),
    clearCustom: () => request<TLSInfo>("/settings/tls/custom", { method: "DELETE" }),
    acme: () => request<ACMEInfo>("/settings/tls/acme"),
    setAcme: (body: ACMERequest) => request<ACMEInfo>("/settings/tls/acme", { method: "PUT", body }),
    clearAcme: () => request<ACMEInfo>("/settings/tls/acme", { method: "DELETE" }),
    issueAcme: () => request<void>("/settings/tls/acme/issue", { method: "POST" }),
  },
  audit: (limit = 100) => request<{ entries: AuditEntry[] }>(`/audit?limit=${limit}`),
  reconcile: () => request<{ report: unknown }>("/system/reconcile", { method: "POST" }),
  diagnostics: () => request<Diagnostics>("/system/diagnostics"),

  projects: {
    list: () => request<{ projects: Project[] }>("/projects"),
    get: (id: string) => request<{ project: Project }>(`/projects/${encodeURIComponent(id)}`),
    preview: (body: CreateProjectRequest) => request<{ preview: Preview }>("/projects/preview", { method: "POST", body }),
    create: (body: CreateProjectRequest) => request<{ project: Project }>("/projects", { method: "POST", body }),
    update: (id: string, body: UpdateProjectRequest) =>
      request<{ project: Project }>(`/projects/${encodeURIComponent(id)}`, { method: "PATCH", body }),
    remove: (id: string, confirm: string, deleteFiles: boolean) =>
      request<void>(`/projects/${encodeURIComponent(id)}`, { method: "DELETE", body: { confirm, deleteFiles } }),
    stopIDEBackend: (id: string) => request<{ stopped: number }>(`/projects/${encodeURIComponent(id)}/ide/stop-backend`, { method: "POST" }),
    workers: {
      list: (id: string) => request<{ workers: Worker[]; presets: WorkerPreset[] }>(`/projects/${encodeURIComponent(id)}/workers`),
      add: (id: string, body: WorkerRequest) => request<{ worker: Worker }>(`/projects/${encodeURIComponent(id)}/workers`, { method: "POST", body }),
      update: (id: string, workerId: string, body: WorkerRequest) =>
        request<{ worker: Worker }>(`/projects/${encodeURIComponent(id)}/workers/${encodeURIComponent(workerId)}`, { method: "PUT", body }),
      remove: (id: string, workerId: string) => request<void>(`/projects/${encodeURIComponent(id)}/workers/${encodeURIComponent(workerId)}`, { method: "DELETE" }),
    },
    domains: {
      list: (id: string) => request<{ domains: DomainEntry[]; proxy: ProxyInfo }>(`/projects/${encodeURIComponent(id)}/domains`),
      add: (id: string, hostname: string) => request<{ domain: DomainEntry }>(`/projects/${encodeURIComponent(id)}/domains`, { method: "POST", body: { hostname } }),
      remove: (id: string, domainId: string) =>
        request<void>(`/projects/${encodeURIComponent(id)}/domains/${encodeURIComponent(domainId)}`, { method: "DELETE" }),
    },
    start: (id: string) => request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/start`, { method: "POST" }),
    stop: (id: string) => request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/stop`, { method: "POST" }),
    restart: (id: string) =>
      request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/restart`, { method: "POST" }),
    useImage: (id: string, image: string, use: "previous" | "latest") =>
      request<{ project: Project }>(`/projects/${encodeURIComponent(id)}/images`, { method: "POST", body: { image, use } }),
    plan: (id: string) => request<{ plan: Preview }>(`/projects/${encodeURIComponent(id)}/plan`),
    stats: (id: string) => request<{ stats: Usage; sampledAt: string }>(`/projects/${encodeURIComponent(id)}/stats`),
    actions: (id: string) => request<{ actions: ActionInfo[] }>(`/projects/${encodeURIComponent(id)}/actions`),
    extras: (id: string) => request<{ services: ExtraServiceInfo[] }>(`/projects/${encodeURIComponent(id)}/extras`),
    logs: (id: string, kind: string, tail = 500) =>
      request<{ lines: LogLine[] }>(`/projects/${encodeURIComponent(id)}/services/${encodeURIComponent(kind)}/logs?tail=${tail}`),
  },

  instanceBackups: {
    list: () => request<InstanceBackupsResponse>("/instance/backups"),
    create: (note: string) => request<{ backup: InstanceBackup }>("/instance/backups", { method: "POST", body: { note } }),
    upload: (file: File) => upload<{ backup: InstanceBackup }>("/instance/backups/upload", "file", file),
    remove: (id: string) => request<void>(`/instance/backups/${encodeURIComponent(id)}`, { method: "DELETE" }),
    restore: (id: string, confirm: string) =>
      request<{ scheduled: string; restarting: boolean }>(`/instance/backups/${encodeURIComponent(id)}/restore`, { method: "POST", body: { confirm } }),
    cancelRestore: () => request<void>("/instance/restore", { method: "DELETE" }),
    downloadUrl: (id: string) => `/api/v1/instance/backups/${encodeURIComponent(id)}/download`,
  },

  backups: {
    list: (id: string) => request<{ backups: BackupInfo[] }>(`/projects/${encodeURIComponent(id)}/backups`),
    create: (id: string, body: { database: boolean; files: boolean; storage: boolean; includeDependencies: boolean; note: string }) =>
      request<{ backup: BackupInfo }>(`/projects/${encodeURIComponent(id)}/backups`, { method: "POST", body }),
    remove: (id: string, backupId: string) =>
      request<void>(`/projects/${encodeURIComponent(id)}/backups/${encodeURIComponent(backupId)}`, { method: "DELETE" }),
    restore: (id: string, backupId: string, body: { database: boolean; files: boolean; storage: boolean; wipeFiles: boolean; wipeStorage: boolean; confirm: string }) =>
      request<{ backup: BackupInfo }>(`/projects/${encodeURIComponent(id)}/backups/${encodeURIComponent(backupId)}/restore`, { method: "POST", body }),
    setSchedule: (id: string, body: Omit<BackupSchedule, "lastRun">) =>
      request<{ schedule: BackupSchedule }>(`/projects/${encodeURIComponent(id)}/backups/schedule`, { method: "PUT", body }),
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

  dbtool: {
    status: () => request<DBToolStatus>("/dbtool"),
    set: (enabled: boolean) => request<DBToolStatus>("/dbtool", { method: "PUT", body: { enabled } }),
    open: (id: string) => request<DBToolLink>(`/projects/${encodeURIComponent(id)}/dbtool`, { method: "POST" }),
  },
  storage: {
    info: (id: string) => request<{ storage: StorageInfo }>(`/projects/${encodeURIComponent(id)}/storage`),
    credentials: (id: string) => request<{ storage: StorageInfo }>(`/projects/${encodeURIComponent(id)}/storage/credentials`),
    setPublic: (id: string, publicRead: boolean) =>
      request<{ storage: StorageInfo }>(`/projects/${encodeURIComponent(id)}/storage/public`, { method: "PUT", body: { publicRead } }),
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
