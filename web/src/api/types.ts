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
  /** The application health check's state; absent without a check or before its first run. */
  health?: HealthStatus;
}

/** An HTTP request Envoryx sends to the application at an interval. */
export interface HealthCheck {
  /** Path with optional query, e.g. /health; "" switches the check off. */
  path: string;
  /** Expected HTTP status (default 200). */
  status?: number;
  intervalSec?: number;
  timeoutSec?: number;
  /** Failed checks in a row before the application counts as down (default 3). */
  failures?: number;
}

export type HealthState = "pending" | "up" | "failing" | "down" | "paused";

export interface HealthStatus {
  state: HealthState;
  since: string;
  checked?: string;
  status?: number;
  latencyMs?: number;
  error?: string;
  failures?: number;
  downSince?: string;
}

export interface HealthResult {
  ok: boolean;
  status?: number;
  latencyMs: number;
  error?: string;
  url: string;
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
  limits?: ResourceLimits;
  /** The health check with its defaults filled in; absent when off. */
  healthCheck?: HealthCheck;
  /** What the proxy does with the project's requests; absent when nothing. */
  proxyRules?: ProxyRules;
}

export interface RedirectRule {
  /** One of the project's host names; empty = all. */
  host?: string;
  /** A path, or a prefix ending in "*". */
  from: string;
  /** A path or an http(s) address; a trailing "*" gets the rest of the path. */
  to: string;
  status: number;
}

export interface HeaderRule {
  name: string;
  /** Empty removes the header. */
  value: string;
}

export interface CORSRule {
  origins: string[];
  methods?: string[];
  headers?: string[];
  credentials?: boolean;
  maxAgeSec?: number;
}

export interface ProxyRules {
  allowIPs?: string[];
  /** The password stays on the server. */
  basicAuth?: { user: string };
  redirects?: RedirectRule[];
  headers?: HeaderRule[];
  cors?: CORSRule;
}

/** A change of the rules; a basic authentication without password keeps the stored one. */
export interface ProxyRulesRequest extends Omit<ProxyRules, "basicAuth"> {
  basicAuth?: { user: string; password?: string };
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
  /** Connect to a server Envoryx does not run instead of a container; version then picks the client tools. */
  external?: ExternalDatabase;
}

/** A database server Envoryx does not run. port 0 = the flavour's default. */
export interface ExternalDatabase {
  host: string;
  port: number;
  username: string;
  /** On an update, empty keeps the stored password. */
  password: string;
  database: string;
}

/** A Redis server Envoryx does not run. port 0 = 6379. */
export interface ExternalRedis {
  host: string;
  port: number;
  password: string;
}

/** A connection to try before it is stored (POST /external/test). */
export type ExternalTest = { kind: "database"; type: string; version: string } & ExternalDatabase | ({ kind: "redis" } & ExternalRedis);

export interface DatabaseUpdate {
  enabled: boolean;
  type?: string;
  version?: string;
  exposePort?: boolean;
  removeData?: boolean;
  /** Add an external database, or change its connection. */
  external?: ExternalDatabase;
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
  /** "" for the primary database, the name of an additional one otherwise. */
  name: string;
  /** Service kind: "database" or "db-<name>". */
  service: string;
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
  /** A server Envoryx does not run: host and port are its address, state is "external", no volume. */
  external?: boolean;
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
  /** Ollama only: hand the host's GPUs to the container. */
  gpu?: boolean;
  /** Redis only: connect to a server Envoryx does not run. */
  external?: ExternalRedis;
}

export interface ExtraUpdate {
  enabled: boolean;
  version?: string;
  exposePort?: boolean;
  removeData?: boolean;
  /** OpenSearch only: switch OpenSearch Dashboards on or off; left out, it stays as it is. */
  dashboards?: boolean;
  /** Ollama only: switch the GPUs on or off; left out, it stays as it is. */
  gpu?: boolean;
  /** Redis only: add an external server or change its address (empty password keeps it). */
  external?: ExternalRedis;
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
  /** Ollama: whether it was handed the host's GPUs. */
  gpu?: boolean;
  /** A server Envoryx does not run (Redis): host and port are its address, there is no container. */
  external?: boolean;
}

/** A model in the Ollama store every project shares. */
export interface OllamaModel {
  name: string;
  size: number;
  modifiedAt: string;
  family?: string;
  parameterSize?: string;
  quantization?: string;
}

