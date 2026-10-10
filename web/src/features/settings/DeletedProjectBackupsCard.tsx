import { ArchiveRestore, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { OrphanedBackup } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Dialog, ErrorState, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatBytes, formatDateTime } from "@/lib/format";
import { RestoreNewDialog } from "@/features/projects/RestoreNewDialog";

/**
 * The backups of deleted projects. Deleting a project leaves its backups behind; from here
 * one becomes a project again, or goes for good.
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
