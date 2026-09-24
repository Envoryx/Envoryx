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

/** A long-running project action (create, start, image pull, backup …) and its current step. */
export interface Operation {
  id: string;
  projectId?: string;
  projectSlug: string;
  projectName: string;
  action: OperationAction;
  /** English template with {{placeholders}} – translate with t(step, stepArgs). */
  step?: string;
  stepArgs?: Record<string, string>;
  startedAt: string;
  updatedAt: string;
  finishedAt?: string;
  error?: string;
}

export type OperationAction = "create" | "duplicate" | "rename" | "start" | "stop" | "restart" | "update" | "delete" | "image" | "backup" | "snapshot" | "clone-database" | "restore";

export interface ProjectStatus {
  state: ProjectState;
  services: ServiceStatus[];
  warnings: string[];
  /** The action running on the project right now, if any. */
  operation?: Operation;
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

/** What a project's primary hostname serves: PHP-FPM behind the web server, the Python application server, the Node dev server or static files. */
export type Serves = "php" | "python" | "node" | "static";

/** Kind of the container that runs the project's code. */
export type AppKind = "php" | "python" | "node";

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
  /** Set when the Node dev server is enabled (routed by the proxy); also the primary route when the project has neither PHP nor a Python server. */
  devHostname?: string;
  /** Missing on payloads from a backend that predates it – use servesOf() then. */
  serves?: Serves;
  /** The application container: PHP if present, else Python, else Node; absent for static projects. */
  appService?: AppKind;
  backupSchedule: BackupSchedule;
  ideGateway?: boolean;
}

/**
 * Client-side fallback for `project.serves`: PHP enabled → php; Python enabled with the server
 * on → python; Node enabled with the dev server on → node; everything else → static. Prefer
 * `project.serves ?? servesOf(project)`.
 */
export function servesOf(p: Pick<Project, "services">): Serves {
  const enabled = (kind: string) => p.services.find((s) => s.kind === kind && s.enabled);
  if (enabled("php")) return "php";
  const python = enabled("python");
  if (python && (python.config as PythonConfig).server) return "python";
  const node = enabled("node");
  if (node && (node.config as NodeConfig).devServer) return "node";
  return "static";
}

/** Client-side fallback for `project.appService`: the first enabled application runtime. */
export function appKindOf(p: Pick<Project, "services">): AppKind | undefined {
  const enabled = (kind: string) => p.services.some((s) => s.kind === kind && s.enabled);
  return enabled("php") ? "php" : enabled("python") ? "python" : enabled("node") ? "node" : undefined;
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
  /** The runtime the template scaffolds for and runs in; older backends omit it (PHP). */
  runtime?: AppKind;
  /** Dev-server defaults of a Node template (preset, port, script). */
  node?: NodeConfig;
  /** Server defaults of a Python template (preset, port, app). */
  python?: PythonConfig;
}

/** Framework preset of the Node dev server with the port the framework listens on by default. */
export interface NodePreset {
  key: string;
  label: string;
  port: number;
}

/** Fallback when the backend predates nodePresets. */
export const defaultNodePresets: NodePreset[] = [
  { key: "vite", label: "Vite (Vue, React, Svelte, Laravel…)", port: 5173 },
  { key: "next", label: "Next.js", port: 3000 },
  { key: "nuxt", label: "Nuxt", port: 3000 },
  { key: "generic", label: "Other (HOST/PORT env only)", port: 5173 },
];

/** Application-server preset of the Python runtime with its default port and import path. */
export interface PythonPreset {
  key: string;
  label: string;
  port: number;
  app: string;
  appLabel: string;
  appHint: string;
}

/** Fallback when the backend predates pythonPresets. */
export const defaultPythonPresets: PythonPreset[] = [
  { key: "django", label: "Django", port: 8000, app: "config.wsgi:application", appLabel: "WSGI application (production mode)", appHint: "e.g. config.wsgi:application – dev mode runs manage.py runserver" },
  { key: "flask", label: "Flask", port: 5000, app: "app:app", appLabel: "Application", appHint: "module:attribute, e.g. app:app" },
  { key: "asgi", label: "FastAPI / ASGI (uvicorn)", port: 8000, app: "main:app", appLabel: "ASGI application", appHint: "module:attribute, e.g. main:app" },
  { key: "wsgi", label: "WSGI (gunicorn)", port: 8000, app: "app:app", appLabel: "WSGI application", appHint: "module:attribute, e.g. app:app" },
  { key: "module", label: "Other (python -m, HOST/PORT env only)", port: 8000, app: "app", appLabel: "Module", appHint: "run as python -m <module>; listen on $HOST:$PORT" },
];

export interface RuntimesResponse {
  runtimes: Runtime[];
  phpExtensions: PHPExtension[];
  phpDefaults: PHPConfig;
  templates?: ProjectTemplate[];
  nodePresets?: NodePreset[];
  pythonPresets?: PythonPreset[];
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
  /** OpenSearch only: add OpenSearch Dashboards. */
  dashboards?: boolean;
}