/** A model download started from Envoryx; completed and total add up the model's layers. */
export interface OllamaPull {
  model: string;
  status: string;
  completed: number;
  total: number;
  error?: string;
  done: boolean;
  startedAt: string;
}

export interface OllamaModels {
  models: OllamaModel[];
  pulls: OllamaPull[];
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
  /** Additional databases, each reached by its name (host, NAME_DB_* variables). */
  databases?: (DatabaseRequest & { name: string })[];
  redis?: ExtraRequest | null;
  memcached?: ExtraRequest | null;
  mailpit?: ExtraRequest | null;
  rabbitmq?: ExtraRequest | null;
  meilisearch?: ExtraRequest | null;
  typesense?: ExtraRequest | null;
  opensearch?: ExtraRequest | null;
  ollama?: ExtraRequest | null;
  storage?: StorageRequest | null;
  git?: GitRequest | null;
  web?: WebRequest;
  env?: EnvVar[];
  template?: string;
  createStarter?: boolean;
  start?: boolean;
  /** Apply the envoryx.yml the cloned repository brings (it wins over the services chosen here). Only with git. */
  useManifest?: boolean;
  /** Fill the project from an uploaded website (POST /site-imports). */
  import?: { id: string; adaptConfig: boolean };
}

/** Something the website import wants to tell; `text` is an English i18n key with {{placeholders}}. */
export interface SiteNotice {
  level: "info" | "warning";
  text: string;
  params?: Record<string, string>;
}

/** What an uploaded website was recognised as, and how Envoryx suggests to run it. */
export interface SiteAnalysis {
  format: "zip" | "tar.gz" | "tar";
  /** The folder the archive wraps the site in; it is left out when unpacking. */
  root?: string;
  files: number;
  bytes: number;
  framework: { id: string; name: string; version?: string };
  runtime: "php" | "static" | "node" | "python";
  phpVersion?: string;
  phpExtensions?: string[];
  docroot: string;
  /** "apache" for sites that rely on .htaccess. */
  web?: string;
  database?: string;
  /** adapt: Envoryx can rewrite it; env: the injected variables win; manual: edit it by hand. */
  config?: { path: string; mode: "adapt" | "env" | "manual" } | null;
  configCandidates?: string[];
  dump?: { bytes: number; compressed: boolean; variant?: string; server?: string; tool?: string } | null;
  notices: SiteNotice[];
}

/** A project's temporary public address (Cloudflare quick tunnel). */
export interface ProjectShare {
  active: boolean;
  state?: "starting" | "online" | "stopped";
  url?: string;
  startedAt?: string;
  expiresAt?: string;
  message?: string;
}

/** A way the project runs its tests (PHPUnit, Pest, npm scripts, Playwright, Cypress, pytest, Django). */
export interface TestSuite {
  id: string;
  framework: string;
  label: string;
  service: string;
  cmd: string[];
  /** The runner writes a JUnit report: the result names the failed tests. */
  report: boolean;
  /** What a filter narrows the run to; absent = no filter. */
  filterHint?: string;
  available: boolean;
  reason?: string;
}

export interface TestCase {
  name: string;
  class?: string;
  file?: string;
  line?: number;
  kind: "failure" | "error";
  message: string;
  details?: string;
}

export interface TestResult {
  report: boolean;
  tests: number;
  failures: number;
  errors: number;
  skipped: number;
  seconds: number;
  failed: TestCase[];
  more?: number;
  output?: string;
}

export interface TestRun {
  id: string;
  suite: string;
  filter?: string;
  status: "passed" | "failed" | "cancelled";
  exitCode: number;
  startedAt: string;
  durationMs: number;
  result: TestResult;
}

/** The package cache shared by all projects (Composer, npm, Yarn, pnpm, pip, uv). */
export interface PackageCache {
  path: string;
  bytes: number;
  entries: { tool: string; bytes: number }[];
}

/** An uploaded website waiting for its project. */
export interface SiteImport {
  id: string;
  siteName: string;
  dumpName?: string;
  createdAt: string;
  expiresAt: string;
  analysis: SiteAnalysis;
}

