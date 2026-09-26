import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { ArrowLeft, ArrowRight, Check, Plus, Rocket, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import { useCreateProject, useProjectLinks, useRuntimes, useSettings } from "@/api/hooks";
import { NodeDevServerFields, defaultDevServerForm, devServerRequest, type DevServerForm } from "./NodeDevServerFields";
import { PythonServerFields, defaultPythonServerForm, pythonServerRequest, type PythonServerForm } from "./PythonServerFields";
import { GoServerFields, defaultGoServerForm, goServerRequest, type GoServerForm } from "./GoServerFields";
import { defaultNodePresets, defaultPythonPresets, type AppKind, type CreateProjectRequest, type EnvVar, type ExternalDatabase, type ExternalRedis, type PHPConfig, type Preview, type Project, type ProjectTemplate, type Serves, type SiteImport } from "@/api/types";
import { Alert, Button, Card, Checkbox, Code, ErrorState, Field, Input, PageHeader, Select, Spinner } from "@/components/ui";
import { CreateProgress } from "./CreateProgress";
import { ImportSiteCard, nameFromArchive } from "./ImportSiteCard";
import { databaseNamePattern } from "./databases";
import { EnvEditor } from "./EnvEditor";
import { emptyExternalDatabase, emptyExternalRedis, externalDatabaseComplete, ExternalDatabaseFields, externalDatabaseTypes, ExternalRedisFields } from "./ExternalConnection";
import { PhpConfigForm } from "./PhpConfigForm";
import { webServerHint } from "./webServers";
import { errorText } from "@/lib/errors";
import { slugify } from "@/lib/format";

const steps = ["General", "Runtimes", "Web server", "Database & services", "Environment", "Summary"] as const;

/** The runtime choice of step 1; it presets the PHP/Python/Go/Node checkboxes, docroot and starter page. */
type Stack = AppKind | "static";

const stacks: { id: Stack; name: string; description: string }[] = [
  { id: "php", name: "PHP application", description: "PHP-FPM behind the web server – Laravel, Symfony, WordPress, Drupal, TYPO3, Shopware…" },
  { id: "python", name: "Python application", description: "Django, Flask, FastAPI… – the application server answers on the project URL." },
  { id: "go", name: "Go application", description: "net/http, Gin, Echo… – air rebuilds the server on every change; it answers on the project URL." },
  { id: "node", name: "Node.js application", description: "Vite, Next.js, Nuxt… – the dev server answers on the project URL." },
  { id: "static", name: "Static site", description: "The web server serves files from the document root; no application runtime." },
];

/** Older backends omit the template runtime; every template was a PHP one then. */
function templateRuntime(tpl: ProjectTemplate): AppKind {
  return tpl.runtime ?? "php";
}

interface Form {
  name: string;
  path: string;
  pathTouched: boolean;
  stack: Stack;
  docroot: string;
  docrootTouched: boolean;
  phpEnabled: boolean;
  phpVersion: string;
  phpConfig: PHPConfig;
  nodeEnabled: boolean;
  nodeVersion: string;
  nodeDev: DevServerForm;
  pythonEnabled: boolean;
  pythonVersion: string;
  pythonServer: PythonServerForm;
  goEnabled: boolean;
  goVersion: string;
  goServer: GoServerForm;
  webType: string;
  webVersion: string;
  spaFallback: boolean;
  dbType: string; // "" = none
  dbVersion: string;
  dbExpose: boolean;
  /** The primary database is a server Envoryx does not run. */
  dbExternal: boolean;
  dbConn: ExternalDatabase;
  /** Additional databases next to the primary one. */
  extraDbs: { name: string; type: string; version: string }[];
  redis: boolean;
  redisVersion: string;
  redisExpose: boolean;
  redisExternal: boolean;
  redisConn: ExternalRedis;
  memcached: boolean;
  memcachedExpose: boolean;
  mailpit: boolean;
  rabbitmq: boolean;
  rabbitmqVersion: string;
  rabbitmqExpose: boolean;
  meilisearch: boolean;
  typesense: boolean;
  typesenseExpose: boolean;
  opensearch: boolean;
  opensearchVersion: string;
  opensearchExpose: boolean;
  opensearchDashboards: boolean;
  ollama: boolean;
  ollamaExpose: boolean;
  ollamaGpu: boolean;
  storage: boolean;
  template: string; // "" = blank
  gitUrl: string;
  gitBranch: string;
  gitUsername: string;
  gitToken: string;
  /** Apply the envoryx.yml the repository brings. */
  useManifest: boolean;
  /** Start from an uploaded website instead of a template or repository. */
  importing: boolean;
  importSite: SiteImport | null;
  adaptConfig: boolean;
  env: EnvVar[];
  createStarter: boolean;
  start: boolean;
}

/** What the primary hostname will serve, derived from the form the same way the backend does. */
function servesOfForm(f: Form): Serves {
  if (f.phpEnabled) return "php";
  if (f.pythonEnabled && f.pythonServer.server) return "python";
  if (f.goEnabled && f.goServer.server) return "go";
  if (f.nodeEnabled && f.nodeDev.devServer) return "node";
  return "static";
}

export function NewProjectPage() {
  const { t } = useTranslation();
  const runtimes = useRuntimes();
  const create = useCreateProject();
  const links = useProjectLinks();
  const settings = useSettings();
  const navigate = useNavigate();
  const [step, setStep] = useState(0);
  const [form, setForm] = useState<Form | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);

  useEffect(() => {
    if (runtimes.data && !form) {
      const php = runtimes.data.runtimes.find((r) => r.key === "php");
      const node = runtimes.data.runtimes.find((r) => r.key === "node");
      const python = runtimes.data.runtimes.find((r) => r.key === "python");
      const golang = runtimes.data.runtimes.find((r) => r.key === "go");
      const web = runtimes.data.runtimes.find((r) => r.key === "caddy");
      setForm({
        name: "",
        path: "",
        pathTouched: false,
        stack: "php",
        docroot: "public",
        docrootTouched: false,
        phpEnabled: true,
        phpVersion: php?.versions.find((v) => v.default)?.version ?? php?.versions[0]?.version ?? "",
        phpConfig: runtimes.data.phpDefaults,
        nodeEnabled: false,
        nodeDev: defaultDevServerForm,
        nodeVersion: node?.versions.find((v) => v.default)?.version ?? node?.versions[0]?.version ?? "",
        pythonEnabled: false,
        pythonServer: defaultPythonServerForm,
        pythonVersion: python?.versions.find((v) => v.default)?.version ?? python?.versions[0]?.version ?? "",
        goEnabled: false,
        goServer: defaultGoServerForm,
        goVersion: golang?.versions.find((v) => v.default)?.version ?? golang?.versions[0]?.version ?? "",
        webType: "caddy",
        webVersion: web?.versions.find((v) => v.default)?.version ?? "",
        spaFallback: false,
        dbType: "",
        dbVersion: "",
        dbExpose: false,
        dbExternal: false,
        dbConn: emptyExternalDatabase,
        extraDbs: [],
        redis: false,
        redisVersion: runtimes.data.runtimes.find((r) => r.key === "redis")?.versions.find((v) => v.default)?.version ?? "",
        redisExpose: false,
        redisExternal: false,
        redisConn: emptyExternalRedis,
        memcached: false,
        memcachedExpose: false,
        mailpit: false,
        rabbitmq: false,
        rabbitmqVersion: runtimes.data.runtimes.find((r) => r.key === "rabbitmq")?.versions.find((v) => v.default)?.version ?? "",
        rabbitmqExpose: false,
        meilisearch: false,
        typesense: false,
        typesenseExpose: false,
        opensearch: false,
        opensearchVersion: runtimes.data.runtimes.find((r) => r.key === "opensearch")?.versions.find((v) => v.default)?.version ?? "",
        opensearchExpose: false,
        opensearchDashboards: false,
        ollama: false,
        ollamaExpose: false,
        ollamaGpu: false,
        storage: false,
        template: "",
        gitUrl: "",
        gitBranch: "",
        gitUsername: "",
        gitToken: "",
        useManifest: true,
        importing: false,
        importSite: null,
        adaptConfig: true,
        env: [],
        createStarter: true,
        start: true,
      });
    }
  }, [runtimes.data, form]);

  const serves = form ? servesOfForm(form) : "php";

  const request = useMemo<CreateProjectRequest | null>(() => {
    if (!form) return null;
    const req: CreateProjectRequest = {
      name: form.name.trim(),
      path: form.path.trim() || slugify(form.name),
      docroot: form.docroot.trim(),
      web: { type: form.webType, version: form.webVersion },
      env: form.env.filter((e) => e.key),
      createStarter: form.createStarter,
      start: form.start,
    };
    // The backend rejects the SPA fallback for PHP projects; only send it where it applies.
    if (form.spaFallback && servesOfForm(form) === "static") req.web = { ...req.web!, spaFallback: true };
    if (form.phpEnabled) {
      // The PHP extensions the selected services' clients need (compiled in, off by default).
      const needed = [form.redis && "redis", form.memcached && "memcached", form.rabbitmq && "amqp"].filter((e): e is string => !!e);
      const extensions = [...new Set([...form.phpConfig.extensions, ...needed])].sort();
      req.php = { version: form.phpVersion, config: { ...form.phpConfig, extensions } };
    }
    if (form.nodeEnabled) req.node = { version: form.nodeVersion, ...devServerRequest(form.nodeDev) };
    if (form.pythonEnabled) req.python = { version: form.pythonVersion, ...pythonServerRequest(form.pythonServer) };
    if (form.goEnabled) req.go = { version: form.goVersion, ...goServerRequest(form.goServer) };
    if (form.dbType) req.database = form.dbExternal && externalDatabaseTypes.includes(form.dbType) ? { type: form.dbType, version: form.dbVersion, exposePort: false, external: form.dbConn } : { type: form.dbType, version: form.dbVersion, exposePort: form.dbExpose };
    const extraDbs = form.extraDbs.filter((d) => d.name.trim());
    if (extraDbs.length > 0) req.databases = extraDbs.map((d) => ({ name: d.name.trim(), type: d.type, version: d.version, exposePort: false }));
    if (form.redis) req.redis = form.redisExternal ? { external: form.redisConn } : { version: form.redisVersion, exposePort: form.redisExpose };
    if (form.memcached) req.memcached = { exposePort: form.memcachedExpose };
    if (form.mailpit) req.mailpit = {};
    if (form.rabbitmq) req.rabbitmq = { version: form.rabbitmqVersion, exposePort: form.rabbitmqExpose };
    if (form.meilisearch) req.meilisearch = {};
    if (form.typesense) req.typesense = { exposePort: form.typesenseExpose };
    if (form.opensearch) req.opensearch = { version: form.opensearchVersion, exposePort: form.opensearchExpose, dashboards: form.opensearchDashboards };
    if (form.ollama) req.ollama = { exposePort: form.ollamaExpose, gpu: form.ollamaGpu };
    if (form.storage) req.storage = {};
    if (form.importing) {
      if (form.importSite) req.import = { id: form.importSite.id, adaptConfig: form.adaptConfig };
      req.createStarter = false;
      return req;
    }
    if (form.template) req.template = form.template;
    if (form.gitUrl.trim() && !form.template) {
      const git: NonNullable<CreateProjectRequest["git"]> = { url: form.gitUrl.trim(), branch: form.gitBranch.trim(), username: form.gitUsername.trim() };
      if (form.gitToken) git.token = form.gitToken;
      req.git = git;
      req.createStarter = false;
      if (form.useManifest) req.useManifest = true;
    }
    return req;
  }, [form]);

  // Keyed on the serialised request: a response (or error) for a request that is no longer
  // current is dropped, so e.g. the error of a template the user has since deselected never shows.
  const requestKey = request ? JSON.stringify(request) : "";
  useEffect(() => {
    if (step !== steps.length - 1 || !request) return;
    let cancelled = false;
    setPreview(null);
    setPreviewError(null);
    api.projects
      .preview(request)
      .then((r) => {
        if (!cancelled) setPreview(r.preview);
      })
      .catch((err: unknown) => {
        if (!cancelled) setPreviewError(errorText(err, t, t("Preview failed")));
      });
    return () => {
      cancelled = true;
    };
  }, [step, requestKey]);

  if (runtimes.isPending || !form) return <Spinner label={t("Loading runtimes…")} />;
  if (runtimes.isError) return <ErrorState message={errorText(runtimes.error, t)} />;

  const rt = runtimes.data;
  const php = rt.runtimes.find((r) => r.key === "php");
  const node = rt.runtimes.find((r) => r.key === "node");
  const python = rt.runtimes.find((r) => r.key === "python");
  const golang = rt.runtimes.find((r) => r.key === "go");
  const nodePresets = rt.nodePresets ?? defaultNodePresets;
  const pythonPresets = rt.pythonPresets ?? defaultPythonPresets;
  const webServers = rt.runtimes.filter((r) => r.kind === "webserver" && r.available);
  const web = webServers.find((r) => r.key === form.webType);
  const databases = rt.runtimes.filter((r) => r.kind === "database");
  const services = rt.runtimes.filter((r) => r.kind === "service");
  const templates = (rt.templates ?? []).filter((tpl) => templateRuntime(tpl) === form.stack);
  const selectedTemplate = rt.templates?.find((x) => x.id === form.template);
  const nameError = form.name.trim().length > 0 && form.name.trim().length < 2 ? t("At least 2 characters.") : slugify(form.name) === "" && form.name.trim() ? t("Name must contain letters or digits.") : undefined;
  const extraDbError = (i: number): string | undefined => {
    const n = form.extraDbs[i]!.name.trim();
    if (!n) return undefined;
    if (!databaseNamePattern.test(n)) return t("Lowercase letters, digits and dashes, starting with a letter.");
    if (form.extraDbs.some((d, j) => j < i && d.name.trim() === n)) return t("The project already has a database of this name.");
    return undefined;
  };
  const canContinue = step === 0 ? form.name.trim().length >= 2 && !nameError && (!form.importing || !!form.importSite) : step === 3 ? form.extraDbs.every((_, i) => !extraDbError(i)) && (!form.dbType || !form.dbExternal || !externalDatabaseTypes.includes(form.dbType) || externalDatabaseComplete(form.dbConn)) && (!form.redis || !form.redisExternal || !!form.redisConn.host) : true;
  // The services the new project will have, so an imported .env knows what Envoryx sets.
  const wizardServices = [
    ...(form.dbType ? [{ kind: "database", variant: form.dbType }] : []),
    ...form.extraDbs.filter((d) => d.name.trim()).map((d) => ({ kind: `db-${d.name.trim()}`, variant: d.type })),
    ...(["redis", "memcached", "mailpit", "rabbitmq", "meilisearch", "typesense", "opensearch", "ollama", "storage"] as const).filter((k) => form[k]).map((kind) => ({ kind })),
  ];
  const setExtraDb = (i: number, patch: Partial<Form["extraDbs"][number]>) => set({ extraDbs: form.extraDbs.map((d, j) => (j === i ? { ...d, ...patch } : d)) });
  const set = (patch: Partial<Form>) => setForm((f) => (f ? { ...f, ...patch } : f));

  /** Presets the runtime checkboxes, docroot and starter page for a stack; fields the user edited stay. */
  const chooseStack = (stack: Stack) => {
    const docroot = (fallback: string) => (form.docrootTouched ? form.docroot : fallback);
    const template = selectedTemplate && templateRuntime(selectedTemplate) !== stack ? "" : form.template;
    // Leaving the Node, Python or Go stack turns its server back off: a PHP or static project
    // that later enables the runtime as a toolchain starts from the same default as a fresh flow.
    const nodeOff = { ...form.nodeDev, devServer: false };
    const pythonOff = { ...form.pythonServer, server: false };
    const goOff = { goEnabled: false, goServer: { ...form.goServer, server: false } };
    switch (stack) {
      case "php":
        set({ stack, template, phpEnabled: true, nodeEnabled: false, nodeDev: nodeOff, pythonEnabled: false, pythonServer: pythonOff, ...goOff, createStarter: true, docroot: docroot("public") });
        break;
      case "python":
        set({ stack, template, phpEnabled: false, nodeEnabled: false, nodeDev: nodeOff, pythonEnabled: true, pythonServer: { ...form.pythonServer, server: true }, ...goOff, createStarter: false, docroot: docroot("") });
        break;
      case "go":
        set({ stack, template, phpEnabled: false, nodeEnabled: false, nodeDev: nodeOff, pythonEnabled: false, pythonServer: pythonOff, goEnabled: true, goServer: { ...form.goServer, server: true }, createStarter: false, docroot: docroot("") });
        break;
      case "node":
        set({ stack, template, phpEnabled: false, nodeEnabled: true, nodeDev: { ...form.nodeDev, devServer: true, preset: "vite", port: "5173" }, pythonEnabled: false, pythonServer: pythonOff, ...goOff, createStarter: false, docroot: docroot("") });
        break;
      case "static":
        set({ stack, template, phpEnabled: false, nodeEnabled: false, nodeDev: nodeOff, pythonEnabled: false, pythonServer: pythonOff, ...goOff, createStarter: true, docroot: docroot("") });
        break;
    }
  };

  /** Throws the uploaded website away (it would wait 24 hours on the server otherwise). */
  const discardImport = () => {
    if (form.importSite) api.siteImports.discard(form.importSite.id).catch(() => undefined);
    set({ importSite: null });
  };

  /** Fills the next steps with what the analysis of the uploaded website suggests. */
  const applyImport = (imp: SiteImport) => {
    const a = imp.analysis;
    const defaultVersion = (key: string) => rt.runtimes.find((r) => r.key === key)?.versions.find((v) => v.default)?.version ?? "";
    const patch: Partial<Form> = {
      importSite: imp,
      adaptConfig: true,
      template: "",
      gitUrl: "",
      createStarter: false,
      stack: a.runtime,
      docroot: a.docroot,
      docrootTouched: true,
      phpEnabled: a.runtime === "php",
      nodeEnabled: a.runtime === "node",
      nodeDev: { ...form.nodeDev, devServer: false },
      pythonEnabled: a.runtime === "python",
      pythonServer: { ...form.pythonServer, server: false },
      goEnabled: a.runtime === "go",
      goServer: { ...form.goServer, server: false },
      dbType: a.database ?? "",
      dbVersion: a.database ? defaultVersion(a.database) : "",
    };
    if (!form.name.trim()) patch.name = nameFromArchive(imp.siteName);
    if (a.phpVersion && php?.versions.some((v) => v.version === a.phpVersion)) patch.phpVersion = a.phpVersion;
    if (a.phpExtensions?.length) patch.phpConfig = { ...form.phpConfig, extensions: [...new Set([...form.phpConfig.extensions, ...a.phpExtensions])].sort() };
    const webKey = a.web && webServers.some((r) => r.key === a.web) ? a.web : "caddy";
    patch.webType = webKey;
    patch.webVersion = defaultVersion(webKey);
    set(patch);
  };

  const chooseTemplate = (id: string) => {
    if (form.importing) discardImport();
    const tpl = rt.templates?.find((x) => x.id === id);
    const patch: Partial<Form> = {
      importing: false,
      importSite: null,
      template: id,
      dbType: tpl?.recommendedDatabase && !form.dbType ? tpl.recommendedDatabase : form.dbType,
      dbVersion: tpl?.recommendedDatabase && !form.dbType ? (rt.runtimes.find((r) => r.key === tpl.recommendedDatabase)?.versions.find((v) => v.default)?.version ?? "") : form.dbVersion,
      gitUrl: tpl ? "" : form.gitUrl,
    };
    if (tpl) {
      if (!form.docrootTouched) patch.docroot = tpl.docroot;
      switch (templateRuntime(tpl)) {
        case "node": {
          patch.nodeEnabled = true;
          const n = tpl.node;
          patch.nodeDev = { ...form.nodeDev, devServer: true, preset: n?.preset ?? form.nodeDev.preset, port: n?.port ? String(n.port) : form.nodeDev.port, script: n?.script ?? form.nodeDev.script };
          break;
        }
        case "python": {
          patch.pythonEnabled = true;
          const py = tpl.python;
          patch.pythonServer = { ...form.pythonServer, server: true, preset: py?.preset ?? form.pythonServer.preset, port: py?.port ? String(py.port) : form.pythonServer.port, app: py?.app ?? form.pythonServer.app };
          break;
        }
        case "go": {
          patch.goEnabled = true;
          const g = tpl.go;
          patch.goServer = { ...form.goServer, server: true, pkg: g?.package ?? form.goServer.pkg, port: g?.port ? String(g.port) : form.goServer.port };
          break;
        }
        default:
          patch.phpEnabled = true;
      }
    }
    set(patch);
  };

  const setNodeDev = (nodeDev: DevServerForm) => {
    const patch: Partial<Form> = { nodeDev };
    // Without the dev server the web server serves the build output – suggest the usual folder.
    if (form.stack === "node" && !form.docrootTouched && nodeDev.devServer !== form.nodeDev.devServer) patch.docroot = nodeDev.devServer ? "" : "dist";
    set(patch);
  };

  const docrootHint =
    form.stack === "php"
      ? t('Subfolder served by the web server, e.g. "public" for Laravel/Symfony. Leave empty for the project root.')
      : serves === "node"
        ? t("Not used while the dev server serves the app; the build output (e.g. dist/) once you turn it off.")
        : serves === "python" || serves === "go"
          ? t("Not used while the application server serves the app; static files (e.g. a collected static/ folder) once you turn it off.")
          : t('Build output served by the web server, e.g. "dist". Leave empty for the project root.');

  const submit = () => {
    if (!request) return;
    setSubmitError(null);
    create.mutate(request, {
      onSuccess: (p) => navigate(`/projects/${p.id}`),
      onError: (err) => setSubmitError(errorText(err, t, t("Creating the project failed"))),
    });
  };

  // Older backends omit `serves` on the preview; the form knows the answer as well.
  const previewServes: Serves = preview?.serves ?? serves;
  const devTarget = { httpPort: 0, hostnames: preview?.devHostname ? [preview.devHostname] : [], serves: "node" as Serves, services: [] as Project["services"] };
  const devUrl = preview?.devHostname ? links(devTarget).url : "";

  const versionOptions = (versions: { version: string; label: string; eol?: boolean; preview?: boolean }[]) =>
    versions.map((v) => (
      <option key={v.version} value={v.version}>
        {v.label}
        {v.eol ? t(" (end of life)") : v.preview ? t(" (preview)") : ""}
      </option>
    ));

  const phpCard = php && (
    <div key="php" className="space-y-4 rounded-md border border-default p-4">
      <Checkbox label={t("Enable PHP")} description={t("Runs PHP-FPM in its own container. Disable for Node-only or static projects.")} checked={form.phpEnabled} onChange={(e) => set({ phpEnabled: e.target.checked })} />
      {form.phpEnabled && (
        <>
          <Field label={t("PHP version")} htmlFor="php-version">
            <Select id="php-version" value={form.phpVersion} onChange={(e) => set({ phpVersion: e.target.value })}>
              {versionOptions(php.versions)}
            </Select>
          </Field>
          <PhpConfigForm value={form.phpConfig} onChange={(c) => set({ phpConfig: c })} extensions={rt.phpExtensions} />
          <p className="text-xs text-subtle">{t("Composer ships with the PHP image.")}</p>
        </>
      )}
    </div>
  );

  const nodeCard = node && (
    <div key="node" className="space-y-4 rounded-md border border-default p-4">
      <Checkbox label={t("Enable Node.js")} description={t("Node.js container for your app or asset builds: run a dev server (Vite, Next.js, Nuxt…) or use it as a toolchain.")} checked={form.nodeEnabled} onChange={(e) => set({ nodeEnabled: e.target.checked })} />
      {form.nodeEnabled && (
        <>
          <Field label={t("Node.js version")} htmlFor="node-version">
            <Select id="node-version" value={form.nodeVersion} onChange={(e) => set({ nodeVersion: e.target.value })}>
              {versionOptions(node.versions)}
            </Select>
          </Field>
          <NodeDevServerFields value={form.nodeDev} onChange={setNodeDev} idPrefix="wizard-node" presets={nodePresets} primary={form.stack === "node"} />
          <p className="text-xs text-subtle">{t("npm, pnpm and yarn ship with the Node image.")}</p>
        </>
      )}
    </div>
  );

  const pythonCard = python && (
    <div key="python" className="space-y-4 rounded-md border border-default p-4">
      <Checkbox label={t("Enable Python")} description={t("Python container for your app or tooling: run Django, Flask, FastAPI (uvicorn) or gunicorn as the application server, or use pip, uv and the interpreter from the terminal.")} checked={form.pythonEnabled} onChange={(e) => set({ pythonEnabled: e.target.checked })} />
      {form.pythonEnabled && (
        <>
          <Field label={t("Python version")} htmlFor="python-version">
            <Select id="python-version" value={form.pythonVersion} onChange={(e) => set({ pythonVersion: e.target.value })}>
              {versionOptions(python.versions)}
            </Select>
          </Field>
          <PythonServerFields value={form.pythonServer} onChange={(pythonServer) => set({ pythonServer })} idPrefix="wizard-python" presets={pythonPresets} primary={!form.phpEnabled} />
          <p className="text-xs text-subtle">{t("pip, uv and venv ship with the Python image; the project's .venv is first on PATH.")}</p>
        </>
      )}
    </div>
  );

  const goCard = golang && (
    <div key="go" className="space-y-4 rounded-md border border-default p-4">
      <Checkbox label={t("Enable Go")} description={t("Go container for your service or tooling: build and run it with live reload (air) and the Delve debugger, or use go build, go test and go mod from the terminal.")} checked={form.goEnabled} onChange={(e) => set({ goEnabled: e.target.checked })} />
      {form.goEnabled && (
        <>
          <Field label={t("Go version")} htmlFor="go-version">
            <Select id="go-version" value={form.goVersion} onChange={(e) => set({ goVersion: e.target.value })}>
              {versionOptions(golang.versions)}
            </Select>
          </Field>
          <GoServerFields value={form.goServer} onChange={(goServer) => set({ goServer })} idPrefix="wizard-go" primary={!form.phpEnabled && !(form.pythonEnabled && form.pythonServer.server)} />
          <p className="text-xs text-subtle">{t("Modules and build results are cached for all projects; tools installed with go install stay in the project home.")}</p>
        </>
      )}
    </div>
  );

  const runtimeCards =
    form.stack === "node"
      ? [nodeCard, pythonCard, goCard, phpCard]
      : form.stack === "python"
        ? [pythonCard, nodeCard, goCard, phpCard]
        : form.stack === "go"
          ? [goCard, nodeCard, pythonCard, phpCard]
          : [phpCard, nodeCard, pythonCard, goCard];

  return (
    <div>
      <PageHeader title={t("New project")} description={t("Envoryx creates an isolated Docker environment for your project.")} />
      <div className="grid gap-6 lg:grid-cols-[14rem_1fr]">
        <ol className="flex gap-2 overflow-x-auto lg:flex-col lg:gap-1" aria-label={t("Steps")}>
          {steps.map((label, i) => (
            <li key={label}>
              <button
                type="button"
                onClick={() => i < step && setStep(i)}
                disabled={i > step}
                className={clsx(
                  "flex w-full items-center gap-2.5 whitespace-nowrap rounded-md px-2.5 py-2 text-left text-sm",
                  i === step ? "bg-accent-500/10 font-medium text-accent-600 dark:text-accent-300" : i < step ? "text-fg hover:bg-muted" : "text-subtle",
                )}
                aria-current={i === step ? "step" : undefined}
              >
                <span className={clsx("flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold", i < step ? "bg-accent-600 text-white" : i === step ? "bg-accent-500/20" : "bg-muted")}>
                  {i < step ? <Check className="size-3" /> : i + 1}
                </span>
                {t(label)}
              </button>
            </li>
          ))}
        </ol>

        <Card className="p-6">
          {step === 0 && (
            <div className="space-y-5">
              <Field label={t("Project name")} htmlFor="name" error={nameError} hint={form.name ? t("Identifier: {{slug}}", { slug: slugify(form.name) || "—" }) : t("Displayed in the UI; the identifier is derived from it.")}>
                <Input id="name" autoFocus value={form.name} onChange={(e) => set({ name: e.target.value, path: form.pathTouched ? form.path : "" })} placeholder="Acme Shop" />
              </Field>
              <Field label={t("Project directory")} htmlFor="path" hint={t("Relative to the projects folder (/projects). Created if it does not exist.")}>
                <div className="flex items-center gap-2">
                  <span className="text-sm text-subtle">/projects/</span>
                  <Input id="path" value={form.pathTouched ? form.path : slugify(form.name)} onChange={(e) => set({ path: e.target.value, pathTouched: true })} placeholder="acme-shop" spellCheck={false} />
                </div>
              </Field>
              <fieldset className="space-y-2">
                <legend className="text-sm font-medium text-fg">{t("Runtime")}</legend>
                <div className="grid gap-2 sm:grid-cols-2">
                  {stacks.map((s) => (
                    <label key={s.id} className={clsx("flex cursor-pointer gap-3 rounded-md border p-3 text-sm", form.stack === s.id ? "border-accent-500 bg-accent-500/5" : "border-default hover:bg-muted")}>
                      <input type="radio" name="stack" className="mt-0.5 accent-accent-600" aria-label={t(s.name)} checked={form.stack === s.id} onChange={() => chooseStack(s.id)} />
                      <span>
                        <span className="block font-medium">{t(s.name)}</span>
                        <span className="block text-xs text-muted">{t(s.description)}</span>
                      </span>
                    </label>
                  ))}
                </div>
              </fieldset>
              <fieldset className="space-y-2">
                <legend className="text-sm font-medium text-fg">{t("Start from")}</legend>
                <div className="grid gap-2 sm:grid-cols-2">
                  {[{ id: "", name: t("Blank"), description: t("Empty directory, optionally with a starter page, or clone a repository below.") }, ...templates].map((item) => (
                    <label key={item.id} className={clsx("flex cursor-pointer gap-3 rounded-md border p-3 text-sm", !form.importing && form.template === item.id ? "border-accent-500 bg-accent-500/5" : "border-default hover:bg-muted")}>
                      <input type="radio" name="template" className="mt-0.5 accent-accent-600" checked={!form.importing && form.template === item.id} onChange={() => chooseTemplate(item.id)} />
                      <span>
                        <span className="block font-medium">{item.name}</span>
                        <span className="block text-xs text-muted">{item.description}</span>
                      </span>
                    </label>
                  ))}
                  <label className={clsx("flex cursor-pointer gap-3 rounded-md border p-3 text-sm", form.importing ? "border-accent-500 bg-accent-500/5" : "border-default hover:bg-muted")}>
                    <input type="radio" name="template" className="mt-0.5 accent-accent-600" checked={form.importing} onChange={() => set({ importing: true, template: "", gitUrl: "" })} />
                    <span>
                      <span className="block font-medium">{t("Existing website")}</span>
                      <span className="block text-xs text-muted">{t("Upload the files of a site you already have – from an old host or a backup – and optionally its database dump.")}</span>
                    </span>
                  </label>
                </div>
                {selectedTemplate?.requiresDatabase && !form.dbType && (
                  <p className="text-xs text-amber-600 dark:text-amber-400">{t("This template needs a database – it is preselected in the “Database & services” step.")}</p>
                )}
              </fieldset>
              <Field label={t("Document root")} htmlFor="docroot" hint={docrootHint}>
                <Input id="docroot" value={form.docroot} onChange={(e) => set({ docroot: e.target.value, docrootTouched: true })} placeholder={form.stack === "php" ? "public" : "dist"} spellCheck={false} />
              </Field>
              {form.importing ? (
                <ImportSiteCard value={form.importSite} onUploaded={applyImport} onDiscard={discardImport} adaptConfig={form.adaptConfig} onAdaptConfig={(adaptConfig) => set({ adaptConfig })} />
              ) : (
                <div className={clsx("space-y-4 rounded-md border border-default p-4", form.template && "opacity-50")}>
                  <p className="text-sm font-medium text-fg">{form.template ? t("Git repository (optional – not with a template)") : t("Git repository (optional)")}</p>
                  <Field label={t("Repository URL")} htmlFor="git-url" hint={t("Cloned into the empty project directory. https://…, git@host:path.git or ssh://…")}>
                    <Input id="git-url" value={form.gitUrl} onChange={(e) => set({ gitUrl: e.target.value })} placeholder="https://github.com/you/project.git" spellCheck={false} disabled={!!form.template} />
                  </Field>
                  {form.gitUrl.trim() && (
                    <div className="grid gap-4 sm:grid-cols-3">
                      <Field label={t("Branch")} htmlFor="git-branch" hint={t("Empty = default branch")}>
                        <Input id="git-branch" value={form.gitBranch} onChange={(e) => set({ gitBranch: e.target.value })} placeholder="main" spellCheck={false} />
                      </Field>
                      {!(form.gitUrl.startsWith("git@") || form.gitUrl.startsWith("ssh://")) ? (
                        <>
                          <Field label={t("Username (optional)")} htmlFor="git-user">
                            <Input id="git-user" value={form.gitUsername} onChange={(e) => set({ gitUsername: e.target.value })} placeholder="x-access-token" autoComplete="off" />
                          </Field>
                          <Field label={t("Access token")} htmlFor="git-token" hint={t("Only for private repositories")}>
                            <Input id="git-token" type="password" value={form.gitToken} onChange={(e) => set({ gitToken: e.target.value })} autoComplete="new-password" />
                          </Field>
                        </>
                      ) : (
                        <p className="self-end pb-2 text-xs text-muted sm:col-span-2">{t("SSH uses the Envoryx deploy key (Settings → Deploy key); add it to the repository first.")}</p>
                      )}
                    </div>
                  )}
                  {form.gitUrl.trim() && (
                    <Checkbox
                      label={t("Use the repository's envoryx.yml")}
                      description={t("If the repository brings one, it decides runtimes, services, domains, environment, workers and cron jobs; the next steps only count without it.")}
                      checked={form.useManifest}
                      onChange={(e) => set({ useManifest: e.target.checked })}
                    />
                  )}
                </div>
              )}
            </div>
          )}

          {step === 1 && <div className="space-y-6">{runtimeCards}</div>}

          {step === 2 && web && (
            <div className="space-y-5">
              <Field label={t("Web server")} htmlFor="web">
                <Select
                  id="web"
                  value={form.webType}
                  onChange={(e) => {
                    const next = webServers.find((r) => r.key === e.target.value);
                    set({ webType: e.target.value, webVersion: next?.versions.find((v) => v.default)?.version ?? next?.versions[0]?.version ?? "" });
                  }}
                >
                  {webServers.map((r) => (
                    <option key={r.key} value={r.key}>
                      {r.name}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label={t("Version")} htmlFor="web-version">
                <Select id="web-version" value={form.webVersion} onChange={(e) => set({ webVersion: e.target.value })}>
                  {web.versions.map((v) => (
                    <option key={v.version} value={v.version}>
                      {v.label}
                    </option>
                  ))}
                </Select>
              </Field>
              <p className="text-sm text-muted">{webServerHint(t, form.webType, serves)}</p>
              <p className="text-sm text-muted">
                {serves === "php"
                  ? t("The web server serves static files from the document root and forwards PHP requests to the PHP container via FastCGI. The project is published on an automatically assigned port and reachable through the proxy under its domain.")
                  : serves === "node"
                    ? t("The dev server answers on the project URL. The web server is part of every project and serves the document root once the dev server is turned off.")
                    : serves === "python" || serves === "go"
                      ? t("The application server answers on the project URL. The web server is part of every project and serves the document root once the server is turned off.")
                      : t("The web server serves static files from the document root.")}
              </p>
              {serves === "static" && (
                <Checkbox label={t("SPA fallback to index.html")} description={t("Unknown paths return index.html so client-side routers work after a reload.")} checked={form.spaFallback} onChange={(e) => set({ spaFallback: e.target.checked })} />
              )}
            </div>
          )}

          {step === 3 && (
            <div className="space-y-6">
              <div>
                <p className="mb-2 text-sm font-medium text-fg">{t("Database")}</p>
                <div className="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-label={t("Database")}>
                  <label className={clsx("flex items-center gap-2.5 rounded-md border px-3 py-2 text-sm cursor-pointer", form.dbType === "" ? "border-accent-500 bg-accent-500/5" : "border-default hover:bg-muted")}>
                    <input type="radio" name="db" className="accent-accent-600" checked={form.dbType === ""} onChange={() => set({ dbType: "", dbVersion: "" })} /> {t("None")}
                  </label>
                  {databases.map((d) => (
                    <label
                      key={d.key}
                      className={clsx("flex items-center gap-2.5 rounded-md border px-3 py-2 text-sm", !d.available ? "border-default opacity-60" : form.dbType === d.key ? "border-accent-500 bg-accent-500/5 cursor-pointer" : "border-default hover:bg-muted cursor-pointer")}
                      title={d.description}
                    >
                      <input
                        type="radio"
                        name="db"
                        className="accent-accent-600"
                        disabled={!d.available}
                        checked={form.dbType === d.key}
                        onChange={() => set({ dbType: d.key, dbVersion: d.versions.find((v) => v.default)?.version ?? d.versions[0]?.version ?? "" })}
                      />
                      {d.name}
                      {!d.available && <span className="text-xs text-subtle">{t("soon")}</span>}
                    </label>
                  ))}
                </div>
              </div>
              {form.dbType && (
                <div className="space-y-4 rounded-md border border-default p-4">
                  {externalDatabaseTypes.includes(form.dbType) && (
                    <div className="flex flex-wrap gap-4" role="radiogroup" aria-label={t("Where the database runs")}>
                      <label className="inline-flex items-center gap-2 text-sm">
                        <input type="radio" name="db-where" checked={!form.dbExternal} onChange={() => set({ dbExternal: false })} />
                        {t("In a container of the project")}
                      </label>
                      <label className="inline-flex items-center gap-2 text-sm">
                        <input type="radio" name="db-where" checked={form.dbExternal} onChange={() => set({ dbExternal: true })} />
                        {t("On an external server")}
                      </label>
                    </div>
                  )}
                  <Field label={t("Version")} htmlFor="db-version" hint={form.dbExternal ? t("Picks the client tools for backups and the connection; choose the server's major version.") : t("Upgrades between versions run on the same data volume; downgrades are not possible.")}>
                    <Select id="db-version" value={form.dbVersion} onChange={(e) => set({ dbVersion: e.target.value })}>
                      {databases
                        .find((d) => d.key === form.dbType)
                        ?.versions.map((v) => (
                          <option key={v.version} value={v.version}>
                            {v.label}
                          </option>
                        ))}
                    </Select>
                  </Field>
                  {form.dbExternal && externalDatabaseTypes.includes(form.dbType) ? (
                    <>
                      <ExternalDatabaseFields id="db-ext" type={form.dbType} version={form.dbVersion} value={form.dbConn} onChange={(dbConn) => set({ dbConn })} />
                      <p className="text-sm text-muted">
                        {t("Envoryx runs no database container and injects this server's DB_HOST, DB_PORT, DB_DATABASE, DB_USERNAME, DB_PASSWORD and DATABASE_URL into the application containers. The connection is tested when the project is created.")}
                      </p>
                    </>
                  ) : (
                    <>
                      <Checkbox
                        label={t("Publish database port on the host")}
                        description={t("Lets you connect from your workstation with TablePlus, DBeaver, etc. The port is assigned automatically.")}
                        checked={form.dbExpose}
                        onChange={(e) => set({ dbExpose: e.target.checked })}
                      />
                      <p className="text-sm text-muted">
                        {t("Envoryx generates secure credentials and injects DB_HOST, DB_DATABASE, DB_USERNAME, DB_PASSWORD and DATABASE_URL into the application containers (PHP, Python, Node). Data lives in a persistent Docker volume.")}
                      </p>
                    </>
                  )}
                </div>
              )}
              <div className="space-y-3">
                <p className="text-sm font-medium text-fg">{t("Additional databases")}</p>
                <p className="text-sm text-muted">{t("Another database server next to the first one – for example PostgreSQL for reporting next to MariaDB. Each is reached at its name as host and injects variables starting with its name (ANALYTICS_DB_HOST, ANALYTICS_DATABASE_URL …).")}</p>
                {form.extraDbs.map((d, i) => {
                  const engine = databases.find((x) => x.key === d.type);
                  return (
                    <div key={i} className="grid gap-3 rounded-md border border-default p-4 sm:grid-cols-[1fr_1fr_1fr_auto] sm:items-end">
                      <Field label={t("Name")} htmlFor={`extra-db-name-${i}`} error={extraDbError(i)}>
                        <Input id={`extra-db-name-${i}`} value={d.name} onChange={(e) => setExtraDb(i, { name: e.target.value.toLowerCase() })} placeholder="analytics" spellCheck={false} autoComplete="off" />
                      </Field>
                      <Field label={t("Type")} htmlFor={`extra-db-type-${i}`}>
                        <Select
                          id={`extra-db-type-${i}`}
                          value={d.type}
                          onChange={(e) => {
                            const next = databases.find((x) => x.key === e.target.value);
                            setExtraDb(i, { type: e.target.value, version: next?.versions.find((v) => v.default)?.version ?? next?.versions[0]?.version ?? "" });
                          }}
                        >
                          {databases
                            .filter((x) => x.available)
                            .map((x) => (
                              <option key={x.key} value={x.key}>
                                {x.name}
                              </option>
                            ))}
                        </Select>
                      </Field>
                      <Field label={t("Version")} htmlFor={`extra-db-version-${i}`}>
                        <Select id={`extra-db-version-${i}`} value={d.version} onChange={(e) => setExtraDb(i, { version: e.target.value })}>
                          {engine?.versions.map((v) => (
                            <option key={v.version} value={v.version}>
                              {v.label}
                            </option>
                          ))}
                        </Select>
                      </Field>
                      <Button variant="ghost" aria-label={t("Remove {{name}}", { name: d.name || t("database") })} onClick={() => set({ extraDbs: form.extraDbs.filter((_, j) => j !== i) })} icon={<Trash2 className="size-4" />} />
                    </div>
                  );
                })}
                <Button
                  size="sm"
                  icon={<Plus className="size-3.5" />}
                  onClick={() => {
                    const pg = databases.find((x) => x.key === "postgresql") ?? databases[0];
                    set({ extraDbs: [...form.extraDbs, { name: "", type: pg?.key ?? "postgresql", version: pg?.versions.find((v) => v.default)?.version ?? "" }] });
                  }}
                >
                  {t("Add a database")}
                </Button>
              </div>
              <div className="space-y-3">
                <p className="text-sm font-medium text-fg">{t("Additional services")}</p>
                <div className="rounded-md border border-default p-4 space-y-3">
                  <Checkbox label="Redis" description={t("Cache and queue backend with a persistent volume. Injects REDIS_HOST, REDIS_PORT and REDIS_URL.")} checked={form.redis} onChange={(e) => set({ redis: e.target.checked })} />
                  {form.redis && (
                    <div className="flex flex-wrap gap-4 pl-7" role="radiogroup" aria-label={t("Where {{service}} runs", { service: "Redis" })}>
                      <label className="inline-flex items-center gap-2 text-sm">
                        <input type="radio" name="redis-where" checked={!form.redisExternal} onChange={() => set({ redisExternal: false })} />
                        {t("In a container of the project")}
                      </label>
                      <label className="inline-flex items-center gap-2 text-sm">
                        <input type="radio" name="redis-where" checked={form.redisExternal} onChange={() => set({ redisExternal: true })} />
                        {t("On an external server")}
                      </label>
                    </div>
                  )}
                  {form.redis && form.redisExternal && (
                    <div className="pl-7">
                      <ExternalRedisFields id="redis-ext" value={form.redisConn} onChange={(redisConn) => set({ redisConn })} />
                    </div>
                  )}
                  {form.redis && !form.redisExternal && (
                    <div className="grid gap-4 pl-7 sm:grid-cols-2">
                      <Field label={t("Redis version")} htmlFor="redis-version">
                        <Select id="redis-version" value={form.redisVersion} onChange={(e) => set({ redisVersion: e.target.value })}>
                          {services
                            .find((s) => s.key === "redis")
                            ?.versions.map((v) => (
                              <option key={v.version} value={v.version}>
                                {v.label}
                              </option>
                            ))}
                        </Select>
                      </Field>
                      <div className="self-end pb-1">
                        <Checkbox label={t("Publish port on the host")} description={t("For RedisInsight etc.")} checked={form.redisExpose} onChange={(e) => set({ redisExpose: e.target.checked })} />
                      </div>
                    </div>
                  )}
                </div>
                <div className="rounded-md border border-default p-4 space-y-3">
                  <Checkbox label="Memcached" description={t("In-memory cache without persistence – a restart empties it. Injects MEMCACHED_HOST, MEMCACHED_PORT and MEMCACHED_URL.")} checked={form.memcached} onChange={(e) => set({ memcached: e.target.checked })} />
                  {form.memcached && (
                    <div className="pl-7">
                      <Checkbox label={t("Publish port on the host")} description={t("For tools on your machine, e.g. telnet or a cache inspector.")} checked={form.memcachedExpose} onChange={(e) => set({ memcachedExpose: e.target.checked })} />
                    </div>
                  )}
                </div>
                <div className="rounded-md border border-default p-4">
                  <Checkbox label="Mailpit" description={t("Catches all outgoing mail and shows it in a web inbox (published on its own port). Injects MAIL_* and MAILER_DSN.")} checked={form.mailpit} onChange={(e) => set({ mailpit: e.target.checked })} />
                </div>
                <div className="rounded-md border border-default p-4 space-y-3">
                  <Checkbox label="RabbitMQ" description={t("Message broker for queues (Symfony Messenger, Laravel queues, Celery) with a persistent volume and a management UI on its own port. Injects RABBITMQ_* including RABBITMQ_URL.")} checked={form.rabbitmq} onChange={(e) => set({ rabbitmq: e.target.checked })} />
                  {form.rabbitmq && (
                    <div className="grid gap-4 pl-7 sm:grid-cols-2">
                      <Field label={t("RabbitMQ version")} htmlFor="rabbitmq-version">
                        <Select id="rabbitmq-version" value={form.rabbitmqVersion} onChange={(e) => set({ rabbitmqVersion: e.target.value })}>
                          {services
                            .find((s) => s.key === "rabbitmq")
                            ?.versions.map((v) => (
                              <option key={v.version} value={v.version}>
                                {v.label}
                              </option>
                            ))}
                        </Select>
                      </Field>
                      <div className="self-end pb-1">
                        <Checkbox label={t("Publish port on the host")} description={t("For AMQP clients running on your machine.")} checked={form.rabbitmqExpose} onChange={(e) => set({ rabbitmqExpose: e.target.checked })} />
                      </div>
                    </div>
                  )}
                </div>
                <div className="rounded-md border border-default p-4">
                  <Checkbox label="Meilisearch" description={t("Search engine (Laravel Scout, Symfony) with a persistent volume and a web dashboard on its own port. Injects MEILISEARCH_HOST/KEY and MEILISEARCH_URL/API_KEY; set SCOUT_DRIVER yourself.")} checked={form.meilisearch} onChange={(e) => set({ meilisearch: e.target.checked })} />
                </div>
                <div className="rounded-md border border-default p-4 space-y-3">
                  <Checkbox label="Typesense" description={t("Search engine (Laravel Scout, InstantSearch) with a persistent volume. Injects TYPESENSE_HOST, TYPESENSE_PORT, TYPESENSE_PROTOCOL, TYPESENSE_API_KEY and TYPESENSE_URL; set SCOUT_DRIVER yourself.")} checked={form.typesense} onChange={(e) => set({ typesense: e.target.checked })} />
                  {form.typesense && (
                    <div className="pl-7">
                      <Checkbox label={t("Publish port on the host")} description={t("For clients and dashboards running on your machine.")} checked={form.typesenseExpose} onChange={(e) => set({ typesenseExpose: e.target.checked })} />
                    </div>
                  )}
                </div>
                <div className="rounded-md border border-default p-4 space-y-3">
                  <Checkbox label="OpenSearch" description={t("Elasticsearch-compatible search engine as a single node with a persistent volume, plain HTTP without login. Injects OPENSEARCH_HOST, OPENSEARCH_PORT, OPENSEARCH_SCHEME and OPENSEARCH_URL. Needs about 1 GB of RAM.")} checked={form.opensearch} onChange={(e) => set({ opensearch: e.target.checked })} />
                  {form.opensearch && (
                    <div className="grid gap-4 pl-7 sm:grid-cols-2">
                      <Field label={t("OpenSearch version")} htmlFor="opensearch-version">
                        <Select id="opensearch-version" value={form.opensearchVersion} onChange={(e) => set({ opensearchVersion: e.target.value })}>
                          {services
                            .find((s) => s.key === "opensearch")
                            ?.versions.map((v) => (
                              <option key={v.version} value={v.version}>
                                {v.label}
                              </option>
                            ))}
                        </Select>
                      </Field>
                      <div className="self-end pb-1">
                        <Checkbox label={t("Publish port on the host")} description={t("For clients and dashboards running on your machine.")} checked={form.opensearchExpose} onChange={(e) => set({ opensearchExpose: e.target.checked })} />
                      </div>
                      <div className="sm:col-span-2">
                        <Checkbox label="OpenSearch Dashboards" description={t("Web UI with the Dev Tools console, index management and Discover, on its own port. The image is about 2.6 GB and needs roughly 400 MB of RAM.")} checked={form.opensearchDashboards} onChange={(e) => set({ opensearchDashboards: e.target.checked })} />
                      </div>
                    </div>
                  )}
                </div>
                <div className="rounded-md border border-default p-4 space-y-3">
                  <Checkbox label="Ollama" description={t("Runs language and embedding models locally (Laravel Prism, LangChain, the Ollama libraries). Injects OLLAMA_HOST, OLLAMA_BASE_URL and OLLAMA_URL. Models live in one store shared by all projects; download them in the Services tab.")} checked={form.ollama} onChange={(e) => set({ ollama: e.target.checked })} />
                  {form.ollama && (
                    <div className="space-y-3 pl-7">
                      <Checkbox label={t("Use the GPU")} description={t("Hands the host's NVIDIA GPUs to Ollama. Docker needs the NVIDIA Container Toolkit for it (on Unraid: the Nvidia Driver plugin); Envoryx checks that before switching.")} checked={form.ollamaGpu} onChange={(e) => set({ ollamaGpu: e.target.checked })} />
                      <Checkbox label={t("Publish port on the host")} description={t("For clients and dashboards running on your machine.")} checked={form.ollamaExpose} onChange={(e) => set({ ollamaExpose: e.target.checked })} />
                    </div>
                  )}
                </div>
                <div className="rounded-md border border-default p-4">
                  <Checkbox label={t("Object storage (S3)")} description={t("S3-compatible object storage with a bucket for this project and a web console. Injects S3_* and the AWS_* variables Laravel and the AWS SDKs read.")} checked={form.storage} onChange={(e) => set({ storage: e.target.checked })} />
                </div>
              </div>
            </div>
          )}

          {step === 4 && (
            <div className="space-y-4">
              <p className="text-sm text-muted">{t("Variables are available to all containers of this project (e.g. getenv() in PHP, os.environ in Python, process.env in Node). Mark secrets to mask them in the UI.")}</p>
              <EnvEditor value={form.env} onChange={(env) => set({ env })} services={wizardServices} exportName={slugify(form.name) || "project"} />
            </div>
          )}

          {step === 5 && (
            <div className="space-y-5">
              {previewError ? (
                <Alert tone="red" title={t("Cannot create this project")}>
                  {previewError}
                </Alert>
              ) : !preview ? (
                <Spinner label={t("Calculating plan…")} />
              ) : (
                <>
                  {preview.warnings.length > 0 && (
                    <Alert tone="amber">
                      <ul className="list-disc pl-4">
                        {preview.warnings.map((w, i) => (
                          <li key={i}>{w}</li>
                        ))}
                      </ul>
                    </Alert>
                  )}
                  <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
                    <dt className="text-muted">{t("Identifier")}</dt>
                    <dd className="font-mono text-xs">{preview.slug}</dd>
                    {selectedTemplate && (
                      <>
                        <dt className="text-muted">{t("Template")}</dt>
                        <dd className="text-xs">
                          {selectedTemplate.name}
                          <span className="block text-subtle">{selectedTemplate.description}</span>
                          {selectedTemplate.notes && <span className="block text-subtle">{selectedTemplate.notes}</span>}
                        </dd>
                      </>
                    )}
                    {form.importSite && (
                      <>
                        <dt className="text-muted">{t("Website")}</dt>
                        <dd className="text-xs">
                          {form.importSite.siteName}
                          {form.importSite.dumpName && <> + {form.importSite.dumpName}</>}
                          <span className="block text-subtle">
                            {form.importSite.dumpName ? t("The files are unpacked into the project directory and the dump is imported into the project database.") : t("The files are unpacked into the project directory.")}
                          </span>
                        </dd>
                      </>
                    )}
                    <dt className="text-muted">{t("Files")}</dt>
                    <dd className="font-mono text-xs">
                      {preview.path} <span className="text-subtle">({t("host")}: {preview.hostPath})</span>
                    </dd>
                    <dt className="text-muted">{t("URL")}</dt>
                    <dd className="font-mono text-xs">
                      {(() => {
                        // The preview carries no service list; the links hook only reads it for the
                        // application container's host port. Behind a Python server or Node dev server the
                        // HTTP port stays unpublished, so the planned container's host port stands in as a
                        // synthetic service – then the hook's own branch applies, also when the proxy is off
                        // and the direct URL is all there is.
                        const appPort = Number(preview.containers.find((c) => c.service === previewServes)?.ports[0]?.split(" ")[0]) || 0;
                        const services: Project["services"] =
                          previewServes === "node"
                            ? [{ kind: "node", variant: "node", version: "", image: "", enabled: true, config: { devServer: true, hostPort: appPort } }]
                            : previewServes === "python" || previewServes === "go"
                              ? [{ kind: previewServes, variant: previewServes, version: "", image: "", enabled: true, config: { server: true, hostPort: appPort } }]
                              : [];
                        const l = links({ httpPort: preview.httpPort, hostnames: [`${preview.slug}.${settings.data?.baseDomain ?? "test"}`], serves: previewServes, services });
                        return !l.direct || l.url === l.direct ? l.url : `${l.url} · ${l.direct}`;
                      })()}
                    </dd>
                    {previewServes === "node" ? (
                      <>
                        <dt className="text-muted">{t("Serves")}</dt>
                        <dd className="text-xs">{t("Node dev server (the HTTP port stays unpublished)")}</dd>
                      </>
                    ) : (
                      <>
                        {previewServes === "python" && (
                          <>
                            <dt className="text-muted">{t("Serves")}</dt>
                            <dd className="text-xs">{t("Python application server (the HTTP port stays unpublished)")}</dd>
                          </>
                        )}
                        {previewServes === "go" && (
                          <>
                            <dt className="text-muted">{t("Serves")}</dt>
                            <dd className="text-xs">{t("Go server (the HTTP port stays unpublished)")}</dd>
                          </>
                        )}
                        {devUrl && (
                          <>
                            <dt className="text-muted">{t("Dev server URL")}</dt>
                            <dd className="font-mono text-xs">{devUrl}</dd>
                          </>
                        )}
                      </>
                    )}
                    <dt className="text-muted">{t("Network")}</dt>
                    <dd className="font-mono text-xs">{preview.network}</dd>
                  </dl>
                  <div>
                    <p className="mb-2 text-sm font-medium text-fg">{t("Containers")}</p>
                    <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
                      {preview.containers.map((c) => (
                        <li key={c.name} className="px-3 py-2 text-xs">
                          <div className="flex items-center justify-between gap-3">
                            <span className="font-mono font-medium text-fg">{c.name}</span>
                            <span className="font-mono text-subtle">{c.image}</span>
                          </div>
                          <ul className="mt-1 space-y-0.5 font-mono text-[11px] text-muted">
                            {c.ports.map((p) => (
                              <li key={p}>{t("port")} {p}</li>
                            ))}
                            {c.mounts.map((m) => (
                              <li key={m} className="truncate">
                                {t("mount")} {m}
                              </li>
                            ))}
                          </ul>
                        </li>
                      ))}
                    </ul>
                  </div>
                  {preview.volumes.length > 0 && (
                    <p className="text-sm text-muted">
                      {t("Volumes")}: <Code>{preview.volumes.join(", ")}</Code>
                    </p>
                  )}
                  <div className="space-y-3 border-t border-default pt-4">
                    {form.importing ? null : form.gitUrl.trim() ? (
                      <p className="text-sm text-muted">
                        {t("Repository {{url}} will be cloned into the project directory.", { url: form.gitUrl.trim() })}
                        {form.useManifest && <> {t("If it brings an envoryx.yml, that file replaces the services chosen here.")}</>}
                      </p>
                    ) : (
                      serves !== "node" &&
                      serves !== "python" &&
                      serves !== "go" && (
                        <Checkbox label={form.phpEnabled ? t("Create starter index.php") : t("Create starter index.html")} description={t("Only if the document root is empty.")} checked={form.createStarter} onChange={(e) => set({ createStarter: e.target.checked })} />
                      )
                    )}
                    <Checkbox label={t("Start project after creation")} checked={form.start} onChange={(e) => set({ start: e.target.checked })} />
                  </div>
                  {create.isPending && <CreateProgress slug={slugify(form.name)} />}
                  {submitError && (
                    <Alert tone="red" title={t("Creation failed")}>
                      {submitError}
                    </Alert>
                  )}
                </>
              )}
            </div>
          )}

          <div className="mt-8 flex items-center justify-between border-t border-default pt-4">
            <Button variant="ghost" onClick={() => (step === 0 ? navigate("/projects") : setStep(step - 1))} icon={<ArrowLeft className="size-4" />} disabled={create.isPending}>
              {step === 0 ? t("Cancel") : t("Back")}
            </Button>
            {step < steps.length - 1 ? (
              <Button variant="primary" onClick={() => setStep(step + 1)} disabled={!canContinue} icon={<ArrowRight className="size-4" />}>
                {t("Continue")}
              </Button>
            ) : (
              <Button variant="primary" onClick={submit} loading={create.isPending} disabled={!preview || !!previewError} icon={<Rocket className="size-4" />}>
                {t("Create project")}
              </Button>
            )}
          </div>
        </Card>
      </div>
    </div>
  );
}
