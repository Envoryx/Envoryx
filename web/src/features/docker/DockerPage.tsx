import { RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys, useDockerOverview } from "@/api/hooks";
import type { ContainerSummary } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Code, ErrorState, PageHeader, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone, formatBytes, formatRelative } from "@/lib/format";

function ContainerTable({ rows, managed }: { rows: ContainerSummary[]; managed: boolean }) {
  if (rows.length === 0) {
    return <p className="px-5 py-6 text-sm text-muted">{managed ? "No Staqio containers exist." : "No other containers on this host."}</p>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="text-left text-xs text-subtle">
          <tr className="border-b border-default">
            <th className="px-5 py-2 font-medium">Name</th>
            <th className="px-3 py-2 font-medium">Image</th>
            <th className="px-3 py-2 font-medium">State</th>
            {managed && <th className="px-3 py-2 font-medium">Project</th>}
            <th className="px-3 py-2 font-medium">Ports</th>
            <th className="px-3 py-2 font-medium">Created</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-[var(--border)]">
          {rows.map((c) => (
            <tr key={c.id}>
              <td className="px-5 py-2 font-mono text-xs text-fg">{c.name}</td>
              <td className="px-3 py-2 font-mono text-xs text-muted">{c.image}</td>
              <td className="px-3 py-2">
                <span className="inline-flex items-center gap-1.5 text-xs">
                  <StatusDot tone={containerStateTone(c.state)} />
                  {c.state}
                </span>
              </td>
              {managed && (
                <td className="px-3 py-2 text-xs">
                  {c.projectId ? (
                    <Link to={`/projects/${c.projectId}`} className="text-accent-600 hover:underline dark:text-accent-300">
                      {c.projectName}
                    </Link>
                  ) : (
                    "—"
                  )}
                  {c.service && <Badge className="ml-1.5">{c.service}</Badge>}
                </td>
              )}
              <td className="px-3 py-2 font-mono text-xs text-muted">{c.ports.map((p) => `${p.hostPort}→${p.containerPort}`).join(", ") || "—"}</td>
              <td className="px-3 py-2 text-xs text-muted">{formatRelative(c.created)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function UnusedImagesCard() {
  const qc = useQueryClient();
  const images = useQuery({ queryKey: ["docker", "unused-images"], queryFn: async () => (await api.unusedImages()).images, refetchInterval: 15000 });
  const prune = useMutation({
    mutationFn: async () => (await api.pruneImages()).result,
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["docker", "unused-images"] }),
  });
  const [confirm, setConfirm] = useState(false);
  const total = (images.data ?? []).reduce((n, i) => n + i.size, 0);

  return (
    <Card>
      <CardHeader
        title="Unused runtime images"
        description="Images Staqio pulled (PHP, Node, MariaDB, Caddy) that no container uses any more, e.g. after a version change. Images from other sources are never touched."
        actions={
          images.data && images.data.length > 0 && !confirm ? (
            <Button size="sm" onClick={() => setConfirm(true)} icon={<Trash2 className="size-3.5" />}>
              Remove all ({formatBytes(total)})
            </Button>
          ) : undefined
        }
      />
      <div className="p-5">
        {images.isPending ? (
          <Spinner />
        ) : images.isError ? (
          <Alert tone="red">{images.error.message}</Alert>
        ) : images.data.length === 0 ? (
          <p className="text-sm text-muted">No unused runtime images.</p>
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {images.data.map((img) => (
              <li key={img.id} className="flex items-center justify-between px-3 py-2 text-xs">
                <span className="font-mono text-fg">{img.tags.join(", ")}</span>
                <span className="text-subtle">{formatBytes(img.size)}</span>
              </li>
            ))}
          </ul>
        )}
        {confirm && (
          <Alert tone="amber" title="Remove these images?">
            They are downloaded again automatically if a project needs them later.
            <div className="mt-2 flex gap-2">
              <Button size="sm" variant="danger" loading={prune.isPending} onClick={() => prune.mutate(undefined, { onSettled: () => setConfirm(false) })}>
                Remove
              </Button>
              <Button size="sm" onClick={() => setConfirm(false)}>
                Cancel
              </Button>
            </div>
          </Alert>
        )}
        {prune.data && (
          <p className="mt-3 text-xs text-muted">
            Removed {prune.data.removed.length} image{prune.data.removed.length === 1 ? "" : "s"}, {formatBytes(prune.data.reclaimedBytes)} reclaimed.
            {prune.data.errors.length > 0 && <span className="text-red-500"> {prune.data.errors.join("; ")}</span>}
          </p>
        )}
        {prune.isError && <Alert tone="red">{prune.error.message}</Alert>}
      </div>
    </Card>
  );
}

export function DockerPage() {
  const q = useDockerOverview();
  const qc = useQueryClient();

  const reconcile = async () => {
    await api.reconcile();
    await qc.invalidateQueries({ queryKey: keys.docker });
    await qc.invalidateQueries({ queryKey: keys.dashboard });
  };

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={q.error.message} action={<Button onClick={() => void q.refetch()}>Retry</Button>} />;
  const d = q.data;

  return (
    <div>
      <PageHeader
        title="Docker"
        description="Diagnostics view. Staqio only manages resources labelled staqio.managed=true; everything else is shown read-only."
        actions={
          <Button onClick={() => void reconcile()} icon={<RefreshCw className="size-4" />}>
            Reconcile now
          </Button>
        }
      />

      {!d.info.connected && (
        <div className="mb-6">
          <Alert tone="red" title="Docker engine unreachable">
            {d.info.error}
          </Alert>
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card className="px-5 py-4">
          <p className="text-xs uppercase tracking-wide text-subtle">Engine</p>
          <p className="mt-1 text-sm font-medium text-fg">{d.info.connected ? `${d.info.serverVersion} · API ${d.info.apiVersion}` : "offline"}</p>
          <p className="text-xs text-muted">
            {d.info.os} {d.info.architecture}
          </p>
        </Card>
        <Card className="px-5 py-4">
          <p className="text-xs uppercase tracking-wide text-subtle">Host resources</p>
          <p className="mt-1 text-sm font-medium text-fg">
            {d.info.ncpu} CPUs · {formatBytes(d.info.memTotal)}
          </p>
        </Card>
        <Card className="px-5 py-4">
          <p className="text-xs uppercase tracking-wide text-subtle">Managed</p>
          <p className="mt-1 text-sm font-medium text-fg">
            {d.containers.length} containers · {d.networks.length} networks · {d.volumes.length} volumes
          </p>
        </Card>
        <Card className="px-5 py-4">
          <p className="text-xs uppercase tracking-wide text-subtle">Host paths</p>
          {Object.entries({ ...d.hostPath.detected, ...d.hostPath.overrides }).length === 0 ? (
            <p className="mt-1 text-xs text-red-500">{d.hostPath.error ?? "unknown"}</p>
          ) : (
            <ul className="mt-1 space-y-0.5 font-mono text-[11px] text-muted">
              {Object.entries({ ...d.hostPath.detected, ...d.hostPath.overrides })
                .filter(([k]) => k === "/projects" || k === "/config")
                .map(([k, v]) => (
                  <li key={k} className="truncate">
                    {k} → {v}
                  </li>
                ))}
            </ul>
          )}
        </Card>
      </div>

      {d.orphans.length > 0 && (
        <div className="mt-6">
          <Alert tone="amber" title={`${d.orphans.length} orphaned Staqio resource${d.orphans.length === 1 ? "" : "s"}`}>
            <p>These carry Staqio labels but belong to no known project (e.g. after restoring an older database). They are never removed automatically.</p>
            <ul className="mt-2 list-disc pl-4 font-mono text-xs">
              {d.orphans.map((o) => (
                <li key={o.type + o.id}>
                  {o.type} {o.name} <span className="text-subtle">(project {o.projectName || o.projectId})</span>
                </li>
              ))}
            </ul>
          </Alert>
        </div>
      )}

      <div className="mt-6 space-y-6">
        <Card>
          <CardHeader title="Staqio containers" description="Managed by Staqio and mapped to projects." />
          <ContainerTable rows={d.containers} managed />
        </Card>
        <div className="grid gap-6 lg:grid-cols-2">
          <Card>
            <CardHeader title="Networks" />
            <ul className="divide-y divide-[var(--border)]">
              {d.networks.length === 0 && <li className="px-5 py-4 text-sm text-muted">None</li>}
              {d.networks.map((n) => (
                <li key={n.ID} className="flex items-center justify-between px-5 py-2 text-xs">
                  <span className="font-mono text-fg">{n.Name}</span>
                  <span className="text-subtle">{n.Driver}</span>
                </li>
              ))}
            </ul>
          </Card>
          <Card>
            <CardHeader title="Volumes" />
            <ul className="divide-y divide-[var(--border)]">
              {d.volumes.length === 0 && <li className="px-5 py-4 text-sm text-muted">None (databases with persistent volumes arrive in Phase 3)</li>}
              {d.volumes.map((v) => (
                <li key={v.Name} className="flex items-center justify-between px-5 py-2 text-xs">
                  <span className="font-mono text-fg">{v.Name}</span>
                  <span className="text-subtle">{v.Driver}</span>
                </li>
              ))}
            </ul>
          </Card>
        </div>
        <UnusedImagesCard />
        <Card>
          <CardHeader
            title="Other containers on this host"
            description={
              <>
                Read-only. Staqio never touches containers without the <Code>staqio.managed=true</Code> label.
              </>
            }
          />
          <ContainerTable rows={d.foreign} managed={false} />
        </Card>
      </div>
    </div>
  );
}
