export interface User {
  id: string;
  username: string;
  role: string;
}

export type ProjectState =
  | "running"
  | "stopped"
  | "partial"
  | "missing"
  | "error"
  | "creating"
  | "deleting";

export interface PortMapping {
  hostIp: string;
  hostPort: number;
  containerPort: number;
  protocol: string;
}

export interface ServiceStatus {
  workerId?: string;
  kind: string;
  variant: string;
  version: string;
  image: string;
  containerName: string;
  containerId?: string;
  exists: boolean;
  running: boolean;
  state: string;
  status?: string;
  health?: string;
  ports: PortMapping[];
  /** Set when the containers were last recreated from a rebuilt image of the same tag. */
  imageChangedAt?: string;
  /** The image before that is still known: a rollback is possible. */
  imagePrevious: boolean;
  /** The containers run the previous image on purpose (rolled back). */
  imagePinned: boolean;
}

export interface ProjectStatus {
  state: ProjectState;
  services: ServiceStatus[];
  warnings: string[];
}

export interface PHPConfig {
  memoryLimit: string;
  uploadMaxFilesize: string;
  postMaxSize: string;
  maxExecutionTime: number;
  displayErrors: boolean;
  errorReporting: string;
  extensions: string[];
  xdebug?: boolean;
  xdebugMode?: "always" | "trigger";
  xdebugIdeKey?: string;
  xdebugClientHost?: string;
}

export interface ProjectService {
  kind: string;
  variant: string;
  version: string;
  image: string;
  enabled: boolean;
  config: Record<string, unknown>;
}

export interface EnvVar {
  key: string;
  value: string;
  isSecret: boolean;
}

export interface Project {
  id: string;
  name: string;
  slug: string;
  path: string;
  docroot: string;
  desiredState: "running" | "stopped";
  lifecycle: "creating" | "ready" | "deleting" | "failed";
  lastError?: string;
  httpPort: number;
  createdAt: string;
  updatedAt: string;
  services: ProjectService[];
  env: EnvVar[];
  status: ProjectStatus;
  git: { url: string; branch: string; username: string; hasToken: boolean };
  /** Default hostname (slug.base) followed by extra domains. */
  hostnames: string[];
  /** Set when the Node dev server is enabled (routed by the proxy). */
  devHostname?: string;
  backupSchedule: BackupSchedule;
  ideGateway?: boolean;
}

export interface RuntimeVersion {
  version: string;
  image: string;
  label: string;
  eol?: boolean;
  preview?: boolean;
  default?: boolean;
}

export interface Runtime {
  key: string;
  name: string;
  kind: "runtime" | "webserver" | "database" | "service";
  versions: RuntimeVersion[];
  available: boolean;
  description: string;
}

export interface PHPExtension {
  name: string;
  description: string;
  builtIn: boolean;
  available: boolean;
}

export interface ProjectTemplate {
  id: string;
  name: string;
  description: string;
  docroot: string;
  requiresDatabase: boolean;
  recommendedDatabase?: string;
  phpExtensions?: string[];
  notes?: string;
}

export interface RuntimesResponse {
  runtimes: Runtime[];
  phpExtensions: PHPExtension[];
  phpDefaults: PHPConfig;
  templates?: ProjectTemplate[];
}

export interface DatabaseRequest {
  type: string;
  version: string;
  exposePort: boolean;
}

export interface DatabaseUpdate {
  enabled: boolean;
  type?: string;
  version?: string;
  exposePort?: boolean;
  removeData?: boolean;
}

/** The shared in-browser database tool (Adminer). */
export interface DBToolStatus {
  enabled: boolean;
  running: boolean;
  containerId?: string;
  image: string;
  /** Database types the tool can open. */
  supported: string[];
}

export interface DBToolLink {
  url: string;
  server: string;
  username: string;
  database: string;
}

export interface DatabaseInfo {
  type: string;
  version: string;
  image: string;
  host: string;
  port: number;
  database: string;
  username: string;
  hostPort: number;
  injectedEnv: string[];
  state: string;
  health?: string;
  volumeName: string;
  volumeExists: boolean;
}

