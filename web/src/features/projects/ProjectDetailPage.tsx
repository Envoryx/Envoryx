import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { ResourcesTab } from "./ResourcesTab";
import { HealthCheckCard } from "./HealthCheckCard";
import { Copy, Pencil, Trash2, Save, ExternalLink, Undo2, RotateCw } from "lucide-react";
import { lazy, Suspense, useEffect, useState, type ReactElement } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { ApiError } from "@/api/client";
import { useDevServerLink, useImageChoice, useProject, useProjectLinks, useProjectPlan, useProjectStats, useRuntimes, useSettings, useUpdateProject } from "@/api/hooks";
import { appKindOf, defaultNodePresets, defaultPythonPresets, servesOf, type EnvVar, type NodeConfig, type PHPConfig, type Project, type PythonConfig, type UpdateProjectRequest, type WebServerConfig } from "@/api/types";
import { NodeDevServerFields, defaultScript, devServerRequest, type DevServerForm } from "./NodeDevServerFields";
import { PythonServerFields, defaultPythonServerForm, pythonServerRequest, type PythonServerForm } from "./PythonServerFields";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, ErrorState, Field, Input, PageHeader, Select, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone, formatBytes, formatDateTime, formatPercent, serviceLabel, stateMeta } from "@/lib/format";
import { DeleteProjectDialog, DuplicateProjectDialog, ProjectActionButtons, RenameProjectDialog, useActionError } from "./ProjectActions";
import { OperationHint } from "@/components/OperationsTray";
import { DatabaseTab } from "./DatabaseTab";
import { ShareButton } from "./ShareButton";
import { EnvEditor } from "./EnvEditor";
import { GitTab } from "./GitTab";
import { DomainsTab } from "./DomainsTab";
import { WorkersTab } from "./WorkersTab";
import { CronTab } from "./CronTab";
import { IdeTab } from "./IdeTab";
import { ServicesTab } from "./ServicesTab";
import { BackupsTab } from "./BackupsTab";
import { LogsTab } from "./LogsTab";
// xterm.js is only needed on this tab; keep it out of the main bundle.
const TerminalTab = lazy(() => import("./TerminalTab").then((m) => ({ default: m.TerminalTab })));
const ActionsTab = lazy(() => import("./ActionsTab").then((m) => ({ default: m.ActionsTab })));
const TestsTab = lazy(() => import("./TestsTab").then((m) => ({ default: m.TestsTab })));
import { PhpConfigForm } from "./PhpConfigForm";
import { webServerHint } from "./webServers";
import { AuditLog } from "@/features/audit/AuditLog";
import { errorText, translateMessage } from "@/lib/errors";

const tabs = ["Overview", "Resources", "Domains", "Git", "Actions", "Tests", "Terminal", "Logs", "Runtime", "Workers", "Cron", "Database", "Services", "Backups", "Environment", "IDE", "History", "Advanced"] as const;
type Tab = (typeof tabs)[number];

