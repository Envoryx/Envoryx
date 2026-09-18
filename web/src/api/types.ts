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

export interface RuntimesResponse {
  runtimes: Runtime[];
  phpExtensions: PHPExtension[];
  phpDefaults: PHPConfig;
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

export interface CreateProjectRequest {
  name: string;
  path?: string;
  docroot?: string;
  php?: { version: string; config: PHPConfig } | null;
  node?: { version: string } | null;
  database?: DatabaseRequest | null;
  redis?: ExtraRequest | null;
  mailpit?: ExtraRequest | null;
  git?: GitRequest | null;
  web?: { type: string; version: string };
  env?: EnvVar[];
  createStarter?: boolean;
  start?: boolean;
}

export interface UpdateProjectRequest {
  name?: string;
  docroot?: string;
  php?: { version: string; config: PHPConfig };
  node?: { enabled: boolean; version?: string };
  database?: DatabaseUpdate;
  redis?: ExtraUpdate;
  mailpit?: ExtraUpdate;
  env?: EnvVar[];
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

export interface Dashboard {
  projects: { total: number; running: number; stopped: number; attention: number };
  docker: DockerInfo;
  stats: StatsSummary | null;
  recent: Project[];
  issues: ReconcileIssue[];
  orphans: number;
  hostPath: HostPathStatus;
  version: string;
  publicHost: string;
  baseDomain: string;
  proxy: ProxyInfo;
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

export interface APIToken {
  id: string;
  name: string;
  prefix: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export interface UpdateSettingsRequest {
  publicHost?: string;
  baseDomain?: string;
  forceHttps?: boolean;
}

export interface Settings {
  publicHost: string;
  baseDomain: string;
  forceHttps: boolean;
  proxy: ProxyInfo;
  version: string;
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
  staqio: string;
  projectId: string;
  projectName: string;
  slug: string;
  createdAt: string;
  note?: string;
  database?: { type: string; version: string; name: string; bytes: number };
  files?: { bytes: number; entries: number; includeDependencies: boolean };
  runtimes: Record<string, string>;
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