export interface DatabaseCredentials {
  host: string;
  port: number;
  database: string;
  username: string;
  password: string;
  rootPassword: string;
  hostPort: number;
  url: string;
}

export interface ExtraRequest {
  version?: string;
  exposePort?: boolean;
}

export interface ExtraUpdate {
  enabled: boolean;
  version?: string;
  exposePort?: boolean;
  removeData?: boolean;
}

export interface ExtraServiceInfo {
  kind: string;
  version: string;
  image: string;
  host: string;
  port: number;
  hostPort: number;
  injectedEnv: string[];
  state: string;
  health?: string;
  volumeName?: string;
  webUiPort?: number;
}

/** Node.js service with optional dev-server mode (script runs as the container's main process). */
export interface NodeRequest {
  version: string;
  devServer?: boolean;
  packageManager?: string;
  script?: string;
  port?: number;
  preset?: string;
}

/** Stored Node service config (from project.services[kind=node].config). */
export interface NodeConfig {
  devServer?: boolean;
  packageManager?: string;
  script?: string;
  port?: number;
  preset?: string;
  hostPort?: number;
}

export const nodePresets: Record<string, string> = {
  vite: "Vite (Laravel, Vue, React, Svelte…)",
  next: "Next.js",
  generic: "Other (HOST/PORT env only)",
};

export interface CreateProjectRequest {
  name: string;
  path?: string;
  docroot?: string;
  php?: { version: string; config: PHPConfig } | null;
  node?: NodeRequest | null;
  database?: DatabaseRequest | null;
  redis?: ExtraRequest | null;
  mailpit?: ExtraRequest | null;
  storage?: StorageRequest | null;
  git?: GitRequest | null;
  web?: { type: string; version: string };
  env?: EnvVar[];
  template?: string;
  createStarter?: boolean;
  start?: boolean;
}

export interface UpdateProjectRequest {
  name?: string;
  docroot?: string;
  web?: { type: string; version: string };
  php?: { version: string; config: PHPConfig };
  node?: ({ enabled: true } & NodeRequest) | { enabled: false };
  database?: DatabaseUpdate;
  redis?: ExtraUpdate;
  mailpit?: ExtraUpdate;
  storage?: StorageUpdate;
  env?: EnvVar[];
  ideGateway?: boolean;
}

export interface StorageRequest {
  version?: string;
  publicRead?: boolean;
}

export interface StorageUpdate {
  enabled: boolean;
  version?: string;
  publicRead?: boolean;
  removeData?: boolean;
}

/** S3-compatible object storage of a project; keys only on the credentials endpoint. */
export interface StorageInfo {
  version: string;
  image: string;
  endpoint: string;
  publicUrl: string;
  hostPort: number;
  consolePort: number;
  consolePath: string;
  region: string;
  bucket: string;
  publicRead: boolean;
  accessKey?: string;
  secretKey?: string;
  injectedEnv: string[];
  state: string;
  health?: string;
  volumeName: string;
  hostname: string;
}

export interface PreviewContainer {
  service: string;
  name: string;
  image: string;
  ports: string[];
  mounts: string[];
}

export interface Preview {
  slug: string;
  path: string;
  hostPath: string;
  httpPort: number;
  network: string;
  containers: PreviewContainer[];
  volumes: string[];
  images: string[];
  warnings: string[];
}

export interface DockerInfo {
  connected: boolean;
  error?: string;
  apiVersion?: string;
  serverVersion?: string;
  os?: string;
  architecture?: string;
  containers: number;
  running: number;
  ncpu: number;
  memTotal: number;
}

export interface Usage {
  cpuPercent: number;
  memoryBytes: number;
  running: number;
  containers: number;
}

export interface StatsSummary {
  containers: number;
  running: number;
  cpuPercent: number;
  memoryBytes: number;
  perProject: Record<string, Usage>;
  sampledAt: string;
}

export interface ReconcileIssue {
  projectId: string;
  projectName: string;
  severity: "warning" | "error";
  message: string;
}

export interface HostPathStatus {
  selfContainerId: string;
  overrides: Record<string, string>;
  detected: Record<string, string>;
  bareMetal: boolean;
  error?: string;
}