export interface ExtraUpdate {
  enabled: boolean;
  version?: string;
  exposePort?: boolean;
  removeData?: boolean;
  /** OpenSearch only: switch OpenSearch Dashboards on or off; left out, it stays as it is. */
  dashboards?: boolean;
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
  /** RabbitMQ login; the password comes from the credentials endpoint. */
  username?: string;
  /** OpenSearch Dashboards, when the OpenSearch service has it; its port is webUiPort. */
  dashboards?: { image: string; state: string; health?: string };
}

export interface RabbitMQCredentials {
  username: string;
  password: string;
  url: string;
}

/** Admin key of Meilisearch (master key) or Typesense; url is the one inside the project network. */
export interface SearchCredentials {
  apiKey: string;
  url: string;
}

/** Node.js service with optional dev-server mode (script runs as the container's main process). */
export interface NodeRequest {
  version: string;
  devServer?: boolean;
  /** "dev" (default) or "production": build script first, then the script with NODE_ENV=production. */
  mode?: string;
  packageManager?: string;
  script?: string;
  buildScript?: string;
  port?: number;
  preset?: string;
  /** Publish the inspector port so an IDE can attach; the script has to start the inspector itself. */
  inspect?: boolean;
  inspectPort?: number;
}

/** Stored Node service config (from project.services[kind=node].config). */
export interface NodeConfig {
  devServer?: boolean;
  mode?: string;
  packageManager?: string;
  script?: string;
  buildScript?: string;
  port?: number;
  preset?: string;
  hostPort?: number;
  inspect?: boolean;
  inspectPort?: number;
  inspectHostPort?: number;
}

/** Python service with optional application-server mode (the server runs as the container's main process). */
export interface PythonRequest {
  version: string;
  server?: boolean;
  /** "dev" (default, reload + debug) or "production" (gunicorn/uvicorn without reload). */
  mode?: string;
  preset?: string;
  /** Import path of the application: module:attribute (main:app, config.wsgi:application). */
  app?: string;
  port?: number;
  /** Publish the debugpy port so an IDE can attach; the application has to start debugpy itself. */
  debug?: boolean;
  debugPort?: number;
}

/** Stored Python service config (from project.services[kind=python].config). */
export interface PythonConfig {
  server?: boolean;
  mode?: string;
  preset?: string;
  app?: string;
  port?: number;
  hostPort?: number;
  debug?: boolean;
  debugPort?: number;
  debugHostPort?: number;
}

/** Stored web service config (from project.services[kind=web].config). */
export interface WebServerConfig {
  /** Unknown paths return index.html (client-side routing); only for projects without PHP. */
  spaFallback?: boolean;
}

/** Web server of a project; spaFallback is rejected while the project has PHP. */
export interface WebRequest {
  type: string;
  version: string;
  spaFallback?: boolean;
}

export interface CreateProjectRequest {
  name: string;
  path?: string;
  docroot?: string;
  php?: { version: string; config: PHPConfig } | null;
  node?: NodeRequest | null;
  python?: PythonRequest | null;
  database?: DatabaseRequest | null;
  redis?: ExtraRequest | null;
  memcached?: ExtraRequest | null;
  mailpit?: ExtraRequest | null;
  rabbitmq?: ExtraRequest | null;
  meilisearch?: ExtraRequest | null;
  typesense?: ExtraRequest | null;
  opensearch?: ExtraRequest | null;
  storage?: StorageRequest | null;
  git?: GitRequest | null;
  web?: WebRequest;
  env?: EnvVar[];
  template?: string;
  createStarter?: boolean;
  start?: boolean;
}

/**
 * Copy of an existing project. Every part defaults to "what the original has", so only
 * what the user switched off has to be sent.
 */
export interface DuplicateProjectRequest {
  name: string;
  path?: string;
  files?: boolean;
  includeDependencies?: boolean;
  database?: boolean;
  storage?: boolean;
  workers?: boolean;
  git?: boolean;
  start?: boolean;
}

/**
 * Renames a project and everything derived from its identifier. `confirm` is the current
 * identifier; `keepDataNames` leaves database, login and bucket as they are.
 */
export interface RenameProjectRequest {
  name: string;
  path?: string;
  confirm: string;
  keepDataNames?: boolean;
}

/** What a rename moved – the UI names the new database and bucket afterwards. */
export interface RenameResult {
  from: string;
  to: string;
  path: string;
  database?: string;
  username?: string;
  bucket?: string;
}

