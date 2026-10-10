import { ArchiveRestore, CloudDownload, Lock, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { OrphanedBackup, RemoteBackup } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Dialog, ErrorState, Select, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatBytes, formatDateTime } from "@/lib/format";
import { RestoreNewDialog } from "@/features/projects/RestoreNewDialog";
import { offsiteKey } from "@/features/offsite/OffsiteTargetsCard";

/**
 * The backups of deleted projects. Deleting a project leaves its backups behind; from here
 * one becomes a project again, or goes for good. Backups on an offsite target come back
 * the same way, also of projects this Envoryx never had (a lost host).
 */
export function DeletedProjectBackupsCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["backups", "orphaned"], queryFn: async () => (await api.backups.orphaned()).backups });
  const [restoring, setRestoring] = useState<OrphanedBackup | null>(null);
  const [deleting, setDeleting] = useState<OrphanedBackup | null>(null);
  const [error, setError] = useState<string | null>(null);
  const remove = useMutation({
    mutationFn: (o: OrphanedBackup) => api.backups.removeOrphaned(o.projectId, o.backup.id),
    onSuccess: () => {
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: ["backups", "orphaned"] });
    },
    onError: (err) => {
      setDeleting(null);
      setError(errorText(err, t, t("Delete failed")));
    },
  });

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <ArchiveRestore className="size-4 text-accent-500" aria-hidden />
            {t("Backups of deleted projects")}
          </span>
        }
        description={t("Deleting a project keeps its backups. Restore one into a new project to bring the project back, or delete the backups you no longer need.")}
      />
      {error && (
        <div className="px-5 pt-4">
          <Alert tone="red">{error}</Alert>
        </div>
      )}
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <ErrorState message={errorText(q.error, t)} />
      ) : q.data.length === 0 ? (
        <p className="px-5 py-8 text-center text-sm text-muted">{t("No backups of deleted projects.")}</p>
      ) : (
        <ul className="divide-y divide-[var(--border)]">
          {q.data.map((o) => (
            <li key={o.backup.id} className="flex flex-wrap items-center gap-x-6 gap-y-2 px-5 py-3">
              <div className="min-w-[14rem] flex-1">
                <p className="text-sm font-medium text-fg">
                  {o.projectName || o.slug}
                  <span className="ml-2 font-normal text-muted">{formatDateTime(o.backup.createdAt)}</span>
                  {o.backup.meta.note && <span className="ml-2 font-normal text-muted">- {o.backup.meta.note}</span>}
                </p>
                <p className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-subtle">
                  {o.backup.meta.database && <Badge tone="amber">{o.backup.meta.database.type} {o.backup.meta.database.version}</Badge>}
                  {o.backup.meta.files && <Badge>{t("{{count}} files", { count: o.backup.meta.files.entries })}</Badge>}
                  {o.backup.meta.storage && <Badge tone="blue">{t("{{count}} objects", { count: o.backup.meta.storage.objects })}</Badge>}
                  {o.backup.missing && <Badge tone="red">{t("files missing")}</Badge>}
                  <span className="font-mono">{o.slug}/{o.backup.dir}</span>
                </p>
              </div>
              <span className="text-xs tabular-nums text-muted">{formatBytes(o.backup.sizeBytes)}</span>
              <div className="flex items-center gap-1.5">
                <Button size="sm" onClick={() => { setError(null); setRestoring(o); }} disabled={o.backup.missing} icon={<ArchiveRestore className="size-3.5" />}>
                  {t("Restore into a new project")}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setDeleting(o)} aria-label={t("Delete backup")}>
                  <Trash2 className="size-4" />
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <OffsiteProjects onFetched={(o) => { setError(null); setRestoring(o); }} />
      <RestoreNewDialog projectId={restoring?.projectId ?? ""} projectName={restoring ? restoring.projectName || restoring.slug : ""} backup={restoring?.backup ?? null} onClose={() => setRestoring(null)} />
      <Dialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title={t("Delete backup?")}
        description={deleting ? t("The backup of {{name}} from {{date}} is removed for good; copies on offsite targets stay there.", { name: deleting.projectName || deleting.slug, date: formatDateTime(deleting.backup.createdAt) }) : undefined}
        footer={
          <>
            <Button onClick={() => setDeleting(null)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => deleting && remove.mutate(deleting)} icon={<Trash2 className="size-4" />}>
              {t("Delete")}
            </Button>
          </>
        }
      />
    </Card>
  );
}

/**
 * The project backups on an offsite target, grouped by project. Fetching one stores it
 * with its project (or with the backups of deleted projects) and opens the restore.
 */
function OffsiteProjects({ onFetched }: { onFetched: (o: OrphanedBackup) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const targets = useQuery({ queryKey: offsiteKey, queryFn: api.offsite.list });
  const list = (targets.data?.targets ?? []).filter((x) => x.enabled);
  const [picked, setPicked] = useState("");
  const targetId = picked || list[0]?.id || "";
  const [open, setOpen] = useState(false);
  const q = useQuery({ queryKey: [...offsiteKey, targetId, "projects"], queryFn: () => api.offsite.remoteProjects(targetId), enabled: open && targetId !== "", retry: false });
  const [error, setError] = useState<string | null>(null);
  const fetch = useMutation({
    mutationFn: (b: RemoteBackup) => api.offsite.fetchProject(targetId, b.key),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["backups", "orphaned"] });
      onFetched(r.backup);
    },
    onError: (err) => setError(errorText(err, t, t("Fetching the backup failed"))),
  });

  if (list.length === 0) return null;
  const name = list.find((x) => x.id === targetId)?.name ?? "";
  return (
    <div className="space-y-3 border-t border-default px-5 py-4">
      <p className="text-sm font-medium text-fg">{t("From an offsite target")}</p>
      <p className="text-xs text-muted">{t("The project backups on the target, also of projects this Envoryx doesn't have. One is fetched and restored into a new project; it needs the same secret key as the Envoryx that made it.")}</p>
      <div className="flex flex-wrap items-center gap-2">
        {list.length > 1 && (
          <div className="w-64 max-w-full">
            <Select aria-label={t("Offsite target")} value={targetId} onChange={(e) => { setPicked(e.target.value); setError(null); }}>
              {list.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.name}
                </option>
              ))}
            </Select>
          </div>
        )}
        <Button size="sm" onClick={() => (open ? void q.refetch() : setOpen(true))} loading={q.isFetching} icon={<RefreshCw className="size-3.5" />}>
          {open ? t("Refresh") : t("Show projects on {{name}}", { name })}
        </Button>
      </div>
      {error && <Alert tone="red">{error}</Alert>}
      {open &&
        (q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <Alert tone="red">{errorText(q.error, t)}</Alert>
        ) : q.data.projects.length === 0 ? (
          <p className="text-sm text-muted">{t("No project backups on this target.")}</p>
        ) : (
          <div className="space-y-3">
            {q.data.projects.map((p) => (
              <div key={p.slug}>
                <p className="mb-1 font-mono text-xs text-subtle">{p.slug}</p>
                <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
                  {p.backups.map((b) => (
                    <li key={b.key} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm">
                      <p className="flex flex-wrap items-center gap-2">
                        {formatDateTime(b.createdAt)}
                        <Badge>{b.kind}</Badge>
                        {b.source === "scheduled" && <Badge tone="blue">{t("scheduled")}</Badge>}
                        {b.encrypted && (
                          <span title={t("encrypted")}>
                            <Lock className="size-3.5 text-muted" aria-label={t("encrypted")} />
                          </span>
                        )}
                        <span className="text-xs tabular-nums text-muted">{formatBytes(b.sizeBytes)}</span>
                      </p>
                      <Button size="sm" loading={fetch.isPending && fetch.variables?.key === b.key} disabled={fetch.isPending} onClick={() => { setError(null); fetch.mutate(b); }} icon={<CloudDownload className="size-3.5" />}>
                        {t("Restore into a new project")}
                      </Button>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        ))}
    </div>
  );
}