/** Free space on one of the filesystems Envoryx writes to. */
export interface StorageUsage {
  path: string;
  totalBytes: number;
  freeBytes: number;
  low: boolean;
}

export interface Dashboard {
  projects: { total: number; running: number; stopped: number; attention: number };
  docker: DockerInfo;
  stats: StatsSummary | null;
  recent: Project[];
  issues: ReconcileIssue[];
  orphans: number;
  hostPath: HostPathStatus;
  storage: StorageUsage[] | null;
  version: string;
  update?: UpdateStatus;
  publicHost: string;
  baseDomain: string;
  proxy: ProxyInfo;
}

/** Result of the daily release check against GitHub. */
export interface UpdateStatus {
  current: string;
  enabled: boolean;
  /** False for development builds (main-<sha>, dev), which never report updates. */
  release: boolean;
  latest?: string;
  available: boolean;
  url?: string;
  publishedAt?: string;
  checkedAt?: string;
  error?: string;
}

/** How the embedded reverse proxy is reachable from the host. */
export interface ProxyInfo {
  enabled: boolean;
  httpPort: number;
  httpsPort: number;
  inDocker: boolean;
  tls: boolean;
  /** Set when the container has its own IP (macvlan/ipvlan) instead of published ports. */
  address?: string;
}

export interface DomainEntry {
  id?: string;
  hostname: string;
  default: boolean;
  createdAt?: string;
}

export interface CustomCertInfo {
  subject: string;
  dnsNames: string[];
  notAfter: string;
  issuer: string;
  expired: boolean;
}

export interface ACMEStatus {
  configured: boolean;
  provider?: string;
  domain?: string;
  email?: string;
  staging?: boolean;
  issuing: boolean;
  lastAttempt?: string;
  lastSuccess?: string;
  lastError?: string;
  notAfter?: string;
  names?: string[];
}

export interface ACMEInfo {
  available: boolean;
  providers: Record<string, string>;
  status?: ACMEStatus;
}

export interface ACMERequest {
  provider: string;
  domain: string;
  email: string;
  token: string;
  staging: boolean;
  useAsBaseDomain: boolean;
}

export interface TLSInfo {
  enabled: boolean;
  ca?: { caSubject: string; caFingerprint: string; caNotAfter: string; custom?: CustomCertInfo | null };
  proxy: ProxyInfo;
  baseDomain?: string;
  forceHttps?: boolean;
}

export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  created: string;
  managed: boolean;
  projectId?: string;
  projectName?: string;
  service?: string;
  ports: PortMapping[];
  labels?: Record<string, string>;
}

export interface Orphan {
  type: string;
  id: string;
  name: string;
  projectId: string;
  projectName: string;
  state?: string;
}

export interface DockerOverview {
  info: DockerInfo;
  containers: ContainerSummary[];
  foreign: ContainerSummary[];
  networks: { ID: string; Name: string; Driver: string; Labels: Record<string, string> }[];
  volumes: { Name: string; Driver: string; Labels: Record<string, string> }[];
  orphans: Orphan[];
  hostPath: HostPathStatus;
}

export interface UnusedImage {
  id: string;
  tags: string[];
  size: number;
}

export interface PruneResult {
  removed: UnusedImage[];
  reclaimedBytes: number;
  errors: string[];
}

export type TokenScope = "read" | "operate" | "admin";

export interface APIToken {
  id: string;
  name: string;
  prefix: string;
  /** Access level; every level includes the ones below it. */
  scope: TokenScope;
  /** Project ids the token is confined to; empty = all projects. */
  projects: string[];
  createdAt: string;
  lastUsedAt: string | null;
}

export interface NotifyConfig {
  enabled: boolean;
  provider: string;
  url?: string;
  token?: string;
  chatId?: string;
  smtpHost?: string;
  smtpPort?: number;
  smtpUser?: string;
  smtpPassword?: string;
  smtpSecurity?: string;
  from?: string;
  to?: string;
  kinds: string[] | null;
}

