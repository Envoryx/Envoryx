import { Gauge, Save } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys, usePlan, useProjectStats } from "@/api/hooks";
import type { ContainerUsage, LimitSet, Project, ResourceLimits } from "@/api/types";
import { Alert, Button, Card, CardHeader, Field, Input, Select, Spinner } from "@/components/ui";
import { formatBytes } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { projectAccess } from "@/lib/access";

const DEFAULT_PIDS = 4096;

type Unit = "MiB" | "GiB";

/** One group's inputs as text, so an empty field means "no limit". */
interface GroupForm {
  cpus: string;
  memory: string;
  unit: Unit;
}

function toForm(l: LimitSet | undefined): GroupForm {
  const mb = l?.memoryMb ?? 0;
  const gib = mb > 0 && mb % 1024 === 0;
  return { cpus: l?.cpus ? String(l.cpus) : "", memory: mb ? String(gib ? mb / 1024 : mb) : "", unit: gib || mb === 0 ? "GiB" : "MiB" };
}

function fromForm(f: GroupForm): LimitSet {
  const cpus = parseFloat(f.cpus.replace(",", "."));
  const mem = parseFloat(f.memory.replace(",", "."));
  const out: LimitSet = {};
  if (cpus > 0) out.cpus = Math.round(cpus * 100) / 100;
  if (mem > 0) out.memoryMb = Math.round(f.unit === "GiB" ? mem * 1024 : mem);
  return out;
}

/** A usage bar; without a limit it shows the value alone. */
function Bar({ value, max, label }: { value: number; max: number; label: string }) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  const tone = pct >= 90 ? "bg-red-500" : pct >= 70 ? "bg-amber-500" : "bg-accent-500";
  return (
    <div className="min-w-0 space-y-1">
      <div className="text-xs tabular-nums text-muted">{label}</div>
      {max > 0 && (
        <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted" role="meter" aria-valuemin={0} aria-valuemax={max} aria-valuenow={value} aria-label={label}>
          <div className={`h-full rounded-full ${tone}`} style={{ width: `${pct}%` }} />
        </div>
      )}
    </div>
  );
}

/** What an empty field means and the most it may hold, from the hoster's plan. */
interface PlanBounds {
  cpus: { empty?: number | undefined; max?: number | undefined };
  memoryMb: { empty?: number | undefined; max?: number | undefined };
}

function GroupFields({ id, title, hint, form, onChange, plan }: { id: string; title: string; hint: string; form: GroupForm; onChange: (f: GroupForm) => void; plan: PlanBounds }) {
  const { t } = useTranslation();
  const cores = (n: number) => t("{{count}} cores", { count: n });
  const mem = (mb: number) => formatBytes(mb * 2 ** 20);
  const bounded = (empty: string | undefined, max: string | undefined, none: string) =>
    [empty ? t("empty: {{value}} from your hoster's plan", { value: empty }) : none, max && t("at most {{value}}", { value: max })].filter(Boolean).join(" - ");
  const cpuHint = bounded(plan.cpus.empty ? cores(plan.cpus.empty) : undefined, plan.cpus.max ? cores(plan.cpus.max) : undefined, t("e.g. 1.5 - empty: no limit"));
  const memHint = bounded(plan.memoryMb.empty ? mem(plan.memoryMb.empty) : undefined, plan.memoryMb.max ? mem(plan.memoryMb.max) : undefined, t("empty: no limit"));
  return (
    <fieldset className="space-y-3 rounded-md border border-default p-3">
      <legend className="px-1 text-sm font-medium text-fg">{title}</legend>
      <p className="text-xs text-muted">{hint}</p>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("CPU cores per container")} htmlFor={`${id}-cpus`} hint={cpuHint}>
          <Input id={`${id}-cpus`} inputMode="decimal" value={form.cpus} onChange={(e) => onChange({ ...form, cpus: e.target.value })} placeholder={plan.cpus.empty ? String(plan.cpus.empty) : t("no limit")} />
        </Field>
        <Field label={t("Memory per container")} htmlFor={`${id}-memory`} hint={memHint}>
          <div className="flex gap-2">
            <Input id={`${id}-memory`} inputMode="decimal" value={form.memory} onChange={(e) => onChange({ ...form, memory: e.target.value })} placeholder={plan.memoryMb.empty ? mem(plan.memoryMb.empty) : t("no limit")} />
            <div className="w-24 shrink-0">
              <Select aria-label={t("Unit")} value={form.unit} onChange={(e) => onChange({ ...form, unit: e.target.value as Unit })}>
                <option value="MiB">MiB</option>
                <option value="GiB">GiB</option>
              </Select>
            </div>
          </div>
        </Field>
      </div>
    </fieldset>
  );
}

/**
 * CPU, memory and process limits of a project's containers, with the live usage of each
 * container against its limit. A container that hits its memory limit loses a process
 * (the kernel's OOM killer); the project then shows a warning for a day.
 */
