import { clsx } from "clsx";
import { Trash2, Save, ExternalLink } from "lucide-react";
import { lazy, Suspense, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ApiError } from "@/api/client";
import { useProject, useProjectPlan, useProjectStats, usePublicHost, useRuntimes, useUpdateProject } from "@/api/hooks";
import type { EnvVar, PHPConfig, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Code, ErrorState, Field, Input, PageHeader, Select, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone, formatBytes, formatDateTime, formatPercent, projectUrl, serviceLabel, stateMeta } from "@/lib/format";
import { DeleteProjectDialog, ProjectActionButtons, useActionError } from "./ProjectActions";
import { DatabaseTab } from "./DatabaseTab";
import { EnvEditor } from "./EnvEditor";
import { LogsTab } from "./LogsTab";
// xterm.js is only needed on this tab; keep it out of the main bundle.
const TerminalTab = lazy(() => import("./TerminalTab").then((m) => ({ default: m.TerminalTab })));
import { PhpConfigForm } from "./PhpConfigForm";

const tabs = ["Overview", "Terminal", "Logs", "PHP", "Database", "Environment", "Advanced"] as const;
type Tab = (typeof tabs)[number];

export function ProjectDetailPage() {
  const { id = "" } = useParams();
  const q = useProject(id);
  const publicHost = usePublicHost();
  const [tab, setTab] = useState<Tab>("Overview");
  const [deleting, setDeleting] = useState(false);
  const { error, capture, setError } = useActionError();

  if (q.isPending) return <Spinner />;
  if (q.isError) {
    const notFound = q.error instanceof ApiError && q.error.status === 404;
    return <ErrorState title={notFound ? "Project not found" : "Could not load project"} message={notFound ? undefined : q.error.message} action={<Link to="/projects" className="text-sm underline">Back to projects</Link>} />;
  }
  const p = q.data;
  const meta = stateMeta[p.status.state];
  const url = projectUrl(p.httpPort, publicHost);

  return (
    <div>
      <PageHeader
        title={
          <span className="flex items-center gap-3">
            <StatusDot tone={meta.tone} pulse={meta.pulse ?? false} />
            {p.name}
            <Badge tone={meta.tone}>{meta.label}</Badge>
          </span>
        }
        description={
          <span className="font-mono text-xs">
            /projects/{p.path}
            {p.docroot ? `/${p.docroot}` : ""} ·{" "}
            {url ? (
              <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 hover:underline">
                {url} <ExternalLink className="size-3" />
              </a>
            ) : (
              "no port"
            )}
          </span>
        }
        actions={
          <>
            <ProjectActionButtons project={p} size="md" onError={capture} />
            <Button variant="ghost" onClick={() => setDeleting(true)} icon={<Trash2 className="size-4" />} aria-label="Delete project" title="Delete project" />
          </>
        }
      />

      {error && (
        <div className="mb-4">
          <Alert tone="red">
            {error}
            <button className="ml-2 underline" onClick={() => setError(null)}>
              dismiss
            </button>
          </Alert>
        </div>
      )}
      {p.status.warnings.length > 0 && (
        <div className="mb-4">
          <Alert tone={p.status.state === "error" ? "red" : "amber"}>
            <ul className="list-disc pl-4">
              {p.status.warnings.map((w, i) => (
                <li key={i}>{w}</li>
              ))}
            </ul>
          </Alert>
        </div>
      )}

      <div className="mb-4 flex gap-1 border-b border-default" role="tablist">
        {tabs.map((t) => (
          <button
            key={t}
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={clsx("-mb-px border-b-2 px-3 py-2 text-sm font-medium", tab === t ? "border-accent-500 text-fg" : "border-transparent text-muted hover:text-fg")}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === "Overview" && <OverviewTab project={p} />}
      {tab === "Terminal" && (
        <Suspense fallback={<Spinner label="Loading terminal…" />}>
          <TerminalTab project={p} />
        </Suspense>
      )}
      {tab === "Logs" && <LogsTab project={p} />}
      {tab === "PHP" && <PhpTab project={p} />}
      {tab === "Database" && <DatabaseTab project={p} />}
      {tab === "Environment" && <EnvTab project={p} />}
      {tab === "Advanced" && <AdvancedTab project={p} />}

      <DeleteProjectDialog project={p} open={deleting} onClose={() => setDeleting(false)} />
    </div>
  );
}

function OverviewTab({ project: p }: { project: Project }) {
  const stats = useProjectStats(p.id, p.status.state === "running" || p.status.state === "partial");
  return (
    <div className="grid gap-6 lg:grid-cols-3">
      <Card className="lg:col-span-2">
        <CardHeader title="Services" description="One container per service, connected through the private project network." />
        <ul className="divide-y divide-[var(--border)]">
          {p.status.services.map((s) => (
            <li key={s.kind} className="flex flex-wrap items-center gap-x-6 gap-y-2 px-5 py-3">
              <div className="flex min-w-[10rem] items-center gap-2.5">
                <StatusDot tone={containerStateTone(s.state)} />
                <div>
                  <p className="text-sm font-medium text-fg">{serviceLabel(s.kind, s.version, s.variant)}</p>
                  <p className="font-mono text-[11px] text-subtle">{s.containerName}</p>
                </div>
              </div>
              <Badge tone={containerStateTone(s.state)}>{s.exists ? s.state : "missing"}</Badge>
              {s.health && <Badge tone={s.health === "healthy" ? "green" : s.health === "starting" ? "blue" : "red"}>{s.health}</Badge>}
              <span className="font-mono text-xs text-muted">{s.image}</span>
              {s.ports.map((port) => (
                <span key={port.hostPort} className="font-mono text-xs text-muted">
                  :{port.hostPort} → {port.containerPort}
                </span>
              ))}
              {s.containerId && <span className="ml-auto font-mono text-[11px] text-subtle">{s.containerId.slice(0, 12)}</span>}
            </li>
          ))}
        </ul>
      </Card>
      <div className="space-y-6">
        <Card>
          <CardHeader title="Resources" />
          <dl className="grid grid-cols-2 gap-4 px-5 py-4 text-sm">
            <div>
              <dt className="text-xs text-subtle">CPU</dt>
              <dd className="text-lg font-semibold tabular-nums">{stats.data ? formatPercent(stats.data.stats.cpuPercent) : "—"}</dd>
            </div>
            <div>
              <dt className="text-xs text-subtle">Memory</dt>
              <dd className="text-lg font-semibold tabular-nums">{stats.data ? formatBytes(stats.data.stats.memoryBytes) : "—"}</dd>
            </div>
          </dl>
        </Card>
        <Card>
          <CardHeader title="Details" />
          <dl className="space-y-2 px-5 py-4 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Identifier</dt>
              <dd className="font-mono text-xs">{p.slug}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Network</dt>
              <dd className="font-mono text-xs">staqio-{p.slug}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Desired state</dt>
              <dd>{p.desiredState}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Created</dt>
              <dd>{formatDateTime(p.createdAt)}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Updated</dt>
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

function PhpTab({ project: p }: { project: Project }) {
  const runtimes = useRuntimes();
  const update = useUpdateProject(p.id);
  const { msg, setMsg } = useSaveFeedback();
  const svc = p.services.find((s) => s.kind === "php");
  const [version, setVersion] = useState(svc?.version ?? "");
  const [config, setConfig] = useState<PHPConfig | null>((svc?.config as unknown as PHPConfig) ?? null);
  const [name, setName] = useState(p.name);
  const [docroot, setDocroot] = useState(p.docroot);

  if (!svc || !config) {
    return <Alert tone="gray">This project has no PHP service.</Alert>;
  }
  if (runtimes.isPending) return <Spinner />;
  const php = runtimes.data?.runtimes.find((r) => r.key === "php");
  const dirty = version !== svc.version || JSON.stringify(config) !== JSON.stringify(svc.config) || name !== p.name || docroot !== p.docroot;

  const save = () => {
    setMsg(null);
    update.mutate(
      { name, docroot, php: { version, config } },
      {
        onSuccess: () => setMsg({ tone: "green", text: p.status.state === "running" ? "Saved and applied. Containers were restarted." : "Saved. Changes apply on next start." }),
        onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Saving failed" }),
      },
    );
  };

  return (
    <Card>
      <CardHeader
        title="Project & PHP settings"
        description="Changing the PHP version recreates the PHP container; configuration changes restart it."
        actions={
          <Button variant="primary" onClick={save} loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
            Save
          </Button>
        }
      />
      <div className="space-y-6 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Project name" htmlFor="p-name">
            <Input id="p-name" value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label="Document root" htmlFor="p-docroot" hint="Relative to the project directory">
            <Input id="p-docroot" value={docroot} onChange={(e) => setDocroot(e.target.value)} placeholder="(project root)" />
          </Field>
          <Field label="PHP version" htmlFor="p-version">
            <Select id="p-version" value={version} onChange={(e) => setVersion(e.target.value)}>
              {php?.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                  {v.eol ? " (end of life)" : v.preview ? " (preview)" : ""}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <PhpConfigForm value={config} onChange={setConfig} extensions={runtimes.data?.phpExtensions ?? []} />
      </div>
    </Card>
  );
}

function EnvTab({ project: p }: { project: Project }) {
  const update = useUpdateProject(p.id);
  const { msg, setMsg } = useSaveFeedback();
  const [env, setEnv] = useState<EnvVar[]>(p.env);
  const dirty = JSON.stringify(env) !== JSON.stringify(p.env);

  const save = () => {
    setMsg(null);
    update.mutate(
      { env: env.filter((e) => e.key) },
      {
        onSuccess: () => setMsg({ tone: "green", text: "Environment saved. Containers were recreated with the new variables." }),
        onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Saving failed" }),
      },
    );
  };

  return (
    <Card>
      <CardHeader
        title="Environment variables"
        description="Injected into every container of this project. Saving recreates the containers."
        actions={
          <Button variant="primary" onClick={save} loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
            Save
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <EnvEditor value={env} onChange={setEnv} />
      </div>
    </Card>
  );
}

function AdvancedTab({ project: p }: { project: Project }) {
  const plan = useProjectPlan(p.id);
  if (plan.isPending) return <Spinner />;
  if (plan.isError) return <ErrorState message={plan.error.message} />;
  const pl = plan.data;
  return (
    <Card>
      <CardHeader title="Docker plan" description="What Staqio provisions for this project. Generated from the desired state; not editable by design." />
      <div className="space-y-5 p-5 text-sm">
        <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-[10rem_1fr]">
          <dt className="text-muted">Host path</dt>
          <dd className="font-mono text-xs">{pl.hostPath}</dd>
          <dt className="text-muted">Network</dt>
          <dd className="font-mono text-xs">{pl.network}</dd>
          <dt className="text-muted">Images</dt>
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
                  <li key={port}>port {port}</li>
                ))}
                {c.mounts.map((m) => (
                  <li key={m} className="truncate">
                    mount {m}
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ul>
        <p className="text-xs text-subtle">
          All resources carry the labels <Code>staqio.managed=true</Code> and <Code>staqio.project.id={p.id}</Code>.
        </p>
      </div>
    </Card>
  );
}