export interface UpdateProjectRequest {
  name?: string;
  docroot?: string;
  web?: WebRequest;
  /** enabled false removes PHP; enabled (default true) on a project without PHP adds it. */
  php?: { enabled?: boolean; version?: string; config?: PHPConfig };
  node?: ({ enabled: true } & NodeRequest) | { enabled: false };
  python?: ({ enabled: true } & PythonRequest) | { enabled: false };
  database?: DatabaseUpdate;
  redis?: ExtraUpdate;
  memcached?: ExtraUpdate;
  mailpit?: ExtraUpdate;
  rabbitmq?: ExtraUpdate;
  meilisearch?: ExtraUpdate;
  typesense?: ExtraUpdate;
  opensearch?: ExtraUpdate;
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
  /** Missing on previews from a backend that predates it. */
  serves?: Serves;
  appService?: AppKind;
  /** Set when the Node dev server is on. */
  devHostname?: string;
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
  /** What Envoryx did on its own since it started (projects resumed, orphans removed), newest first. */
  activity?: Activity[];
  hostPath: HostPathStatus;
  storage: StorageUsage[] | null;
  version: string;
  update?: UpdateStatus;
  publicHost: string;
  baseDomain: string;
  proxy: ProxyInfo;
}

/** One autonomous action of Envoryx, see ActivityNotice. */
export interface Activity {
  at: string;
  kind: "projects.resumed" | "docker.orphans_removed" | string;
  items: string[];
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
  projectsFollowEnvoryx?: boolean;
  folderViewFolder?: string;
}

export interface SSHInfo {
  enabled: boolean;
  port: number;
  fingerprint: string;
  /** Same key as MD5 – the form JetBrains IDEs show. */
  fingerprintMd5?: string;
}

export interface Settings {
  publicHost: string;
  /** Envoryx has its own IP and no public host is set: links to published ports break. */
  publicHostNeeded?: boolean;
  publicHostSuggestion?: { hostname?: string; ip?: string } | null;
  /** Startup findings (e.g. config directory on FUSE/network storage). */
  warnings: string[];
  xdebugClientHost?: string;
  sshAuthorizedKeys?: string;
  ssh?: SSHInfo;
  baseDomain: string;
  forceHttps: boolean;
  /** Project containers stop with the Envoryx container and resume when it comes back. */
  projectsFollowEnvoryx?: boolean;
  /** FolderView3 folder (Unraid plugin) the containers are labelled for; "" = none. */
  folderViewFolder?: string;
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
  storage?: { bucket: string; objects: number; bytes: number };
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

export type CronRuntime = "php" | "node" | "python";
export type CronRunStatus = "running" | "succeeded" | "failed" | "timed_out" | "error" | "interrupted";

export interface CronRun {
  id: string;
  source: "schedule" | "manual";
  status: CronRunStatus;
  exitCode: number;
  startedAt: string;
  finishedAt?: string;
  /** Only in the runs list (operate scope). */
  output?: string;
  truncated?: boolean;
}

export interface CronJob {
  id: string;
  name: string;
  runtime: CronRuntime;
  schedule: string;
  command: string;
  timeoutSeconds: number;
  enabled: boolean;
  nextRun?: string;
  running: boolean;
  runtimeMissing: boolean;
  lastRun?: CronRun;
  createdAt: string;
}

export interface CronJobRequest {
  name: string;
  runtime: CronRuntime;
  schedule: string;
  command: string;
  timeoutSeconds: number;
  enabled: boolean;
}

export interface WorkerPreset {
  id: string;
  group: string;
  label: string;
  description: string;
  argLabel?: string;
  argHint?: string;
  requires?: string[];
  /** Service the worker runs in: "php", "node" or "python". */
  runtime?: string;
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

/** What a database clone did: where the data came from and the snapshot taken beforehand. */
export interface CloneDatabaseResult {
  source: string;
  database: string;
  snapshot?: BackupInfo;
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

export type LogLevel = "" | "warn" | "error";

export interface LogLine {
  time: string;
  stream: "stdout" | "stderr";
  text: string;
  /** Guessed from the text; "" for ordinary lines. */
  level?: LogLevel;
}

/** Filter of the log endpoints. since/until: RFC 3339 or a duration back from now (6h, 7d). */
export interface LogFilter {
  since?: string;
  until?: string;
  q?: string;
  level?: LogLevel;
  stream?: "" | "stdout" | "stderr";
}

export interface LogPage {
  lines: LogLine[];
  /** All matching lines in the range; only the last lines.length are returned. */
  matched: number;
  truncated: boolean;
}

export interface LogBucket {
  start: string;
  total: number;
  warnings: number;
  errors: number;
}

export interface LogMessage {
  level: "warn" | "error";
  /** The text with numbers, ids and times masked – what groups the occurrences. */
  pattern: string;
  example: string;
  count: number;
  first: string;
  last: string;
}

export interface LogSummary {
  from: string;
  to: string;
  bucketSeconds: number;
  buckets: LogBucket[];
  total: number;
  warnings: number;
  errors: number;
  top: LogMessage[];
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

/** One diagnostics check; title and hint are stable strings the UI translates. */
export interface DiagnosticCheck {
  id: string;
  category: "runtime" | "network" | "security" | "maintenance";
  status: "ok" | "info" | "warning" | "error";
  title: string;
  detail?: string;
  hint?: string;
  action?: { kind: "setPublicHost" | "settingsTab" | "link"; value: string; label?: string };
  docs?: string;
}

export interface Diagnostics {
  checks: DiagnosticCheck[];
  summary: { ok: number; info: number; warning: number; error: number };
  at: string;
}