export function ProjectDetailPage() {
  const { t } = useTranslation();
  const { id = "" } = useParams();
  const q = useProject(id);
  const links = useProjectLinks();
  const devLink = useDevServerLink();
  const [params] = useSearchParams();
  const [tab, setTab] = useState<Tab>(() => {
    const requested = params.get("tab");
    return tabs.includes(requested as Tab) ? (requested as Tab) : "Overview";
  });
  const [deleting, setDeleting] = useState(false);
  const [duplicating, setDuplicating] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const { error, capture, setError } = useActionError();

  if (q.isPending) return <Spinner />;
  if (q.isError) {
    const notFound = q.error instanceof ApiError && q.error.status === 404;
    return <ErrorState title={notFound ? t("Project not found") : t("Could not load project")} message={notFound ? undefined : errorText(q.error, t)} action={<Link to="/projects" className="text-sm underline">{t("Back to projects")}</Link>} />;
  }
  const p = q.data;
  const meta = stateMeta[p.status.state];
  const serves = p.serves ?? servesOf(p);
  const { url } = links(p);
  const devUrl = devLink(p);

  return (
    <div>
      <PageHeader
        title={
          <span className="flex items-center gap-3">
            <StatusDot tone={meta.tone} pulse={meta.pulse ?? false} />
            {p.name}
            <Badge tone={meta.tone}>{t(meta.label)}</Badge>
          </span>
        }
        description={
          <span className="flex flex-col gap-1">
            {p.status.operation && <OperationHint op={p.status.operation} />}
            <span className="font-mono text-xs">
              /projects/{p.path}
              {p.docroot ? `/${p.docroot}` : ""} ·{" "}
              {url ? (
                <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 hover:underline">
                  {url} <ExternalLink className="size-3" />
                </a>
              ) : (
                t("no port")
              )}
              {devUrl && devUrl !== url && (
                <>
                  {/* Behind a Node dev server the project URL already is the dev server; the -dev name is only an
                      alias – and without the proxy both resolve to the same host port, so it is not repeated. */}
                  {` · ${serves === "node" ? t("dev alias") : "dev"}: `}
                  <a href={devUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 hover:underline">
                    {devUrl} <ExternalLink className="size-3" />
                  </a>
                </>
              )}
            </span>
          </span>
        }
        actions={
          <>
            <ProjectActionButtons project={p} size="md" onError={capture} />
            <ShareButton project={p} />
            <Button variant="ghost" onClick={() => setRenaming(true)} icon={<Pencil className="size-4" />} aria-label={t("Rename project")} title={t("Rename project – identifier, URL and containers follow")} />
            <Button variant="ghost" onClick={() => setDuplicating(true)} icon={<Copy className="size-4" />} aria-label={t("Duplicate project")} title={t("Duplicate project – config, files and database")} />
            <Button variant="ghost" onClick={() => setDeleting(true)} icon={<Trash2 className="size-4" />} aria-label={t("Delete project")} title={t("Delete project")} />
          </>
        }
      />

      {error && (
        <div className="mb-4">
          <Alert tone="red">
            {error}
            <button className="ml-2 underline" onClick={() => setError(null)}>
              {t("dismiss")}
            </button>
          </Alert>
        </div>
      )}
      {p.status.warnings.length > 0 && (
        <div className="mb-4">
          <Alert tone={p.status.state === "error" ? "red" : "amber"}>
            <ul className="list-disc pl-4">
              {p.status.warnings.map((w, i) => (
                <li key={i}>{translateMessage(w, t)}</li>
              ))}
            </ul>
          </Alert>
        </div>
      )}

      <div className="mb-4 flex gap-1 border-b border-default" role="tablist">
        {tabs.map((name) => (
          <button
            key={name}
            role="tab"
            aria-selected={tab === name}
            onClick={() => setTab(name)}
            className={clsx("-mb-px border-b-2 px-3 py-2 text-sm font-medium", tab === name ? "border-accent-500 text-fg" : "border-transparent text-muted hover:text-fg")}
          >
            {t(name)}
          </button>
        ))}
      </div>

      {tab === "Overview" && <OverviewTab project={p} />}
      {tab === "Domains" && <DomainsTab project={p} />}
      {tab === "Git" && <GitTab project={p} />}
      {tab === "Actions" && (
        <Suspense fallback={<Spinner label={t("Loading actions…")} />}>
          <ActionsTab project={p} />
        </Suspense>
      )}
      {tab === "Tests" && (
        <Suspense fallback={<Spinner />}>
          <TestsTab project={p} />
        </Suspense>
      )}
      {tab === "Terminal" && (
        <Suspense fallback={<Spinner label={t("Loading terminal…")} />}>
          <TerminalTab project={p} />
        </Suspense>
      )}
      {tab === "Logs" && <LogsTab project={p} />}
      {tab === "Runtime" && (
        <div className="space-y-6">
          <ProjectSettingsCard project={p} onRename={() => setRenaming(true)} />
          {/* The application runtime comes first (PHP, else Python, else Node), then the web server, then the other runtimes as toolchains. */}
          {(() => {
            const app = p.appService ?? appKindOf(p);
            const cards: Record<"php" | "python" | "node", ReactElement> = { php: <PhpCard key="php" project={p} />, python: <PythonCard key="python" project={p} />, node: <NodeCard key="node" project={p} /> };
            const [first, ...rest]: ("php" | "python" | "node")[] = app === "node" ? ["node", "python", "php"] : app === "python" ? ["python", "node", "php"] : ["php", "node", "python"];
            return (
              <>
                {first && cards[first]}
                <WebServerCard project={p} />
                {rest.map((k) => cards[k])}
              </>
            );
          })()}
        </div>
      )}
      {tab === "Workers" && <WorkersTab project={p} />}
      {tab === "Cron" && <CronTab project={p} />}
      {tab === "Database" && <DatabaseTab project={p} />}
      {tab === "Services" && <ServicesTab project={p} />}
      {tab === "Backups" && <BackupsTab project={p} />}
      {tab === "Environment" && <EnvTab project={p} />}
      {tab === "IDE" && <IdeTab project={p} />}
      {tab === "Resources" && <ResourcesTab project={p} />}
      {tab === "History" && (
        <Card>
          <CardHeader title={t("History")} description={t("Everything done to this project, newest first; open an entry to see what a change changed.")} />
          <div className="pt-4">
            <AuditLog project={p.id} />
          </div>
        </Card>
      )}
      {tab === "Advanced" && <AdvancedTab project={p} />}

      <RenameProjectDialog project={p} open={renaming} onClose={() => setRenaming(false)} />
      <DuplicateProjectDialog project={p} open={duplicating} onClose={() => setDuplicating(false)} />
      <DeleteProjectDialog project={p} open={deleting} onClose={() => setDeleting(false)} />
    </div>
  );
}

function OverviewTab({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const stats = useProjectStats(p.id, p.status.state === "running" || p.status.state === "partial");
  const imageChoice = useImageChoice(p.id);
  const [imageMsg, setImageMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const chooseImage = (image: string, use: "previous" | "latest") => {
    setImageMsg(null);
    imageChoice.mutate(
      { image, use },
      {
        onSuccess: () => setImageMsg({ tone: "green", text: use === "previous" ? t("Rolled back to the previous image.") : t("Back on the current image.") }),
        onError: (err) => setImageMsg({ tone: "red", text: errorText(err, t, t("Changing the image failed")) }),
      },
    );
  };
  return (
    <div className="grid gap-6 lg:grid-cols-3">
      <Card className="lg:col-span-2">
        <CardHeader title={t("Services")} description={t("One container per service, connected through the private project network.")} />
        {imageMsg && (
          <div className="px-5 pt-4">
            <Alert tone={imageMsg.tone}>{imageMsg.text}</Alert>
          </div>
        )}
        <ul className="divide-y divide-[var(--border)]">
          {p.status.services.map((s) => (
            <li key={s.kind === "worker" ? `worker-${s.workerId}` : s.kind} className="flex flex-wrap items-center gap-x-6 gap-y-2 px-5 py-3">
              <div className="flex min-w-[10rem] items-center gap-2.5">
                <StatusDot tone={containerStateTone(s.state)} />
                <div>
                  <p className="text-sm font-medium text-fg">{serviceLabel(s.kind, s.version, s.variant)}</p>
                  {s.containerName && <p className="font-mono text-[11px] text-subtle">{s.containerName}</p>}
                </div>
              </div>
              <Badge tone={containerStateTone(s.state)}>{s.exists || s.state === "external" ? s.state : t("missing")}</Badge>
              {s.health && <Badge tone={s.health === "healthy" ? "green" : s.health === "starting" ? "blue" : "red"}>{s.health}</Badge>}
              <span className="font-mono text-xs text-muted">{s.image}</span>
              {s.ports.map((port) => (
                <span key={port.hostPort} className="font-mono text-xs text-muted">
                  :{port.hostPort} → {port.containerPort}
                </span>
              ))}
              {s.imagePinned ? (
                <span className="inline-flex items-center gap-2 text-xs">
                  <Badge tone="amber">{t("previous image")}</Badge>
                  <Button size="sm" variant="ghost" icon={<RotateCw className="size-3.5" />} loading={imageChoice.isPending} onClick={() => chooseImage(s.image, "latest")}>
                    {t("Use current image")}
                  </Button>
                </span>
              ) : (
                s.imagePrevious && (
                  <span className="inline-flex items-center gap-2 text-xs text-muted">
                    {s.imageChangedAt && t("image updated {{date}}", { date: formatDateTime(s.imageChangedAt) })}
                    <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} loading={imageChoice.isPending} onClick={() => chooseImage(s.image, "previous")}>
                      {t("Roll back")}
                    </Button>
                  </span>
                )
              )}
              {s.containerId && <span className="ml-auto font-mono text-[11px] text-subtle">{s.containerId.slice(0, 12)}</span>}
            </li>
          ))}
        </ul>
      </Card>
      <div className="space-y-6">
        <HealthCheckCard project={p} />
        <Card>
          <CardHeader title={t("Resources")} />
          <dl className="grid grid-cols-2 gap-4 px-5 py-4 text-sm">
            <div>
              <dt className="text-xs text-subtle">{t("CPU")}</dt>
              <dd className="text-lg font-semibold tabular-nums">{stats.data ? formatPercent(stats.data.stats.cpuPercent) : "—"}</dd>
            </div>
            <div>
              <dt className="text-xs text-subtle">{t("Memory")}</dt>
              <dd className="text-lg font-semibold tabular-nums">{stats.data ? formatBytes(stats.data.stats.memoryBytes) : "—"}</dd>
            </div>
          </dl>
        </Card>
        <Card>
          <CardHeader title={t("Details")} />
          <dl className="space-y-2 px-5 py-4 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-muted">{t("Identifier")}</dt>
              <dd className="font-mono text-xs">{p.slug}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">{t("Network")}</dt>
              <dd className="font-mono text-xs">envoryx-{p.slug}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">{t("Desired state")}</dt>
              <dd>{t(p.desiredState)}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">{t("Created")}</dt>
              <dd>{formatDateTime(p.createdAt)}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">{t("Updated")}</dt>
              <dd>{formatDateTime(p.updatedAt)}</dd>
            </div>
          </dl>
        </Card>
      </div>
    </div>
  );
}

function useSaveFeedback() {
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  useEffect(() => {
    if (!msg || msg.tone !== "green") return;
    const t = setTimeout(() => setMsg(null), 4000);
    return () => clearTimeout(t);
  }, [msg]);
  return { msg, setMsg };
}

/** Name and document root – every project has them, whatever runs behind the web server. */
function ProjectSettingsCard({ project: p, onRename }: { project: Project; onRename: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateProject(p.id);
  const { msg, setMsg } = useSaveFeedback();
  const [name, setName] = useState(p.name);
  const [docroot, setDocroot] = useState(p.docroot);
  const serves = p.serves ?? servesOf(p);
  const dirty = name !== p.name || docroot !== p.docroot;

  const save = () => {
    setMsg(null);
    update.mutate(
      { name, docroot },
      {
        onSuccess: () => setMsg({ tone: "green", text: p.status.state === "running" ? t("Saved and applied. Containers were restarted.") : t("Saved. Changes apply on next start.") }),
        onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
      },
    );
  };

  return (
    <Card>
      <CardHeader
        title={t("Project settings")}
        description={t("Changing the document root rewrites the web server configuration and restarts the containers.")}
        actions={
          <Button variant="primary" onClick={save} loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-6 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field
            label={t("Project name")}
            htmlFor="p-name"
            hint={t("The displayed name only; the identifier {{slug}} and with it the URL, the containers and the directory stay.", { slug: p.slug })}
          >
            <Input id="p-name" value={name} onChange={(e) => setName(e.target.value)} />
            <button type="button" className="mt-1 text-xs text-accent-500 underline" onClick={onRename}>
              {t("Rename the project including its identifier…")}
            </button>
          </Field>
          <Field
            label={t("Document root")}
            htmlFor="p-docroot"
            hint={
              serves === "node"
                ? t("Not used while the dev server serves the app; the build output (e.g. dist/) once you turn it off.")
                : serves === "python"
                  ? t("Not used while the application server serves the app; static files (e.g. a collected static/ folder) once you turn it off.")
                  : t("Relative to the project directory")
            }
          >
            <Input id="p-docroot" value={docroot} onChange={(e) => setDocroot(e.target.value)} placeholder={t("(project root)")} />
          </Field>
        </div>
      </div>
    </Card>
  );
}

function PhpCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const runtimes = useRuntimes();
  const update = useUpdateProject(p.id);
  const { msg, setMsg } = useSaveFeedback();
  const svc = p.services.find((s) => s.kind === "php");
  const php = runtimes.data?.runtimes.find((r) => r.key === "php");
  const defaultVersion = php?.versions.find((v) => v.default)?.version ?? php?.versions[0]?.version ?? "";
  const [enabled, setEnabled] = useState(!!svc);
  const [version, setVersion] = useState(svc?.version ?? "");
  const [config, setConfig] = useState<PHPConfig | null>((svc?.config as unknown as PHPConfig) ?? null);
  const settings = useSettings();
  const projectsHost = settings.data?.hostPath ? (settings.data.hostPath.overrides[settings.data.projectsDir] ?? settings.data.hostPath.detected[settings.data.projectsDir]) : undefined;
  const hostDir = projectsHost ? `${projectsHost}/${p.path}` : undefined;
  // A project without PHP starts from the catalogue defaults once PHP is switched on.
  useEffect(() => {
    if (!svc && !version) setVersion(defaultVersion);
    if (!svc && !config && runtimes.data) setConfig(runtimes.data.phpDefaults);
  }, [svc, version, config, defaultVersion, runtimes.data]);

  if (runtimes.isPending) return <Spinner />;
  const dirty = enabled !== !!svc || (enabled && !!svc && (version !== svc.version || JSON.stringify(config) !== JSON.stringify(svc.config)));

  const save = () => {
    setMsg(null);
    const cfg = config ?? runtimes.data?.phpDefaults;
    const body: UpdateProjectRequest = enabled && cfg ? { php: { enabled: true, version, config: cfg } } : { php: { enabled: false } };
    update.mutate(body, {
      onSuccess: () =>
        setMsg({
          tone: "green",
          text: !enabled
            ? t("PHP removed. The web server now serves the document root statically; PHP workers pause until PHP is back.")
            : !svc
              ? t("PHP added. The web server forwards PHP requests to the new container.")
              : p.status.state === "running"
                ? t("Saved and applied. Containers were restarted.")
                : t("Saved. Changes apply on next start."),
        }),
      onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
    });
  };

  return (
    <Card>
      <CardHeader
        title={t("PHP")}
        description={
          svc
            ? t("Changing the PHP version recreates the PHP container; configuration changes restart it. Removing PHP keeps the files and worker definitions.")
            : t("Add PHP-FPM to this project: the web server then forwards PHP requests to it and PHP becomes the application. The SPA fallback of the static setup is dropped.")
        }
        actions={
          <Button variant="primary" onClick={save} loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-6 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Checkbox label={t("Enable PHP")} description={t("Runs PHP-FPM in its own container. Disable for Node-only or static projects.")} checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        {enabled && (
          <>
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label={t("PHP version")} htmlFor="p-version">
                <Select id="p-version" value={version} onChange={(e) => setVersion(e.target.value)}>
                  {php?.versions.map((v) => (
                    <option key={v.version} value={v.version}>
                      {v.label}
                      {v.eol ? t(" (end of life)") : v.preview ? t(" (preview)") : ""}
                    </option>
                  ))}
                </Select>
              </Field>
            </div>
            {config && <PhpConfigForm value={config} onChange={setConfig} extensions={runtimes.data?.phpExtensions ?? []} hostname={p.hostnames[0]} projectDir={hostDir} />}
          </>
        )}
      </div>
    </Card>
  );
}

function WebServerCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const runtimes = useRuntimes();
  const update = useUpdateProject(p.id);
  const { msg, setMsg } = useSaveFeedback();
  const svc = p.services.find((s) => s.kind === "web");
  const serves = p.serves ?? servesOf(p);
  const stored = (svc?.config ?? {}) as WebServerConfig;
  const [type, setType] = useState(svc?.variant ?? "caddy");
  const [version, setVersion] = useState(svc?.version ?? "");
  const [spa, setSpa] = useState(!!stored.spaFallback);

  if (!svc) return null;
  if (runtimes.isPending) return <Spinner />;
  const servers = runtimes.data?.runtimes.filter((r) => r.kind === "webserver" && r.available) ?? [];
  const selected = servers.find((r) => r.key === type);
  const dirty = type !== svc.variant || version !== svc.version || spa !== !!stored.spaFallback;

  const save = () => {
    setMsg(null);
    // The backend rejects spaFallback for projects with PHP, so it is only sent for static ones.
    update.mutate(
      { web: serves === "static" ? { type, version, spaFallback: spa } : { type, version } },
      {
        onSuccess: () => setMsg({ tone: "green", text: p.status.state === "running" ? t("Saved and applied. Containers were restarted.") : t("Saved. Changes apply on next start.") }),
        onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
      },
    );
  };

  return (
    <Card>
      <CardHeader
        title={t("Web server")}
        description={t("Switching the web server recreates the web container; the document root and port stay the same.")}
        actions={
          <Button variant="primary" onClick={save} loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("Web server")} htmlFor="web-type">
            <Select
              id="web-type"
              value={type}
              onChange={(e) => {
                const next = servers.find((r) => r.key === e.target.value);
                setType(e.target.value);
                setVersion(next?.versions.find((v) => v.default)?.version ?? next?.versions[0]?.version ?? "");
              }}
            >
              {servers.map((r) => (
                <option key={r.key} value={r.key}>
                  {r.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("Version")} htmlFor="web-version">
            <Select id="web-version" value={version} onChange={(e) => setVersion(e.target.value)}>
              {selected?.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {serves === "static" && (
          <Checkbox label={t("SPA fallback to index.html")} description={t("Unknown paths return index.html so client-side routers work after a reload.")} checked={spa} onChange={(e) => setSpa(e.target.checked)} />
        )}
        <p className="text-sm text-muted">{webServerHint(t, type, serves)}</p>
      </div>
    </Card>
  );
}

function NodeCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const runtimes = useRuntimes();
  const update = useUpdateProject(p.id);
  const devLink = useDevServerLink();
  const { msg, setMsg } = useSaveFeedback();
  const serves = p.serves ?? servesOf(p);
  const svc = p.services.find((s) => s.kind === "node" && s.enabled);
  const node = runtimes.data?.runtimes.find((r) => r.key === "node");
  const stored = (svc?.config ?? {}) as NodeConfig;
  const fromStored = (): DevServerForm => ({
    devServer: !!stored.devServer,
    mode: stored.mode ?? "dev",
    packageManager: stored.packageManager ?? "npm",
    script: stored.script ?? defaultScript(stored.mode ?? "dev", stored.preset ?? "vite"),
    buildScript: stored.buildScript ?? "build",
    port: String(stored.port ?? 5173),
    preset: stored.preset ?? "vite",
    inspect: !!stored.inspect,
    inspectPort: String(stored.inspectPort ?? 9229),
  });
  const [enabled, setEnabled] = useState(!!svc);
  const [version, setVersion] = useState(svc?.version ?? "");
  const [dev, setDev] = useState<DevServerForm>(fromStored);
  useEffect(() => {
    setEnabled(!!svc);
    setVersion(svc?.version ?? node?.versions.find((v) => v.default)?.version ?? "");
    setDev(fromStored());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [svc, node]);
  const dirty = enabled !== !!svc || (enabled && (version !== (svc?.version ?? "") || JSON.stringify(dev) !== JSON.stringify(fromStored())));
  const url = devLink(p);
  const nodeStatus = p.status.services.find((s) => s.kind === "node");

  return (
    <Card>
      <CardHeader
        title={t("Node.js")}
        description={
          serves !== "php"
            ? t("Application runtime of this project: run your dev server here or build static assets served by the web server. Removing it only removes the container; node_modules stays in the project directory.")
            : t("Toolchain container for asset builds (npm, pnpm, yarn), optionally running your dev server. Removing it only removes the container; node_modules stays in the project directory.")
        }
        actions={
          <Button
            variant="primary"
            icon={<Save className="size-4" />}
            loading={update.isPending}
            disabled={!dirty}
            onClick={() =>
              update.mutate(
                { node: enabled ? { enabled: true, version, ...devServerRequest(dev) } : { enabled: false } },
                {
                  onSuccess: () => setMsg({ tone: "green", text: enabled ? t("Node.js container updated.") : t("Node.js container removed.") }),
                  onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
                },
              )
            }
          >
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {stored.devServer && (
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-default px-3 py-2 text-sm">
            <span className="text-muted">{t("Dev server")}</span>
            {nodeStatus && (
              <span className="inline-flex items-center gap-1.5 text-xs">
                <StatusDot tone={containerStateTone(nodeStatus.state)} /> {nodeStatus.state}
              </span>
            )}
            {url ? (
              <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
                {url} <ExternalLink className="size-3" />
              </a>
            ) : (
              <span className="text-xs text-subtle">{t("no port")}</span>
            )}
            {stored.hostPort ? <span className="font-mono text-xs text-subtle">{t("host port {{port}}", { port: stored.hostPort })}</span> : null}
            {stored.mode === "production" && <Badge tone="blue">{t("production build")}</Badge>}
            {stored.inspect && stored.inspectHostPort ? <span className="font-mono text-xs text-subtle">{t("inspector on host port {{port}}", { port: stored.inspectHostPort })}</span> : null}
          </div>
        )}
        <Checkbox label={t("Enable Node.js")} checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        {enabled && node && (
          <Field label={t("Node.js version")} htmlFor="node-version">
            <Select id="node-version" value={version} onChange={(e) => setVersion(e.target.value)}>
              {node.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                  {v.eol ? t(" (end of life)") : v.preview ? t(" (preview)") : ""}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {enabled && <NodeDevServerFields value={dev} onChange={setDev} presets={runtimes.data?.nodePresets ?? defaultNodePresets} primary={serves !== "php"} />}
      </div>
    </Card>
  );
}

function PythonCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const runtimes = useRuntimes();
  const update = useUpdateProject(p.id);
  const links = useProjectLinks();
  const { msg, setMsg } = useSaveFeedback();
  const serves = p.serves ?? servesOf(p);
  const svc = p.services.find((s) => s.kind === "python" && s.enabled);
  const python = runtimes.data?.runtimes.find((r) => r.key === "python");
  const stored = (svc?.config ?? {}) as PythonConfig;
  const fromStored = (): PythonServerForm => ({
    server: !!stored.server,
    mode: stored.mode ?? "dev",
    preset: stored.preset ?? defaultPythonServerForm.preset,
    app: stored.app ?? defaultPythonServerForm.app,
    port: String(stored.port ?? 8000),
    debug: !!stored.debug,
    debugPort: String(stored.debugPort ?? 5678),
  });
  const [enabled, setEnabled] = useState(!!svc);
  const [version, setVersion] = useState(svc?.version ?? "");
  const [server, setServer] = useState<PythonServerForm>(fromStored);
  useEffect(() => {
    setEnabled(!!svc);
    setVersion(svc?.version ?? python?.versions.find((v) => v.default)?.version ?? "");
    setServer(fromStored());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [svc, python]);
  const dirty = enabled !== !!svc || (enabled && (version !== (svc?.version ?? "") || JSON.stringify(server) !== JSON.stringify(fromStored())));
  const pythonStatus = p.status.services.find((s) => s.kind === "python");
  // The server answers on the project URL when Python is the application; next to PHP only its host port is published.
  const url = serves === "python" ? links(p).url : stored.hostPort ? links({ httpPort: stored.hostPort, hostnames: [], serves: "static", services: [] }).direct : "";

  return (
    <Card>
      <CardHeader
        title={t("Python")}
        description={
          serves !== "php"
            ? t("Application runtime of this project: run Django, Flask, FastAPI or any WSGI/ASGI app here. Removing it only removes the container; the .venv stays in the project directory.")
            : t("Tooling container (pip, uv, venv), optionally running an application server on its own port. Removing it only removes the container; the .venv stays in the project directory.")
        }
        actions={
          <Button
            variant="primary"
            icon={<Save className="size-4" />}
            loading={update.isPending}
            disabled={!dirty}
            onClick={() =>
              update.mutate(
                { python: enabled ? { enabled: true, version, ...pythonServerRequest(server) } : { enabled: false } },
                {
                  onSuccess: () => setMsg({ tone: "green", text: enabled ? t("Python container updated.") : t("Python container removed.") }),
                  onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
                },
              )
            }
          >
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {stored.server && (
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-default px-3 py-2 text-sm">
            <span className="text-muted">{t("Application server")}</span>
            {pythonStatus && (
              <span className="inline-flex items-center gap-1.5 text-xs">
                <StatusDot tone={containerStateTone(pythonStatus.state)} /> {pythonStatus.state}
              </span>
            )}
            {url ? (
              <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
                {url} <ExternalLink className="size-3" />
              </a>
            ) : (
              <span className="text-xs text-subtle">{t("no port")}</span>
            )}
            {stored.hostPort ? <span className="font-mono text-xs text-subtle">{t("host port {{port}}", { port: stored.hostPort })}</span> : null}
            {stored.mode === "production" && <Badge tone="blue">{t("production server")}</Badge>}
            {stored.debug && stored.debugHostPort ? <span className="font-mono text-xs text-subtle">{t("debugpy on host port {{port}}", { port: stored.debugHostPort })}</span> : null}
          </div>
        )}
        <Checkbox label={t("Enable Python")} checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        {enabled && python && (
          <Field label={t("Python version")} htmlFor="python-version">
            <Select id="python-version" value={version} onChange={(e) => setVersion(e.target.value)}>
              {python.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                  {v.eol ? t(" (end of life)") : v.preview ? t(" (preview)") : ""}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {enabled && <PythonServerFields value={server} onChange={setServer} presets={runtimes.data?.pythonPresets ?? defaultPythonPresets} primary={serves !== "php"} />}
      </div>
    </Card>
  );
}

function EnvTab({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const update = useUpdateProject(p.id);
  const { msg, setMsg } = useSaveFeedback();
  const [env, setEnv] = useState<EnvVar[]>(p.env);
  const dirty = JSON.stringify(env) !== JSON.stringify(p.env);

  const save = () => {
    setMsg(null);
    update.mutate(
      { env: env.filter((e) => e.key) },
      {
        onSuccess: () => setMsg({ tone: "green", text: t("Environment saved. Containers were recreated with the new variables.") }),
        onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
      },
    );
  };

  return (
    <Card>
      <CardHeader
        title={t("Environment variables")}
        description={t("Injected into every container of this project. Saving recreates the containers.")}
        actions={
          <Button variant="primary" onClick={save} loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <EnvEditor value={env} onChange={setEnv} services={p.services} exportName={p.slug} />
      </div>
    </Card>
  );
}

function AdvancedTab({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const plan = useProjectPlan(p.id);
  if (plan.isPending) return <Spinner />;
  if (plan.isError) return <ErrorState message={errorText(plan.error, t)} />;
  const pl = plan.data;
  return (
    <Card>
      <CardHeader title={t("Docker plan")} description={t("What Envoryx provisions for this project. Generated from the desired state; not editable by design.")} />
      <div className="space-y-5 p-5 text-sm">
        <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-[10rem_1fr]">
          <dt className="text-muted">{t("Host path")}</dt>
          <dd className="font-mono text-xs">{pl.hostPath}</dd>
          <dt className="text-muted">{t("Network")}</dt>
          <dd className="font-mono text-xs">{pl.network}</dd>
          <dt className="text-muted">{t("Images")}</dt>
          <dd className="font-mono text-xs">{pl.images.join(", ")}</dd>
        </dl>
        <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
          {pl.containers.map((c) => (
            <li key={c.name} className="px-3 py-2 text-xs">
              <div className="flex items-center justify-between gap-3">
                <span className="font-mono font-medium text-fg">{c.name}</span>
                <span className="font-mono text-subtle">{c.image}</span>
              </div>
              <ul className="mt-1 space-y-0.5 font-mono text-[11px] text-muted">
                {c.ports.map((port) => (
                  <li key={port}>{t("port")} {port}</li>
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
        <p className="text-xs text-subtle">
          {t("All resources carry the labels")} <Code>envoryx.managed=true</Code> {t("and")} <Code>envoryx.project.id={p.id}</Code>.
        </p>
      </div>
    </Card>
  );
}
