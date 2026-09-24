import { Clock, History, Pencil, Play, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { CronJob, CronJobRequest, CronRun, CronRuntime, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Dialog, ErrorState, Field, Input, Select, Spinner, type Tone } from "@/components/ui";
import { formatDateTime, formatRelative } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { defaultSchedule, describeSchedule, everyOptions, fromCron, toCron, weekdayNames, type ScheduleForm, type ScheduleKind } from "./cronSchedule";

const runtimeLabels: Record<CronRuntime, string> = { php: "PHP", node: "Node.js", python: "Python" };

const statusTone: Record<CronRun["status"], Tone> = { running: "blue", succeeded: "green", failed: "red", timed_out: "red", error: "red", interrupted: "amber" };
const statusLabel: Record<CronRun["status"], string> = { running: "running", succeeded: "succeeded", failed: "failed", timed_out: "timed out", error: "could not run", interrupted: "interrupted" };

/** Common commands to start from; offered for the runtimes the project has. */
const templates: { label: string; runtime: CronRuntime; name: string; command: string; schedule: string }[] = [
  { label: "Laravel scheduler", runtime: "php", name: "scheduler", command: "php artisan schedule:run", schedule: "* * * * *" },
  { label: "Symfony command", runtime: "php", name: "command", command: "php bin/console app:my-command", schedule: "0 3 * * *" },
  { label: "PHP script", runtime: "php", name: "script", command: "php scripts/cleanup.php", schedule: "0 * * * *" },
  { label: "npm script", runtime: "node", name: "script", command: "npm run cleanup", schedule: "0 * * * *" },
  { label: "Django command", runtime: "python", name: "command", command: "python manage.py clearsessions", schedule: "0 3 * * *" },
  { label: "Python script", runtime: "python", name: "script", command: "python scripts/cleanup.py", schedule: "0 * * * *" },
];

const textareaClass = "w-full rounded-md border border-default bg-elevated p-2 font-mono text-xs text-fg focus:border-accent-500 focus:outline-none";

function duration(run: CronRun): string {
  if (!run.finishedAt) return "";
  const s = Math.max(0, Math.round((Date.parse(run.finishedAt) - Date.parse(run.startedAt)) / 1000));
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`;
}

function RunBadge({ run }: { run: CronRun }) {
  const { t } = useTranslation();
  return (
    <Badge tone={statusTone[run.status]}>
      {t(statusLabel[run.status])}
      {run.status === "failed" ? ` (${run.exitCode})` : ""}
    </Badge>
  );
}

function RunHistory({ project, job }: { project: Project; job: CronJob }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["projects", project.id, "cron", job.id, "runs"],
    queryFn: () => api.projects.cron.runs(project.id, job.id),
    // While a run is going, look again until it is done.
    refetchInterval: (query) => (query.state.data?.runs.some((r) => r.status === "running") || job.running ? 2000 : false),
  });
  const [open, setOpen] = useState<string | null>(null);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} />;
  if (q.data.runs.length === 0) return <p className="text-xs text-subtle">{t("No runs yet.")}</p>;
  return (
    <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
      {q.data.runs.map((r) => (
        <li key={r.id} className="px-3 py-2 text-xs">
          <button type="button" className="flex w-full flex-wrap items-center gap-2 text-left" onClick={() => setOpen(open === r.id ? null : r.id)} aria-expanded={open === r.id}>
            <RunBadge run={r} />
            <span className="text-muted">{formatDateTime(r.startedAt)}</span>
            {duration(r) && <span className="text-subtle">{duration(r)}</span>}
            {r.source === "manual" && <Badge>{t("manual")}</Badge>}
          </button>
          {open === r.id && (
            <div className="mt-2 space-y-1">
              {r.truncated && <p className="text-subtle">{t("Only the last 64 KB of the output are kept.")}</p>}
              <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded bg-[var(--code-bg,rgba(0,0,0,0.04))] p-2 font-mono text-[11px] text-fg">{r.output || t("(no output)")}</pre>
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

interface FormState {
  name: string;
  runtime: CronRuntime;
  schedule: ScheduleForm;
  command: string;
  timeoutMinutes: number;
  enabled: boolean;
}

function toForm(job: CronJob): FormState {
  return { name: job.name, runtime: job.runtime, schedule: fromCron(job.schedule), command: job.command, timeoutMinutes: Math.max(1, Math.round(job.timeoutSeconds / 60)), enabled: job.enabled };
}

function SchedulePreview({ expr }: { expr: string }) {
  const { t } = useTranslation();
  const [debounced, setDebounced] = useState(expr);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(expr), 300);
    return () => clearTimeout(id);
  }, [expr]);
  const q = useQuery({ queryKey: ["cron-preview", debounced], queryFn: () => api.projects.cron.preview(debounced), enabled: debounced.trim() !== "", retry: false });
  if (!debounced.trim()) return null;
  if (q.isError) return <p className="text-xs text-red-600 dark:text-red-400">{errorText(q.error, t)}</p>;
  if (!q.data) return null;
  return (
    <p className="text-xs text-subtle">
      {t("Next runs ({{timezone}}):", { timezone: q.data.timezone })} {q.data.next.slice(0, 3).map((n) => formatDateTime(n)).join(" · ")}
    </p>
  );
}

function ScheduleFields({ value, onChange }: { value: ScheduleForm; onChange: (v: ScheduleForm) => void }) {
  const { t } = useTranslation();
  const set = (patch: Partial<ScheduleForm>) => onChange({ ...value, ...patch });
  const pad = (n: number) => String(n).padStart(2, "0");
  const time = (
    <Field label={t("Time")} htmlFor="cron-time">
      <Input
        id="cron-time"
        type="time"
        value={`${pad(value.hour)}:${pad(value.minute)}`}
        onChange={(e) => {
          const [h = NaN, m = NaN] = e.target.value.split(":").map(Number);
          if (!Number.isNaN(h) && !Number.isNaN(m)) set({ hour: h, minute: m });
        }}
      />
    </Field>
  );
  const kinds: [ScheduleKind, string][] = [
    ["minute", "Every minute"],
    ["every", "Every few minutes"],
    ["hourly", "Hourly"],
    ["daily", "Daily"],
    ["weekly", "Weekly"],
    ["monthly", "Monthly"],
    ["custom", "Cron expression"],
  ];
  return (
    <div className="grid gap-4 sm:grid-cols-3">
      <Field label={t("Schedule")} htmlFor="cron-kind">
        <Select id="cron-kind" value={value.kind} onChange={(e) => set({ kind: e.target.value as ScheduleKind, custom: value.kind === "custom" ? value.custom : toCron(value) })}>
          {kinds.map(([k, label]) => (
            <option key={k} value={k}>
              {t(label)}
            </option>
          ))}
        </Select>
      </Field>
      {value.kind === "every" && (
        <Field label={t("Interval")} htmlFor="cron-every">
          <Select id="cron-every" value={value.every} onChange={(e) => set({ every: Number(e.target.value) })}>
            {everyOptions.map((n) => (
              <option key={n} value={n}>
                {t("every {{minutes}} minutes", { minutes: n })}
              </option>
            ))}
          </Select>
        </Field>
      )}
      {value.kind === "hourly" && (
        <Field label={t("Minute")} htmlFor="cron-minute">
          <Input id="cron-minute" type="number" min={0} max={59} value={value.minute} onChange={(e) => set({ minute: Math.min(59, Math.max(0, Number(e.target.value) || 0)) })} />
        </Field>
      )}
      {value.kind === "daily" && time}
      {value.kind === "weekly" && (
        <>
          <Field label={t("Weekday")} htmlFor="cron-weekday">
            <Select id="cron-weekday" value={value.weekday} onChange={(e) => set({ weekday: Number(e.target.value) })}>
              {weekdayNames.map((d, i) => (
                <option key={d} value={i}>
                  {t(d)}
                </option>
              ))}
            </Select>
          </Field>
          {time}
        </>
      )}
      {value.kind === "monthly" && (
        <>
          <Field label={t("Day of the month")} htmlFor="cron-day" hint={t("1-28, so every month has it")}>
            <Input id="cron-day" type="number" min={1} max={28} value={value.day} onChange={(e) => set({ day: Math.min(28, Math.max(1, Number(e.target.value) || 1)) })} />
          </Field>
          {time}
        </>
      )}
      {value.kind === "custom" && (
        <Field label={t("Cron expression")} htmlFor="cron-expr" hint={t("minute hour day-of-month month day-of-week, e.g. 30 2 * * 1-5")}>
          <Input id="cron-expr" value={value.custom} onChange={(e) => set({ custom: e.target.value })} placeholder="*/10 * * * *" spellCheck={false} className="font-mono" />
        </Field>
      )}
    </div>
  );
}

function JobForm({ project, job, onDone }: { project: Project; job?: CronJob; onDone: (message: string) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const has = (kind: string) => project.services.some((s) => s.kind === kind && s.enabled);
  const runtimes = (["php", "python", "node"] as CronRuntime[]).filter(has);
  const [form, setForm] = useState<FormState>(() => (job ? toForm(job) : { name: "", runtime: runtimes[0] ?? "php", schedule: defaultSchedule, command: "", timeoutMinutes: 10, enabled: true }));
  const [error, setError] = useState<string | null>(null);
  const set = (patch: Partial<FormState>) => setForm((f) => ({ ...f, ...patch }));
  const body = (): CronJobRequest => ({ name: form.name.trim(), runtime: form.runtime, schedule: toCron(form.schedule), command: form.command, timeoutSeconds: form.timeoutMinutes * 60, enabled: form.enabled });
  const save = useMutation({
    mutationFn: () => (job ? api.projects.cron.update(project.id, job.id, body()) : api.projects.cron.add(project.id, body())),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["projects", project.id, "cron"] });
      onDone(job ? t('Cron job "{{name}}" saved.', { name: r.job.name }) : t('Cron job "{{name}}" added.', { name: r.job.name }));
    },
    onError: (err) => setError(errorText(err, t, t("Saving the cron job failed"))),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate();
  };
  const offered = templates.filter((tpl) => runtimes.includes(tpl.runtime));
  return (
    <form onSubmit={submit} className="space-y-4">
      {error && <Alert tone="red">{error}</Alert>}
      {!job && offered.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-muted">{t("Start from:")}</span>
          {offered.map((tpl) => (
            <Button key={tpl.label} type="button" size="sm" variant="ghost" onClick={() => set({ name: form.name || tpl.name, runtime: tpl.runtime, command: tpl.command, schedule: fromCron(tpl.schedule) })}>
              {t(tpl.label)}
            </Button>
          ))}
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label={t("Name")} htmlFor="cron-name" hint={t("Lower-case, e.g. nightly-report")}>
          <Input id="cron-name" value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="nightly-report" spellCheck={false} autoCapitalize="none" />
        </Field>
        <Field label={t("Runs in")} htmlFor="cron-runtime">
          <Select id="cron-runtime" value={form.runtime} onChange={(e) => set({ runtime: e.target.value as CronRuntime })}>
            {(runtimes.includes(form.runtime) ? runtimes : [form.runtime, ...runtimes]).map((r) => (
              <option key={r} value={r}>
                {t("{{runtime}} container", { runtime: runtimeLabels[r] })}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("Timeout")} htmlFor="cron-timeout" hint={t("Minutes; the command is stopped after that.")}>
          <Input id="cron-timeout" type="number" min={1} max={1440} value={form.timeoutMinutes} onChange={(e) => set({ timeoutMinutes: Math.min(1440, Math.max(1, Number(e.target.value) || 1)) })} />
        </Field>
      </div>
      <Field label={t("Command")} htmlFor="cron-command" hint={t("Runs through sh -c as the project user in the project directory, with the project's environment variables. Pipes, && and redirections work.")}>
        <textarea id="cron-command" value={form.command} onChange={(e) => set({ command: e.target.value })} rows={3} spellCheck={false} className={textareaClass} placeholder="php artisan report:send --daily" />
      </Field>
      <ScheduleFields value={form.schedule} onChange={(schedule) => set({ schedule })} />
      <SchedulePreview expr={toCron(form.schedule)} />
      <div className="flex gap-2">
        <Button type="submit" variant="primary" loading={save.isPending} disabled={!form.name.trim() || !form.command.trim() || !toCron(form.schedule)} icon={job ? undefined : <Plus className="size-4" />}>
          {job ? t("Save") : t("Add cron job")}
        </Button>
        {job && (
          <Button type="button" onClick={() => onDone("")}>
            {t("Cancel")}
          </Button>
        )}
      </div>
    </form>
  );
}

export function CronTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["projects", project.id, "cron"],
    queryFn: () => api.projects.cron.list(project.id),
    refetchInterval: (query) => (query.state.data?.jobs.some((j) => j.running) ? 2000 : 30000),
  });
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [history, setHistory] = useState<string | null>(null);
  const [removing, setRemoving] = useState<CronJob | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: ["projects", project.id, "cron"] });
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: errorText(err, t, fallback) });
  const run = useMutation({
    mutationFn: (j: CronJob) => api.projects.cron.run(project.id, j.id),
    onSuccess: (_, j) => {
      setHistory(j.id);
      setMsg({ tone: "green", text: t('"{{name}}" started.', { name: j.name }) });
      refresh();
    },
    onError: (err) => fail(err, t("Starting the cron job failed")),
  });
  const toggle = useMutation({
    mutationFn: (j: CronJob) => api.projects.cron.update(project.id, j.id, { name: j.name, runtime: j.runtime, schedule: j.schedule, command: j.command, timeoutSeconds: j.timeoutSeconds, enabled: !j.enabled }),
    onSuccess: refresh,
    onError: (err) => fail(err, t("Updating the cron job failed")),
  });
  const remove = useMutation({
    mutationFn: (j: CronJob) => api.projects.cron.remove(project.id, j.id),
    onSuccess: () => {
      setRemoving(null);
      refresh();
    },
    onError: (err) => {
      setRemoving(null);
      fail(err, t("Removing the cron job failed"));
    },
  });
  const done = (text: string) => {
    setEditing(null);
    if (text) setMsg({ tone: "green", text });
  };
  const hasRuntime = project.services.some((s) => (s.kind === "php" || s.kind === "node" || s.kind === "python") && s.enabled);

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} />;
  const running = project.status.state === "running";

  return (
    <div className="space-y-6">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      {!hasRuntime && <Alert tone="amber">{t("Cron jobs run in the project's PHP, Python or Node.js container – this project has none. Add a runtime in the Runtime tab first.")}</Alert>}
      {hasRuntime && !running && q.data.jobs.length > 0 && <Alert tone="blue">{t("The project is not running, so its cron jobs are paused. They resume when it starts; missed runs are not caught up.")}</Alert>}
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <Clock className="size-4 text-accent-500" aria-hidden /> {t("Cron jobs")}
            </span>
          }
          description={t("Commands Envoryx runs on a schedule inside the project's PHP, Python or Node.js container while the project is running. A run that is still going when the next one is due is not started twice. Schedules are read in {{timezone}}.", { timezone: q.data.timezone })}
        />
        {q.data.jobs.length === 0 ? (
          <p className="px-5 py-4 text-sm text-muted">{t("No cron jobs yet.")}</p>
        ) : (
          <ul className="divide-y divide-[var(--border)]">
            {q.data.jobs.map((j) => (
              <li key={j.id} className="space-y-3 px-5 py-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0 space-y-0.5">
                    <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
                      {j.name}
                      <Badge>{runtimeLabels[j.runtime]}</Badge>
                      {!j.enabled && <Badge tone="gray">{t("disabled")}</Badge>}
                      {j.running && <Badge tone="blue">{t("running")}</Badge>}
                      {j.runtimeMissing && <Badge tone="amber">{t("paused – runtime missing")}</Badge>}
                    </p>
                    <p className="text-xs text-muted">
                      <span className="font-mono">{j.schedule}</span>
                      {describeSchedule(j.schedule, t) !== j.schedule && <> · {describeSchedule(j.schedule, t)}</>}
                      {j.nextRun && <> · {t("next: {{time}}", { time: formatDateTime(j.nextRun) })}</>}
                    </p>
                    <p className="whitespace-pre-wrap break-all font-mono text-[11px] text-subtle">{j.command}</p>
                    {j.lastRun && (
                      <p className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
                        {t("Last run:")} <RunBadge run={j.lastRun} /> <span title={formatDateTime(j.lastRun.startedAt)}>{formatRelative(j.lastRun.startedAt, t)}</span>
                      </p>
                    )}
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Button size="sm" onClick={() => run.mutate(j)} loading={run.isPending && run.variables?.id === j.id} disabled={j.running || j.runtimeMissing} icon={<Play className="size-3.5" />}>
                      {t("Run now")}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setHistory(history === j.id ? null : j.id)} icon={<History className="size-3.5" />} aria-expanded={history === j.id}>
                      {t("History")}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditing(editing === j.id ? null : j.id)} icon={<Pencil className="size-3.5" />}>
                      {t("Edit")}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => toggle.mutate(j)} loading={toggle.isPending && toggle.variables?.id === j.id}>
                      {j.enabled ? t("Disable") : t("Enable")}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setRemoving(j)} icon={<Trash2 className="size-3.5" />} aria-label={t("Remove {{name}}", { name: j.name })}>
                      {t("Remove")}
                    </Button>
                  </div>
                </div>
                {editing === j.id && (
                  <div className="rounded-md border border-default p-4">
                    <JobForm project={project} job={j} onDone={done} />
                  </div>
                )}
                {history === j.id && <RunHistory project={project} job={j} />}
              </li>
            ))}
          </ul>
        )}
        {hasRuntime && (
          <div className="space-y-4 border-t border-default p-5">
            <p className="text-sm font-medium text-fg">{t("Add cron job")}</p>
            <JobForm key={q.data.jobs.length} project={project} onDone={done} />
          </div>
        )}
      </Card>
      <Dialog
        open={!!removing}
        onClose={() => setRemoving(null)}
        title={t("Remove {{name}}?", { name: removing?.name ?? "" })}
        description={t("The cron job and its run history are deleted. A run in progress finishes.")}
        footer={
          <>
            <Button onClick={() => setRemoving(null)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => removing && remove.mutate(removing)}>
              {t("Remove")}
            </Button>
          </>
        }
      />
    </div>
  );
}
