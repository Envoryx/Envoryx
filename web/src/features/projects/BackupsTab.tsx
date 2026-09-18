import { Archive, Download, RotateCcw, Trash2 } from "lucide-react";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { keys } from "@/api/hooks";
import type { BackupInfo, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Dialog, ErrorState, Field, Input, Spinner } from "@/components/ui";
import { formatBytes, formatDateTime } from "@/lib/format";

export function BackupsTab({ project }: { project: Project }) {
  const qc = useQueryClient();
  const hasDb = project.services.some((s) => s.kind === "database" && s.enabled);
  const backups = useQuery({ queryKey: ["projects", project.id, "backups"], queryFn: async () => (await api.backups.list(project.id)).backups });
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["projects", project.id, "backups"] });
    void qc.invalidateQueries({ queryKey: keys.project(project.id) });
  };
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : fallback });

  const [withDb, setWithDb] = useState(hasDb);
  const [withFiles, setWithFiles] = useState(true);
  const [withDeps, setWithDeps] = useState(false);
  const [note, setNote] = useState("");
  const create = useMutation({
    mutationFn: () => api.backups.create(project.id, { database: withDb && hasDb, files: withFiles, includeDependencies: withDeps, note }),
    onSuccess: (res) => {
      setNote("");
      setMsg({ tone: "green", text: `Backup created (${formatBytes(res.backup.sizeBytes)}).` });
      refresh();
    },
    onError: (err) => fail(err, "Backup failed"),
  });

  const [restoreTarget, setRestoreTarget] = useState<BackupInfo | null>(null);
  const [rDb, setRDb] = useState(true);
  const [rFiles, setRFiles] = useState(true);
  const [rWipe, setRWipe] = useState(false);
  const [rConfirm, setRConfirm] = useState("");
  const restore = useMutation({
    mutationFn: (b: BackupInfo) => api.backups.restore(project.id, b.id, { database: rDb && !!b.meta.database && hasDb, files: rFiles && !!b.meta.files, wipeFiles: rWipe, confirm: rConfirm }),
    onSuccess: () => {
      setRestoreTarget(null);
      setMsg({ tone: "green", text: "Backup restored." });
      refresh();
    },
    onError: (err) => {
      setRestoreTarget(null);
      fail(err, "Restore failed");
    },
  });

  const [deleteTarget, setDeleteTarget] = useState<BackupInfo | null>(null);
  const remove = useMutation({
    mutationFn: (b: BackupInfo) => api.backups.remove(project.id, b.id),
    onSuccess: () => {
      setDeleteTarget(null);
      refresh();
    },
    onError: (err) => {
      setDeleteTarget(null);
      fail(err, "Delete failed");
    },
  });

  const openRestore = (b: BackupInfo) => {
    setRDb(!!b.meta.database && hasDb);
    setRFiles(!!b.meta.files);
    setRWipe(false);
    setRConfirm("");
    setRestoreTarget(b);
  };

  return (
    <div className="space-y-6">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <Archive className="size-4 text-accent-500" aria-hidden /> Create backup
            </span>
          }
          description={
            <>
              Stored under <Code>/config/backups/{project.slug}/</Code>. The database is dumped inside its container; files are archived by Staqio.
            </>
          }
        />
        <div className="space-y-4 p-5">
          <div className="grid gap-3 sm:grid-cols-3">
            <Checkbox label="Database" description={hasDb ? "Logical dump of the primary database" : "Project has no database"} checked={withDb && hasDb} disabled={!hasDb} onChange={(e) => setWithDb(e.target.checked)} />
            <Checkbox label="Project files" description="Everything in the project directory" checked={withFiles} onChange={(e) => setWithFiles(e.target.checked)} />
            <Checkbox label="Include dependencies" description="Keep vendor/ and node_modules/ (large, reproducible)" checked={withDeps} disabled={!withFiles} onChange={(e) => setWithDeps(e.target.checked)} />
          </div>
          <div className="flex items-end gap-2">
            <Field label="Note (optional)" htmlFor="backup-note">
              <Input id="backup-note" value={note} onChange={(e) => setNote(e.target.value)} placeholder="before upgrade to Laravel 13" maxLength={500} />
            </Field>
            <Button variant="primary" loading={create.isPending} disabled={!(withDb && hasDb) && !withFiles} onClick={() => create.mutate()} icon={<Archive className="size-4" />}>
              Create backup
            </Button>
          </div>
        </div>
      </Card>

      <Card>
        <CardHeader title="Backups" description="Newest first. Restoring overwrites the current database and/or files – Staqio asks for confirmation." />
        {backups.isPending ? (
          <Spinner />
        ) : backups.isError ? (
          <ErrorState message={backups.error.message} />
        ) : backups.data.length === 0 ? (
          <p className="px-5 py-8 text-center text-sm text-muted">No backups yet.</p>
        ) : (
          <ul className="divide-y divide-[var(--border)]">
            {backups.data.map((b) => (
              <li key={b.id} className="flex flex-wrap items-center gap-x-6 gap-y-2 px-5 py-3">
                <div className="min-w-[14rem] flex-1">
                  <p className="text-sm font-medium text-fg">
                    {formatDateTime(b.createdAt)}
                    {b.meta.note && <span className="ml-2 font-normal text-muted">– {b.meta.note}</span>}
                  </p>
                  <p className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-subtle">
                    {b.meta.database && <Badge tone="amber">{b.meta.database.type} {b.meta.database.version} · {formatBytes(b.meta.database.bytes)}</Badge>}
                    {b.meta.files && (
                      <Badge>
                        {b.meta.files.entries} files · {formatBytes(b.meta.files.bytes)}
                        {b.meta.files.includeDependencies ? " · with deps" : ""}
                      </Badge>
                    )}
                    {b.missing && <Badge tone="red">files missing</Badge>}
                    <span className="font-mono">{b.dir}</span>
                  </p>
                </div>
                <span className="text-xs tabular-nums text-muted">{formatBytes(b.sizeBytes)}</span>
                <div className="flex items-center gap-1.5">
                  <a
                    href={`/api/v1/projects/${encodeURIComponent(project.id)}/backups/${encodeURIComponent(b.id)}/download`}
                    className="inline-flex h-8 items-center gap-1.5 rounded-md border border-default bg-elevated px-2.5 text-xs font-medium text-fg hover:bg-muted"
                    download
                  >
                    <Download className="size-3.5" aria-hidden /> Download
                  </a>
                  <Button size="sm" onClick={() => openRestore(b)} disabled={b.missing} icon={<RotateCcw className="size-3.5" />}>
                    Restore
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setDeleteTarget(b)} aria-label="Delete backup">
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Dialog
        open={restoreTarget !== null}
        onClose={() => setRestoreTarget(null)}
        title="Restore backup?"
        description={restoreTarget ? `From ${formatDateTime(restoreTarget.createdAt)}${restoreTarget.meta.note ? ` – ${restoreTarget.meta.note}` : ""}` : undefined}
        footer={
          <>
            <Button onClick={() => setRestoreTarget(null)}>Cancel</Button>
            <Button variant="danger" loading={restore.isPending} disabled={rConfirm !== project.slug || (!rDb && !rFiles)} onClick={() => restoreTarget && restore.mutate(restoreTarget)} icon={<RotateCcw className="size-4" />}>
              Restore
            </Button>
          </>
        }
      >
        {restoreTarget && (
          <div className="space-y-4">
            <Alert tone="red" title="This overwrites current data">
              {rDb && restoreTarget.meta.database && <p>The database “{restoreTarget.meta.database.name}” is replaced by the dump; changes since the backup are lost.</p>}
              {rFiles && restoreTarget.meta.files && <p>Files in the archive overwrite the project directory{rWipe ? "; everything else in the directory is deleted first" : "; files not in the backup are kept"}.</p>}
            </Alert>
            <Checkbox label="Restore database" checked={rDb} disabled={!restoreTarget.meta.database || !hasDb} onChange={(e) => setRDb(e.target.checked)} description={!restoreTarget.meta.database ? "not in this backup" : !hasDb ? "project has no database" : undefined} />
            <Checkbox label="Restore files" checked={rFiles} disabled={!restoreTarget.meta.files} onChange={(e) => setRFiles(e.target.checked)} description={!restoreTarget.meta.files ? "not in this backup" : undefined} />
            {rFiles && <Checkbox label="Empty the project directory first" description="Makes the directory match the backup exactly (also removes vendor/ and node_modules/ if they were not included)." checked={rWipe} onChange={(e) => setRWipe(e.target.checked)} />}
            <Field label={`Type ${project.slug} to confirm`} htmlFor="restore-confirm">
              <Input id="restore-confirm" value={rConfirm} onChange={(e) => setRConfirm(e.target.value)} autoComplete="off" />
            </Field>
          </div>
        )}
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title="Delete backup?"
        description={deleteTarget ? `${formatDateTime(deleteTarget.createdAt)} · ${formatBytes(deleteTarget.sizeBytes)}` : undefined}
        footer={
          <>
            <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => deleteTarget && remove.mutate(deleteTarget)} icon={<Trash2 className="size-4" />}>
              Delete
            </Button>
          </>
        }
      />
    </div>
  );
}