/** What creating a project from an upload did beyond the project itself. */
export interface SiteImportResult {
  framework: SiteAnalysis["framework"];
  database: boolean;
  adapted: { changed: string[] | null; originals: string[] | null; removed: string[] | null };
  notices: SiteNotice[];
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
  /** Adds, changes or removes (enabled: false) additional databases by name. */
  databases?: Record<string, DatabaseUpdate>;
  redis?: ExtraUpdate;
  memcached?: ExtraUpdate;
  mailpit?: ExtraUpdate;
  rabbitmq?: ExtraUpdate;
  meilisearch?: ExtraUpdate;
  typesense?: ExtraUpdate;
  opensearch?: ExtraUpdate;
  ollama?: ExtraUpdate;
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

/** Limits of one group of containers; 0 = no limit. */
export interface LimitSet {
  cpus?: number;
  memoryMb?: number;
}

/** Per-container limits of a project: application containers, services, processes (0 = default 4096). */
export interface ResourceLimits {
  app: LimitSet;
  services: LimitSet;
  pids?: number;
}

/** One running container's usage with the limits it runs under. */
export interface ContainerUsage {
  containerId: string;
  name: string;
  service: string;
  group: "app" | "services";
  /** 100 = one core. */
  cpuPercent: number;
  memoryBytes: number;
  /** Cores, 0 = none. */
  cpuLimit: number;
  /** Bytes, 0 = none. */
  memLimit: number;
}

export type MetricRange = "1h" | "6h" | "24h" | "7d" | "30d" | "90d" | "365d";

/** [ts, cpu %, cpu max %, mem, mem max, rx/s, tx/s, read/s, write/s] */
export type MetricPoint = [number, number, number, number, number, number, number, number, number];

export interface ProjectMetrics {
  from: number;
  to: number;
  /** Bucket size in seconds. */
  res: number;
  containers: { name: string; group: "app" | "services"; points: MetricPoint[] }[];
  sizes: { kind: "volume" | "files" | "backups"; name: string; points: [number, number][] }[];
}

export interface ProjectUsageSummary {
  id: string;
  name: string;
  slug: string;
  cpuAvg: number;
  cpuMax: number;
  cpuNow: number;
  memAvg: number;
  memMax: number;
  memNow: number;
  netRx: number;
  netTx: number;
  blkRead: number;
  blkWrite: number;
  disk: Partial<Record<"volume" | "files" | "backups", number>>;
  /** [ts, cpu %, mem] */
  series: [number, number, number][];
}

export interface MetricsOverview {
  from: number;
  to: number;
  res: number;
  projects: ProjectUsageSummary[];
}

export interface ProjectStatsResponse {
  stats: Usage;
  sampledAt: string;
  containers?: ContainerUsage[];
  limits?: ResourceLimits;
  host?: { cpus: number; memory: number };
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
  /** Non-secret credentials of the provider. */
  fields?: Record<string, string>;
  /** Secret credentials that are stored (their values never come back). */
  secrets?: string[];
}

/** One credential a DNS provider needs; label and hint are English (translated in the UI). */
export interface ACMEField {
  key: string;
  label: string;
  secret: boolean;
  optional?: boolean;
  hint?: string;
}

export interface ACMEProviderInfo {
  key: string;
  name: string;
  fields: ACMEField[];
  propagationMinutes: number;
}

export interface ACMEInfo {
  available: boolean;
  providers: Record<string, string>;
  providerList?: ACMEProviderInfo[];
  status?: ACMEStatus;
}

export interface ACMERequest {
  provider: string;
  domain: string;
  email: string;
  /** The provider's fields; empty secrets keep the stored ones. */
  credentials: Record<string, string>;
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
  logHistory?: { enabled?: boolean; retentionDays?: number; maxMb?: number };
  metricsRetentionDays?: number;
}

/** Container output kept beyond the containers (Settings → General). */
export interface LogHistoryInfo {
  enabled: boolean;
  retentionDays: number;
  maxMb: number;
  /** false when the history directory could not be opened. */
  available: boolean;
  dir?: string;
  usage: { bytes: number; files: number; oldest?: string };
  /** Containers being read right now. */
  following: number;
}

/** The resource history (Settings → General). */
export interface MetricsInfo {
  retentionDays: number;
  /** Stored samples (all resolutions) and disk space measurements. */
  samples: number;
  sizes: number;
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
  logHistory?: LogHistoryInfo;
  metrics?: MetricsInfo;
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

/** One difference between a project and its envoryx.yml. */
export interface ManifestChange {
  /** docroot, web, php, node, python, database, redis …, storage, env, domain, worker, cron */
  section: string;
  /** Variable, host name, worker or cron job within the section. */
  item?: string;
  action: "add" | "change" | "remove";
  from?: string;
  to?: string;
  /** Why the change is not made: "prune" (a removal needs prune), "downgrade" or "external" (a new external connection needs its password). */
  skipped?: "prune" | "downgrade" | "external";
}

export interface ManifestPlan {
  changes: ManifestChange[];
  /** Variables declared under secrets without a value on the project. */
  missingSecrets: string[];
  inSync: boolean;
}

/** The project as envoryx.yml plus the file in the project directory compared with it. */
export interface ProjectManifest {
  fileName: string;
  yaml: string;
  repository: { present: boolean; error?: string; plan?: ManifestPlan };
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
  /** Dumps of additional databases; db is the database's name in the project. */
  databases?: { db: string; type: string; version: string; name: string; bytes: number }[];
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
  /** Copies on offsite targets, by backup id. */
  offsite?: Record<string, OffsiteUpload[]>;
  offsiteTargets?: OffsiteTargetName[];
}

export type OffsiteType = "s3" | "sftp" | "webdav";

/** An offsite target as stored; secrets are only reported as present. */
export interface OffsiteTarget {
  id: string;
  name: string;
  type: OffsiteType;
  enabled: boolean;
  /** Upload every scheduled project backup. */
  auto: boolean;
  /** Take a daily instance backup at instanceHour and upload it. */
  instance: boolean;
  instanceHour: number;
  prefix: string;
  /** Scheduled copies kept per project (0 = all). */
  keep: number;
  instanceKeep: number;
  encrypt: boolean;
  endpoint?: string;
  region?: string;
  bucket?: string;
  accessKey?: string;
  host?: string;
  port?: number;
  user?: string;
  hostKey?: string;
  url?: string;
  secrets: { passphrase: boolean; secretKey: boolean; password: boolean; privateKey: boolean };
  location: string;
  last?: OffsiteUpload;
}

/** What the form sends: the settings plus secrets, which stay unchanged when empty. */
export type OffsiteTargetInput = Omit<OffsiteTarget, "id" | "secrets" | "location" | "last"> & {
  id?: string;
  passphrase?: string;
  secretKey?: string;
  password?: string;
  privateKey?: string;
};

/** The copy of one backup on one target. */
export interface OffsiteUpload {
  targetId: string;
  targetName: string;
  backupId: string;
  scope: "project" | "instance";
  status: "pending" | "running" | "done" | "failed";
  error?: string;
  remoteKey?: string;
  sizeBytes: number;
  attempts: number;
  nextAttemptAt?: string;
  updatedAt: string;
}

export interface OffsiteTargetName {
  id: string;
  name: string;
  type: OffsiteType;
  enabled: boolean;
  encrypt: boolean;
}

/** A backup found on a target. */
export interface RemoteBackup {
  key: string;
  /** Backup directory (project) or instance backup id. */
  id: string;
  createdAt: string;
  kind: string;
  source?: string;
  sizeBytes: number;
  encrypted: boolean;
}

export interface BackupsResponse {
  backups: BackupInfo[];
  offsite?: Record<string, OffsiteUpload[]>;
  offsiteTargets?: OffsiteTargetName[];
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
  /** "history": the stored history, across container recreations; "container": what Docker still holds. */
  source: "history" | "container";
  /** First stored line when source is "history". */
  oldest?: string;
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

/** Filters of the audit log; empty fields do not filter. */
export interface AuditFilter {
  q?: string | undefined;
  /** An account; its API tokens count too. */
  user?: string | undefined;
  /** Action prefixes ("project.", "auth.login"); an entry matches any. */
  actions?: string[] | undefined;
  /** A project id. */
  project?: string | undefined;
  /** Dates (2026-09-26); until counts in full. */
  since?: string | undefined;
  until?: string | undefined;
}

export interface AuditPage {
  entries: AuditEntry[];
  /** Cursor of the next page; "" when this was the last. */
  next: string;
}

/** One setting a project update changed (details.diff of project.updated). */
export interface AuditChange {
  section: string;
  item?: string;
  from?: string;
  to?: string;
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