export interface NotifyInfo {
  status: { config: NotifyConfig; hasToken: boolean; hasSmtpPassword: boolean; lastSent?: string; lastError?: string };
  providers: Record<string, string>;
  kinds: { kind: string; description: string; default: boolean }[];
}

export interface UpdateSettingsRequest {
  publicHost?: string;
  xdebugClientHost?: string;
  sshAuthorizedKeys?: string;
  baseDomain?: string;
  forceHttps?: boolean;
}

export interface SSHInfo {
  enabled: boolean;
  port: number;
  fingerprint: string;
}

export interface Settings {
  publicHost: string;
  /** Startup findings (e.g. config directory on FUSE/network storage). */
  warnings: string[];
  xdebugClientHost?: string;
  sshAuthorizedKeys?: string;
  ssh?: SSHInfo;
  baseDomain: string;
  forceHttps: boolean;
  proxy: ProxyInfo;
  version: string;
  update?: UpdateStatus;
  schemaVersion: number;
  configDir: string;
  projectsDir: string;
  hostPath: HostPathStatus;
  portRange: { start: number; end: number };
  puid: number;
  pgid: number;
  dockerHost: string;
  session: { idleTimeout: string; absoluteTimeout: string };
  secureCookies: boolean;
}

export interface GitStatus {
  configured: boolean;
  url?: string;
  branch?: string;
  hasToken: boolean;
  isRepo: boolean;
  currentBranch?: string;
  commit?: string;
  shortHash?: string;
  subject?: string;
  author?: string;
  date?: string;
  dirty: number;
  remote?: string;
  error?: string;
}

export interface GitResult {
  output: string;
  exitCode: number;
  status: GitStatus;
}

export interface GitRequest {
  url: string;
  branch?: string;
  username?: string;
  /** omit to keep the stored token, "" to clear it */
  token?: string;
}

export interface BackupMeta {
  format: number;
  envoryx: string;
  projectId: string;
  projectName: string;
  slug: string;
  createdAt: string;
  note?: string;
  source?: string;
  database?: { type: string; version: string; name: string; bytes: number };
  files?: { bytes: number; entries: number; includeDependencies: boolean };
  runtimes: Record<string, string>;
}

export interface Worker {
  id: string;
  name: string;
  preset: string;
  arg: string;
  enabled: boolean;
  command: string[];
  createdAt: string;
}

export interface WorkerPreset {
  id: string;
  group: string;
  label: string;
  description: string;
  argLabel?: string;
  argHint?: string;
  requires?: string[];
}

export interface WorkerRequest {
  name: string;
  preset: string;
  arg: string;
  enabled: boolean;
}

export interface BackupSchedule {
  schedule: "" | "daily" | "weekly";
  hour: number;
  weekday: number;
  keep: number;
  includeDependencies: boolean;
  lastRun?: string;
}

export interface BackupInfo {
  id: string;
  dir: string;
  kind: string;
  sizeBytes: number;
  createdAt: string;
  meta: BackupMeta;
  missing: boolean;
}

export interface InstanceBackupMeta {
  format: number;
  envoryx: string;
  schema: number;
  createdAt: string;
  kind: string;
  note?: string;
  entries: number;
}

export interface InstanceBackup {
  id: string;
  kind: "manual" | "upload" | "pre-migrate" | "pre-restore" | string;
  sizeBytes: number;
  createdAt: string;
  meta: InstanceBackupMeta;
}

export interface InstanceBackupsResponse {
  backups: InstanceBackup[];
  pendingRestore: { id: string; requestedAt: string } | null;
  dir: string;
  canRestart: boolean;
}

export interface ActionInfo {
  id: string;
  group: string;
  label: string;
  description: string;
  service: string;
  cmd: string[];
  requires: string[] | null;
  destructive: boolean;
  available: boolean;
  reason?: string;
}

export interface LogLine {
  time: string;
  stream: "stdout" | "stderr";
  text: string;
}

export interface AuditEntry {
  id: string;
  createdAt: string;
  username: string;
  action: string;
  targetType: string;
  targetId: string;
  details: Record<string, unknown> | null;
  ip: string;
}
