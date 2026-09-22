import { Cog, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys } from "@/api/hooks";
import type { Project, Worker, WorkerPreset } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, ErrorState, Field, Input, Select, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone } from "@/lib/format";
import { errorText } from "@/lib/errors";

export function WorkersTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["projects", project.id, "workers"], queryFn: () => api.projects.workers.list(project.id) });
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["projects", project.id, "workers"] });
    void qc.invalidateQueries({ queryKey: keys.project(project.id) });
  };
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: errorText(err, t, fallback) });
  const [name, setName] = useState("");
  const [preset, setPreset] = useState("laravel:queue");
  const [arg, setArg] = useState("");
  const add = useMutation({
    mutationFn: () => api.projects.workers.add(project.id, { name: name.trim(), preset, arg: arg.trim(), enabled: true }),
    onSuccess: (r) => {
      setName("");
      setArg("");
      setMsg({ tone: "green", text: t('Worker "{{name}}" added and started.', { name: r.worker.name }) });
      refresh();
    },
    onError: (err) => fail(err, t("Adding the worker failed")),
  });
  const toggle = useMutation({
    mutationFn: (w: Worker) => api.projects.workers.update(project.id, w.id, { name: w.name, preset: w.preset, arg: w.arg, enabled: !w.enabled }),
    onSuccess: refresh,
    onError: (err) => fail(err, t("Updating the worker failed")),
  });
  const remove = useMutation({
    mutationFn: (w: Worker) => api.projects.workers.remove(project.id, w.id),
    onSuccess: refresh,
    onError: (err) => fail(err, t("Removing the worker failed")),
  });
  const hasPhp = project.services.some((s) => s.kind === "php" && s.enabled);

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} />;
  const presets = q.data.presets;
  const selected = presets.find((p) => p.id === preset);
  const statusOf = (w: Worker) => project.status.services.find((s) => s.kind === "worker" && s.workerId === w.id);

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    add.mutate();
  }

  return (
    <div className="space-y-6">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      {!hasPhp && <Alert tone="amber">{t("Workers currently run from the PHP image – this project has no PHP service.")}</Alert>}
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <Cog className="size-4 text-accent-500" aria-hidden /> {t("Workers")}
            </span>
          }
          description={t("Long-running processes next to the web server: queue workers, schedulers, WebSocket servers. Each runs in its own container from the project's PHP image (Node workers are not supported yet), restarts automatically and follows start/stop of the project. Logs are in the Logs tab.")}
        />
        {q.data.workers.length === 0 ? (
          <p className="px-5 py-4 text-sm text-muted">{t("No workers yet.")}</p>
        ) : (
          <ul className="divide-y divide-[var(--border)]">
            {q.data.workers.map((w) => {
              const st = statusOf(w);
              const p = presets.find((x) => x.id === w.preset);
              return (
                <li key={w.id} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3">
                  <div className="min-w-0">
                    <p className="flex items-center gap-2 text-sm font-medium">
                      {w.name}
                      <Badge>{p ? t(p.label) : w.preset}</Badge>
                      {w.enabled ? (
                        st ? (
                          <span className="inline-flex items-center gap-1.5 text-xs font-normal text-muted">
                            <StatusDot tone={containerStateTone(st.state)} /> {st.state}
                          </span>
                        ) : null
                      ) : (
                        <Badge tone="gray">{t("disabled")}</Badge>
                      )}
                    </p>
                    <p className="mt-0.5 font-mono text-[11px] text-subtle">{w.command.join(" ")}</p>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <Button size="sm" onClick={() => toggle.mutate(w)} loading={toggle.isPending && toggle.variables?.id === w.id}>
                      {w.enabled ? t("Disable") : t("Enable")}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => remove.mutate(w)} loading={remove.isPending && remove.variables?.id === w.id} icon={<Trash2 className="size-3.5" />} aria-label={t("Remove {{name}}", { name: w.name })}>
                      {t("Remove")}
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
        <form onSubmit={submit} className="space-y-4 border-t border-default p-5">
          <p className="text-sm font-medium text-fg">{t("Add worker")}</p>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={t("Name")} htmlFor="worker-name" hint={t("Lower-case, e.g. queue")}>
              <Input id="worker-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="queue" spellCheck={false} autoCapitalize="none" />
            </Field>
            <Field label={t("Preset")} htmlFor="worker-preset" hint={selected ? t(selected.description) : undefined}>
              <Select id="worker-preset" value={preset} onChange={(e) => { setPreset(e.target.value); setArg(""); }}>
                {groupPresets(presets).map(([group, items]) => (
                  <optgroup key={group} label={group}>
                    {items.map((p) => (
                      <option key={p.id} value={p.id}>{t(p.label)}</option>
                    ))}
                  </optgroup>
                ))}
              </Select>
            </Field>
            {selected?.argLabel && (
              <Field label={t(selected.argLabel)} htmlFor="worker-arg" hint={selected.argHint ? t(selected.argHint) : undefined}>
                <Input id="worker-arg" value={arg} onChange={(e) => setArg(e.target.value)} spellCheck={false} />
              </Field>
            )}
          </div>
          {selected?.requires && selected.requires.length > 0 && (
            <p className="text-xs text-subtle">{t("Expects {{files}} in the project directory.", { files: selected.requires.join(", ") })}</p>
          )}
          <Button type="submit" variant="primary" loading={add.isPending} disabled={!name.trim() || !hasPhp} icon={<Plus className="size-4" />}>
            {t("Add worker")}
          </Button>
        </form>
      </Card>
    </div>
  );
}

function groupPresets(presets: WorkerPreset[]): [string, WorkerPreset[]][] {
  const groups = new Map<string, WorkerPreset[]>();
  for (const p of presets) {
    groups.set(p.group, [...(groups.get(p.group) ?? []), p]);
  }
  return [...groups.entries()];
}
