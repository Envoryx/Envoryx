import { Cog, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { keys } from "@/api/hooks";
import type { Project, Worker, WorkerPreset } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Code, ErrorState, Field, Input, Select, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone } from "@/lib/format";

export function WorkersTab({ project }: { project: Project }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["projects", project.id, "workers"], queryFn: () => api.projects.workers.list(project.id) });
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["projects", project.id, "workers"] });
    void qc.invalidateQueries({ queryKey: keys.project(project.id) });
  };
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : fallback });
  const [name, setName] = useState("");
  const [preset, setPreset] = useState("laravel:queue");
  const [arg, setArg] = useState("");
  const add = useMutation({
    mutationFn: () => api.projects.workers.add(project.id, { name: name.trim(), preset, arg: arg.trim(), enabled: true }),
    onSuccess: (r) => {
      setName("");
      setArg("");
      setMsg({ tone: "green", text: `Worker "${r.worker.name}" added and started.` });
      refresh();
    },
    onError: (err) => fail(err, "Adding the worker failed"),
  });
  const toggle = useMutation({
    mutationFn: (w: Worker) => api.projects.workers.update(project.id, w.id, { name: w.name, preset: w.preset, arg: w.arg, enabled: !w.enabled }),
    onSuccess: refresh,
    onError: (err) => fail(err, "Updating the worker failed"),
  });
  const remove = useMutation({
    mutationFn: (w: Worker) => api.projects.workers.remove(project.id, w.id),
    onSuccess: refresh,
    onError: (err) => fail(err, "Removing the worker failed"),
  });
  const hasPhp = project.services.some((s) => s.kind === "php" && s.enabled);

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={q.error.message} />;
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
      {!hasPhp && <Alert tone="amber">Workers run from the PHP image – this project has no PHP service.</Alert>}
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <Cog className="size-4 text-accent-500" aria-hidden /> Workers
            </span>
          }
          description="Long-running processes next to the web server: queue workers, schedulers, WebSocket servers. Each runs in its own container from the PHP image, restarts automatically and follows start/stop of the project. Logs are in the Logs tab."
        />
        {q.data.workers.length === 0 ? (
          <p className="px-5 py-4 text-sm text-muted">No workers yet.</p>
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
                      <Badge>{p?.label ?? w.preset}</Badge>
                      {w.enabled ? (
                        st ? (
                          <span className="inline-flex items-center gap-1.5 text-xs font-normal text-muted">
                            <StatusDot tone={containerStateTone(st.state)} /> {st.state}
                          </span>
                        ) : null
                      ) : (
                        <Badge tone="gray">disabled</Badge>
                      )}
                    </p>
                    <p className="mt-0.5 font-mono text-[11px] text-subtle">{w.command.join(" ")}</p>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <Button size="sm" onClick={() => toggle.mutate(w)} loading={toggle.isPending && toggle.variables?.id === w.id}>
                      {w.enabled ? "Disable" : "Enable"}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => remove.mutate(w)} loading={remove.isPending && remove.variables?.id === w.id} icon={<Trash2 className="size-3.5" />} aria-label={`Remove ${w.name}`}>
                      Remove
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
        <form onSubmit={submit} className="space-y-4 border-t border-default p-5">
          <p className="text-sm font-medium text-fg">Add worker</p>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Name" htmlFor="worker-name" hint="Lower-case, e.g. queue">
              <Input id="worker-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="queue" spellCheck={false} autoCapitalize="none" />
            </Field>
            <Field label="Preset" htmlFor="worker-preset" hint={selected?.description}>
              <Select id="worker-preset" value={preset} onChange={(e) => { setPreset(e.target.value); setArg(""); }}>
                {groupPresets(presets).map(([group, items]) => (
                  <optgroup key={group} label={group}>
                    {items.map((p) => (
                      <option key={p.id} value={p.id}>{p.label}</option>
                    ))}
                  </optgroup>
                ))}
              </Select>
            </Field>
            {selected?.argLabel && (
              <Field label={selected.argLabel} htmlFor="worker-arg" hint={selected.argHint}>
                <Input id="worker-arg" value={arg} onChange={(e) => setArg(e.target.value)} spellCheck={false} />
              </Field>
            )}
          </div>
          {selected?.requires && selected.requires.length > 0 && (
            <p className="text-xs text-subtle">Expects <Code>{selected.requires.join(", ")}</Code> in the project directory.</p>
          )}
          <Button type="submit" variant="primary" loading={add.isPending} disabled={!name.trim() || !hasPhp} icon={<Plus className="size-4" />}>
            Add worker
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
