import { Container, Gauge, Layers, Network, RefreshCw, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys, useDockerOverview } from "@/api/hooks";
import type { ContainerSummary, Orphan } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, ErrorState, PageHeader, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone, formatBytes, formatRelative } from "@/lib/format";
import { errorText, translateMessage } from "@/lib/errors";
import { SectionLayout, type SectionGroup } from "@/components/SectionNav";
import { settingsHref } from "@/features/settings/links";

function ContainerTable({ rows, managed }: { rows: ContainerSummary[]; managed: boolean }) {
  const { t } = useTranslation();
  if (rows.length === 0) {
    return <p className="px-5 py-6 text-sm text-muted">{managed ? t("No Envoryx containers exist.") : t("No other containers on this host.")}</p>;
  }
  // Envoryx's containers come in groups, one per project, so each project reads as a block.
  const groups = new Map<string, ContainerSummary[]>();
  for (const c of rows) {
    const key = managed ? (c.projectId ?? "") : "";
    groups.set(key, [...(groups.get(key) ?? []), c]);
  }
  const cols = 5;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="text-left text-xs text-subtle">
          <tr className="border-b border-default">
            <th className="px-5 py-2 font-medium">{t("Name")}</th>
            <th className="px-3 py-2 font-medium">{t("Image")}</th>
            <th className="px-3 py-2 font-medium">{t("State")}</th>
            <th className="px-3 py-2 font-medium">{t("Ports")}</th>
            <th className="px-3 py-2 font-medium">{t("Created")}</th>
          </tr>
        </thead>
        {[...groups.entries()].map(([projectId, list]) => (
          <tbody key={projectId} className="divide-y divide-[var(--border)] border-b border-default last:border-b-0">
            {managed && (
              <tr className="bg-muted/50">
                <th colSpan={cols} scope="rowgroup" className="px-5 py-1.5 text-left text-xs font-medium">
                  {projectId ? (
                    <Link to={`/projects/${projectId}`} className="text-accent-600 hover:underline dark:text-accent-300">
                      {list[0]!.projectName || projectId}
                    </Link>
                  ) : (
                    <span className="text-muted">{t("Without a project")}</span>
                  )}
                  <span className="ml-2 font-normal text-subtle">{t("{{running}} / {{total}} running", { running: list.filter((c) => c.state === "running").length, total: list.length })}</span>
                </th>
              </tr>
            )}
            {list.map((c) => (
              <tr key={c.id}>
                <td className="px-5 py-2 font-mono text-xs text-fg">
                  {c.name}
                  {c.service && <Badge className="ml-1.5">{c.service}</Badge>}
                </td>
                <td className="px-3 py-2 font-mono text-xs text-muted">{c.image}</td>
                <td className="px-3 py-2">
                  <span className="inline-flex items-center gap-1.5 text-xs">
                    <StatusDot tone={containerStateTone(c.state)} />
                    {c.state}
                  </span>
                </td>
                <td className="px-3 py-2 font-mono text-xs text-muted">{c.ports.map((p) => `${p.hostPort}→${p.containerPort}`).join(", ") || "-"}</td>
                <td className="px-3 py-2 text-xs text-muted">{formatRelative(c.created, t)}</td>
              </tr>
            ))}
          </tbody>
        ))}
      </table>
    </div>
  );
}

/** Networks or volumes with the project their labels name. */
function ResourceList({ title, rows }: { title: string; rows: { key: string; name: string; driver: string; labels: Record<string, string> }[] }) {
  const { t } = useTranslation();
  return (
    <Card>
      <CardHeader title={title} />
      <ul className="divide-y divide-[var(--border)]">
        {rows.length === 0 && <li className="px-5 py-4 text-sm text-muted">{t("None")}</li>}
        {rows.map((r) => {
          const projectId = r.labels["envoryx.project.id"];
          return (
            <li key={r.key} className="flex items-center justify-between gap-3 px-5 py-2 text-xs">
              <span className="min-w-0 truncate font-mono text-fg">{r.name}</span>
              <span className="flex shrink-0 items-center gap-3">
                {projectId && (
                  <Link to={`/projects/${projectId}`} className="text-accent-600 hover:underline dark:text-accent-300">
                    {r.labels["envoryx.project.name"] || projectId}
                  </Link>
                )}
                <span className="text-subtle">{r.driver}</span>
              </span>
            </li>
          );
        })}
      </ul>
    </Card>
  );
}