export function LimitsCard({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const stats = useProjectStats(project.id, true);
  const current = project.limits;
  const editable = projectAccess(project).admin;
  // The hoster's plan fills in an empty field and caps every value.
  const planLimits = usePlan().data?.plan?.limits;
  const cap = planLimits?.container;
  const def = planLimits?.containerDefault;
  const bounds: PlanBounds = {
    cpus: { empty: def?.cpus || cap?.cpus, max: cap?.cpus },
    memoryMb: { empty: def?.memoryMb || cap?.memoryMb, max: cap?.memoryMb },
  };
  const [app, setApp] = useState<GroupForm>(toForm(current?.app));
  const [services, setServices] = useState<GroupForm>(toForm(current?.services));
  const [pids, setPids] = useState(current?.pids ? String(current.pids) : "");
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const currentKey = JSON.stringify(current ?? {});
  useEffect(() => {
    setApp(toForm(current?.app));
    setServices(toForm(current?.services));
    setPids(current?.pids ? String(current.pids) : "");
    // eslint-disable-next-line react-hooks/exhaustive-deps -- compared by content
  }, [currentKey]);

  const body: ResourceLimits = { app: fromForm(app), services: fromForm(services) };
  const p = parseInt(pids, 10);
  if (p > 0) body.pids = p;
  const dirty = JSON.stringify(body) !== JSON.stringify({ app: current?.app ?? {}, services: current?.services ?? {}, ...(current?.pids ? { pids: current.pids } : {}) });

  const save = useMutation({
    mutationFn: () => api.projects.setLimits(project.id, body),
    onSuccess: () => {
      setMsg({ tone: "green", text: project.desiredState === "running" ? t("Limits saved and applied to the running containers.") : t("Limits saved; they apply when the project starts.") });
      void qc.invalidateQueries({ queryKey: keys.project(project.id) });
      void qc.invalidateQueries({ queryKey: keys.projectStats(project.id) });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });

  const host = stats.data?.host;
  const containers: ContainerUsage[] = [...(stats.data?.containers ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  const slugPrefix = `envoryx-${project.slug}-`;

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Gauge className="size-4 text-accent-500" aria-hidden /> {t("Resource limits")}
          </span>
        }
        description={t("Caps what each container of this project may use, so a runaway worker or Node process cannot take the whole server. Docker limits containers one by one: the numbers apply to each container of the group, not to the project in total.")}
        actions={
          editable && (
            <Button size="sm" variant="primary" loading={save.isPending} disabled={!dirty} onClick={() => { setMsg(null); save.mutate(); }} icon={<Save className="size-3.5" />}>
              {t("Save")}
            </Button>
          )
        }
      />
      <fieldset disabled={!editable} className="min-w-0 space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {host && <p className="text-xs text-muted">{t("This host has {{cpus}} cores and {{memory}} of memory.", { cpus: host.cpus, memory: formatBytes(host.memory) })}</p>}
        <div className="grid gap-4 lg:grid-cols-2">
          <GroupFields id="limits-app" title={t("Application containers")} hint={t("Web server, PHP, Node.js, Python, Go, Ruby, Java, .NET and every worker.")} form={app} onChange={setApp} plan={bounds} />
          <GroupFields id="limits-services" title={t("Services")} hint={t("Database, Redis, Memcached, Mailpit, RabbitMQ, search engines, object storage. OpenSearch needs at least 1.5 GiB.")} form={services} onChange={setServices} plan={bounds} />
        </div>
        <div className="max-w-xs">
          <Field label={t("Processes per container")} htmlFor="limits-pids" hint={t("Stops fork bombs and worker pools that keep spawning. Empty: {{n}}.", { n: DEFAULT_PIDS })}>
            <Input id="limits-pids" inputMode="numeric" value={pids} onChange={(e) => setPids(e.target.value.replace(/[^0-9]/g, ""))} placeholder={String(DEFAULT_PIDS)} />
          </Field>
        </div>
        <p className="text-xs text-subtle">{t("Changes apply to running containers right away; only removing a CPU or memory limit restarts the affected containers. When a container reaches its memory limit, the kernel ends a process in it - the project then shows a warning and Envoryx sends a notification.")}</p>

        <div className="space-y-2 border-t border-default pt-4">
          <p className="text-sm font-medium text-fg">{t("Usage now")}</p>
          {stats.isPending && project.desiredState === "running" ? (
            <Spinner />
          ) : containers.length === 0 ? (
            <p className="text-sm text-muted">{t("No container is running.")}</p>
          ) : (
            <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
              {containers.map((c) => {
                const cpuMax = c.cpuLimit * 100;
                return (
                  <li key={c.containerId} className="grid items-center gap-x-6 gap-y-2 px-3 py-2 sm:grid-cols-[12rem_1fr_1fr]">
                    <span className="truncate font-mono text-xs text-fg" title={c.name}>
                      {c.name.startsWith(slugPrefix) ? c.name.slice(slugPrefix.length) : c.name}
                    </span>
                    <Bar
                      value={c.cpuPercent}
                      max={cpuMax}
                      label={cpuMax > 0 ? t("CPU {{used}} of {{limit}} cores", { used: (c.cpuPercent / 100).toFixed(2), limit: c.cpuLimit }) : t("CPU {{used}} cores (no limit)", { used: (c.cpuPercent / 100).toFixed(2) })}
                    />
                    <Bar
                      value={c.memoryBytes}
                      max={c.memLimit}
                      label={c.memLimit > 0 ? t("Memory {{used}} of {{limit}}", { used: formatBytes(c.memoryBytes), limit: formatBytes(c.memLimit) }) : t("Memory {{used}} (no limit)", { used: formatBytes(c.memoryBytes) })}
                    />
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </fieldset>
    </Card>
  );
}
