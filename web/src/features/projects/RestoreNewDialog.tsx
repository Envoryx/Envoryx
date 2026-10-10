import { ArchiveRestore } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { useRestoreNewProject } from "@/api/hooks";
import type { BackupInfo, BackupMeta, RestoreNewRequest } from "@/api/types";
import { Alert, Button, Checkbox, Dialog, Field, Input } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatDateTime, slugify } from "@/lib/format";
import { CreateProgress } from "./CreateProgress";

/** Whether a backup holds any database data: a dump, a copy of a data volume or addon volumes. */
function hasDatabase(meta: BackupMeta): boolean {
  return !!meta.database || (meta.databases?.length ?? 0) > 0 || (meta.databaseVolumes?.length ?? 0) > 0 || (meta.addonVolumes?.length ?? 0) > 0;
}

/** Whether a backup was made before backups carried workers, cron jobs, limits, health check and proxy rules (0.25). */
export function madeBefore025(meta: BackupMeta): string | null {
  const m = /^v?(\d+)\.(\d+)\./.exec(meta.envoryx ?? "");
  if (!m) return null;
  const [major, minor] = [Number(m[1]), Number(m[2])];
  return major === 0 && minor < 25 ? `${major}.${minor}` : null;
}

/**
 * Restores a backup into a new project: the settings the backup was made with (services,
 * variables, workers, cron jobs, repository) and, as ticked, its database, files and
 * objects. The backup may belong to a project that still exists or to a deleted one.
 */
export function RestoreNewDialog({ projectId, projectName, backup, onClose }: { projectId: string; projectName: string; backup: BackupInfo | null; onClose: () => void }) {
  const { t } = useTranslation();
  const restore = useRestoreNewProject();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [pathTouched, setPathTouched] = useState(false);
  const [parts, setParts] = useState({ database: true, files: true, storage: true, start: false });
  const [error, setError] = useState<string | null>(null);

  const suggestion = t("{{name}} Restored", { name: projectName });
  const wanted = name.trim() || suggestion;
  const slug = slugify(wanted);
  const dir = pathTouched ? path.trim() : slug;

  const close = () => {
    if (restore.isPending) return;
    setName("");
    setPath("");
    setPathTouched(false);
    setParts({ database: true, files: true, storage: true, start: false });
    setError(null);
    onClose();
  };

  const submit = () => {
    if (!backup) return;
    setError(null);
    const body: RestoreNewRequest = {
      name: wanted,
      database: parts.database && hasDatabase(backup.meta),
      files: parts.files && !!backup.meta.files,
      storage: parts.storage && !!backup.meta.storage,
      start: parts.start,
    };
    if (dir !== slug) body.path = dir;
    restore.mutate(
      { projectId, backupId: backup.id, body },
      {
        onSuccess: (p) => {
          close();
          navigate(`/projects/${p.id}`);
        },
        onError: (err) => setError(errorText(err, t, t("Restoring failed"))),
      },
    );
  };

  return (
    <Dialog
      open={backup !== null}
      onClose={close}
      title={t("Restore into a new project")}
      description={backup ? t("The backup of {{name}} from {{date}}. The new project gets the services, variables, workers, cron jobs and repository the backup was made with, and its own directory, host ports and containers.", { name: projectName, date: formatDateTime(backup.createdAt) }) : undefined}
      footer={
        <>
          <Button onClick={close} disabled={restore.isPending}>
            {t("Cancel")}
          </Button>
          <Button variant="primary" onClick={submit} loading={restore.isPending} disabled={slug === ""} icon={<ArchiveRestore className="size-4" />}>
            {t("Restore into a new project")}
          </Button>
        </>
      }
    >
      {backup && (
        <div className="space-y-4">
          {error && <Alert tone="red">{error}</Alert>}
          {madeBefore025(backup.meta) && (
            <Alert tone="amber">{t("Envoryx {{version}} made this backup, before backups carried the workers, cron jobs, resource limits, health check and proxy rules. The new project starts without them; add them again afterwards.", { version: madeBefore025(backup.meta) })}</Alert>
          )}
          <Field label={t("Name of the new project")} htmlFor="restore-new-name" hint={t("Identifier: {{slug}}", { slug: slug || "-" })}>
            <Input id="restore-new-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={suggestion} autoComplete="off" spellCheck={false} />
          </Field>
          <Field label={t("Directory")} htmlFor="restore-new-path" hint={t("Below the projects directory.")}>
            <Input id="restore-new-path" value={pathTouched ? path : slug} onChange={(e) => { setPath(e.target.value); setPathTouched(true); }} spellCheck={false} />
          </Field>
          <div className="space-y-2">
            <Checkbox
              label={t("Restore database")}
              checked={parts.database && hasDatabase(backup.meta)}
              disabled={!hasDatabase(backup.meta)}
              description={!hasDatabase(backup.meta) ? t("not in this backup") : undefined}
              onChange={(e) => setParts({ ...parts, database: e.target.checked })}
            />
            <Checkbox
              label={t("Restore files")}
              checked={parts.files && !!backup.meta.files}
              disabled={!backup.meta.files}
              description={!backup.meta.files ? t("not in this backup") : undefined}
              onChange={(e) => setParts({ ...parts, files: e.target.checked })}
            />
            {backup.meta.storage && <Checkbox label={t("Restore object storage")} checked={parts.storage} onChange={(e) => setParts({ ...parts, storage: e.target.checked })} />}
            <Checkbox label={t("Start the project when it is ready")} checked={parts.start} onChange={(e) => setParts({ ...parts, start: e.target.checked })} />
          </div>
          {restore.isPending && <CreateProgress slug={slug} action="restore-new" title={t("Restoring the backup…")} hint={t("The files and the database are restored into the new project; big backups take a moment.")} />}
        </div>
      )}
    </Dialog>
  );
}