/** Resources with Envoryx labels but no project. Containers and networks are cleared by
 * the reconciler; volumes hold data and wait for the user. */
function OrphansAlert({ orphans }: { orphans: Orphan[] }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const remove = useMutation({
    mutationFn: ({ type, id }: Orphan) => api.removeOrphan(type, id),
    onSuccess: () => {
      setError(null);
      void qc.invalidateQueries({ queryKey: keys.docker });
      void qc.invalidateQueries({ queryKey: keys.dashboard });
    },
    onError: (err) => setError(errorText(err, t)),
  });
  return (
    <div>
      <Alert tone="amber" title={t("{{count}} orphaned Envoryx resources", { count: orphans.length })}>
        <p>{t("These carry Envoryx labels but belong to no known project (e.g. after restoring an older database). Containers and networks are removed automatically within a minute; volumes hold data and stay until you remove them.")}</p>
        {error && <p className="mt-2 text-red-600 dark:text-red-400">{error}</p>}
        <ul className="mt-2 space-y-1 font-mono text-xs">
          {orphans.map((o) => (
            <li key={o.type + o.id} className="flex flex-wrap items-center gap-2">
              <span>
                {o.type} {o.name} <span className="text-subtle">({t("project")} {o.projectName || o.projectId})</span>
              </span>
              <Button
                size="sm"
                variant="danger"
                icon={<Trash2 className="size-3" />}
                loading={remove.isPending && remove.variables?.id === o.id}
                disabled={remove.isPending}
                onClick={() => remove.mutate(o)}
              >
                {t("Remove")}
              </Button>
            </li>
          ))}
        </ul>
      </Alert>
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
  const { t } = useTranslation();
  const [confirm, setConfirm] = useState(false);
  const total = (images.data ?? []).reduce((n, i) => n + i.size, 0);

  return (
    <Card>
      <CardHeader
        title={t("Unused runtime images")}
        description={t("Images Envoryx pulled (PHP, Node, Python, Go, Ruby, Java, .NET, MariaDB, Caddy, Apache, Nginx) that no container uses any more, e.g. after a version change. Images from other sources are never touched.")}
        actions={
          images.data && images.data.length > 0 && !confirm ? (
            <Button size="sm" onClick={() => setConfirm(true)} icon={<Trash2 className="size-3.5" />}>
              {t("Remove all ({{size}})", { size: formatBytes(total) })}
            </Button>
          ) : undefined
        }
      />
      <div className="p-5">
        {images.isPending ? (
          <Spinner />
        ) : images.isError ? (
          <Alert tone="red">{errorText(images.error, t)}</Alert>
        ) : images.data.length === 0 ? (
          <p className="text-sm text-muted">{t("No unused runtime images.")}</p>
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
          <Alert tone="amber" title={t("Remove these images?")}>
            {t("They are downloaded again automatically if a project needs them later.")}
            <div className="mt-2 flex gap-2">
              <Button size="sm" variant="danger" loading={prune.isPending} onClick={() => prune.mutate(undefined, { onSettled: () => setConfirm(false) })}>
                {t("Remove")}
              </Button>
              <Button size="sm" onClick={() => setConfirm(false)}>
                {t("Cancel")}
              </Button>
            </div>
          </Alert>
        )}
        {prune.data && (
          <p className="mt-3 text-xs text-muted">
            {t("Removed {{count}} images, {{size}} reclaimed.", { count: prune.data.removed.length, size: formatBytes(prune.data.reclaimedBytes) })}
            {prune.data.errors.length > 0 && <span className="text-red-500"> {prune.data.errors.join("; ")}</span>}
          </p>
        )}
        {prune.isError && <Alert tone="red">{errorText(prune.error, t)}</Alert>}
      </div>
    </Card>
  );
}

type Tab = "overview" | "containers" | "networks" | "images";

export function DockerPage() {
  const { t } = useTranslation();
  const q = useDockerOverview();
  const qc = useQueryClient();
  const [params] = useSearchParams();

  const reconcile = async () => {
    await api.reconcile();
    await qc.invalidateQueries({ queryKey: keys.docker });
    await qc.invalidateQueries({ queryKey: keys.dashboard });
  };

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} action={<Button onClick={() => void q.refetch()}>{t("Retry")}</Button>} />;
  const d = q.data;
  const groups: SectionGroup<Tab>[] = [
    { items: [{ id: "overview", label: "Overview", icon: Gauge, badge: d.orphans.length > 0 ? <Badge tone="amber">{d.orphans.length}</Badge> : undefined }] },
    {
      group: "Resources",
      items: [
        { id: "containers", label: "Containers", icon: Container },
        { id: "networks", label: "Networks & volumes", icon: Network },
        { id: "images", label: "Images", icon: Layers },
      ],
    },
  ];
  const requested = params.get("tab");
  const tab: Tab = requested === "containers" || requested === "networks" || requested === "images" ? requested : "overview";

  return (
    <div>
      <PageHeader
        title={t("Docker")}
        description={t("Envoryx only manages resources labelled envoryx.managed=true; everything else is shown read-only.")}
        actions={
          <Button onClick={() => void reconcile()} icon={<RefreshCw className="size-4" />}>
            {t("Reconcile now")}
          </Button>
        }
      />

      {!d.info.connected && (
        <div className="mb-6">
          <Alert tone="red" title={t("Docker engine unreachable")}>
            {translateMessage(d.info.error, t)}
          </Alert>
        </div>
      )}

      <SectionLayout label={t("Docker sections")} groups={groups} current={tab} href={(id) => (id === "overview" ? "/docker" : `/docker?tab=${id}`)}>
        {tab === "overview" && (
          <div className="space-y-6">
            <div className="grid gap-4 sm:grid-cols-3">
              <Card className="px-5 py-4">
                <p className="text-xs uppercase tracking-wide text-subtle">{t("Engine")}</p>
                <p className="mt-1 text-sm font-medium text-fg">{d.info.connected ? `${d.info.serverVersion} · API ${d.info.apiVersion}` : t("offline")}</p>
                <p className="text-xs text-muted">
                  {d.info.os} {d.info.architecture}
                </p>
              </Card>
              <Card className="px-5 py-4">
                <p className="text-xs uppercase tracking-wide text-subtle">{t("Host resources")}</p>
                <p className="mt-1 text-sm font-medium text-fg">
                  {t("{{count}} CPUs", { count: d.info.ncpu })} · {formatBytes(d.info.memTotal)}
                </p>
              </Card>
              <Card className="px-5 py-4">
                <p className="text-xs uppercase tracking-wide text-subtle">{t("Managed")}</p>
                <p className="mt-1 text-sm font-medium text-fg">
                  {t("{{containers}} containers · {{networks}} networks · {{volumes}} volumes", { containers: d.containers.length, networks: d.networks.length, volumes: d.volumes.length })}
                </p>
              </Card>
            </div>
            {d.orphans.length > 0 && <OrphansAlert orphans={d.orphans} />}
            <p className="text-xs text-subtle">
              {t("Host paths and the other set-up checks are part of the diagnostics.")}{" "}
              <Link to={settingsHref("diagnostics")} className="underline">
                {t("Open diagnostics")}
              </Link>
            </p>
          </div>
        )}
        {tab === "containers" && (
          <div className="space-y-6">
            <Card>
              <CardHeader title={t("Envoryx containers")} description={t("Managed by Envoryx and mapped to projects.")} />
              <ContainerTable rows={d.containers} managed />
            </Card>
            <Card>
              <CardHeader title={t("Other containers on this host")} description={t("Read-only. Envoryx never touches containers without the envoryx.managed=true label.")} />
              <ContainerTable rows={d.foreign} managed={false} />
            </Card>
          </div>
        )}
        {tab === "networks" && (
          <div className="space-y-6">
            <ResourceList title={t("Networks")} rows={d.networks.map((n) => ({ key: n.ID, name: n.Name, driver: n.Driver, labels: n.Labels ?? {} }))} />
            <ResourceList title={t("Volumes")} rows={d.volumes.map((v) => ({ key: v.Name, name: v.Name, driver: v.Driver, labels: v.Labels ?? {} }))} />
          </div>
        )}
        {tab === "images" && <UnusedImagesCard />}
      </SectionLayout>
    </div>
  );
}
