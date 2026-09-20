import { Archive, Download, Plus, RotateCcw, Trash2, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { InstanceBackup } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Code, Dialog, Field, Input, Spinner } from "@/components/ui";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";

const key = ["instance-backups"] as const;

function kindTone(kind: string): "green" | "gray" | "amber" | "blue" {
  switch (kind) {
    case "manual":
      return "blue";
    case "upload":
      return "green";
    case "pre-restore":
      return "amber";
    default:
      return "gray";
  }
}

/** After the restart was requested: gives the old process time to go away, then polls
 * /health until the new one answers and reloads the page. */
function useWaitForRestart(active: boolean) {
  useEffect(() => {
    if (!active) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    const poll = async () => {
      if (stopped) return;
      try {
        await api.health();
        window.location.reload();
        return;
      } catch {
        timer = setTimeout(() => void poll(), 1500);
      }
    };
    timer = setTimeout(() => void poll(), 3000);
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [active]);
}

export function InstanceBackupsCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: key, queryFn: api.instanceBackups.list });
  const [note, setNote] = useState("");
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [restoreTarget, setRestoreTarget] = useState<InstanceBackup | null>(null);
  const [confirm, setConfirm] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<InstanceBackup | null>(null);
  const [restarting, setRestarting] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  useWaitForRestart(restarting);

  const invalidate = () => void qc.invalidateQueries({ queryKey: key });
  const fail = (fallback: string) => (err: unknown) => setMsg({ tone: "red", text: errorText(err, t, fallback) });

  const create = useMutation({
    mutationFn: (n: string) => api.instanceBackups.create(n),
    onSuccess: (r) => {
      setNote("");
      setMsg({ tone: "green", text: t("Backup {{id}} created ({{size}}).", { id: r.backup.id, size: formatBytes(r.backup.sizeBytes) }) });
      invalidate();
    },
    onError: fail(t("Creating the backup failed")),
  });
  const upload = useMutation({
    mutationFn: (file: File) => api.instanceBackups.upload(file),
    onSuccess: (r) => {
      setMsg({ tone: "green", text: t("Backup imported as {{id}} (created {{date}} with Envoryx {{version}}).", { id: r.backup.id, date: formatDateTime(r.backup.meta.createdAt), version: r.backup.meta.envoryx }) });
      invalidate();
    },
    onError: fail(t("Importing the backup failed")),
  });
  const remove = useMutation({
    mutationFn: (b: InstanceBackup) => api.instanceBackups.remove(b.id),
    onSuccess: () => {
      setDeleteTarget(null);
      invalidate();
    },
    onError: fail(t("Deleting the backup failed")),
  });
  const restore = useMutation({
    mutationFn: (b: InstanceBackup) => api.instanceBackups.restore(b.id, confirm),
    onSuccess: (r) => {
      setRestoreTarget(null);
      setConfirm("");
      if (r.restarting) {
        setRestarting(true);
      } else {
        setMsg({ tone: "green", text: t("Restore scheduled – restart the Envoryx container to apply it.") });
        invalidate();
      }
    },
    onError: fail(t("Scheduling the restore failed")),
  });
  const cancel = useMutation({
    mutationFn: () => api.instanceBackups.cancelRestore(),
    onSuccess: invalidate,
    onError: fail(t("Cancelling failed")),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    create.mutate(note.trim());
  }

  function pickFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    setMsg(null);
    upload.mutate(file);
  }

  if (restarting) {
    return (
      <Card>
        <CardHeader title={t("Instance backups")} />
        <div className="p-5">
          <Alert tone="amber" title={t("Envoryx is restarting to apply the restore…")}>
            <div className="mt-2 flex items-center gap-3">
              <Spinner />
              <span>{t("This page reloads automatically. You will need to sign in again.")}</span>
            </div>
          </Alert>
        </div>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader
        title={t("Instance backups")}
        description={t("A snapshot of Envoryx itself: database (accounts, projects, tokens, settings), local CA, SSH keys, notification settings and generated project configuration. Project files and Docker volumes are covered by project backups. One is taken automatically before every schema upgrade.")}
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {q.data?.pendingRestore && (
          <Alert tone="amber" title={t("A restore of {{id}} is scheduled", { id: q.data.pendingRestore.id })}>
            <p>{t("It is applied when Envoryx starts next. Until then the current state stays in place.")}</p>
            <Button size="sm" className="mt-2" onClick={() => cancel.mutate()} loading={cancel.isPending}>
              {t("Cancel restore")}
            </Button>
          </Alert>
        )}
        {q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <Alert tone="red">{errorText(q.error, t)}</Alert>
        ) : q.data.backups.length === 0 ? (
          <p className="text-sm text-muted">{t("No instance backups yet.")}</p>
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {q.data.backups.map((b) => (
              <li key={b.id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm">
                <div className="flex items-center gap-3">
                  <Archive className="size-4 text-accent-500" aria-hidden />
                  <div>
                    <p className="flex flex-wrap items-center gap-2 font-medium">
                      {formatDateTime(b.createdAt)}
                      <Badge tone={kindTone(b.kind)}>{b.kind}</Badge>
                    </p>
                    <p className="font-mono text-[11px] text-subtle">
                      {b.id} · {formatBytes(b.sizeBytes)} · Envoryx {b.meta.envoryx} · {t("schema {{n}}", { n: b.meta.schema })}
                      {b.meta.note ? ` · ${b.meta.note}` : ""}
                    </p>
                  </div>
                </div>
                <div className="flex items-center gap-1">
                  <a href={api.instanceBackups.downloadUrl(b.id)} download className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-muted hover:bg-muted hover:text-fg">
                    <Download className="size-3.5" aria-hidden />
                    {t("Download")}
                  </a>
                  <Button size="sm" variant="ghost" onClick={() => setRestoreTarget(b)} icon={<RotateCcw className="size-3.5" />} aria-label={t("Restore {{id}}", { id: b.id })}>
                    {t("Restore")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setDeleteTarget(b)} icon={<Trash2 className="size-3.5" />} aria-label={t("Delete {{id}}", { id: b.id })}>
                    {t("Delete")}
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <form onSubmit={submit} className="flex flex-wrap items-end gap-2">
          <div className="min-w-48 flex-1">
            <Field label={t("Note")} htmlFor="instance-backup-note" hint={t("Optional, e.g. why you are taking this backup.")}>
              <Input id="instance-backup-note" value={note} onChange={(e) => setNote(e.target.value)} placeholder={t("before update to 1.2")} maxLength={500} />
            </Field>
          </div>
          <Button type="submit" variant="primary" className="mb-6" loading={create.isPending} icon={<Plus className="size-4" />}>
            {t("Create backup")}
          </Button>
          <input ref={fileInput} type="file" accept=".tar.gz,application/gzip" className="hidden" onChange={pickFile} aria-label={t("Backup file")} />
          <Button type="button" className="mb-6" loading={upload.isPending} onClick={() => fileInput.current?.click()} icon={<Upload className="size-4" />}>
            {t("Import backup")}
          </Button>
        </form>
        {q.data && (
          <p className="text-xs text-subtle">
            {t("Stored in")} <Code>{q.data.dir}</Code>. {t("Automatic backups (pre-migrate, pre-restore) keep the last 5; yours stay until you delete them.")}
          </p>
        )}
      </div>

      <Dialog
        open={restoreTarget !== null}
        onClose={() => {
          setRestoreTarget(null);
          setConfirm("");
        }}
        title={t("Restore instance backup?")}
        description={restoreTarget ? `${formatDateTime(restoreTarget.createdAt)} · Envoryx ${restoreTarget.meta.envoryx} · ${formatBytes(restoreTarget.sizeBytes)}` : undefined}
        footer={
          <>
            <Button
              onClick={() => {
                setRestoreTarget(null);
                setConfirm("");
              }}
            >
              {t("Cancel")}
            </Button>
            <Button variant="danger" loading={restore.isPending} disabled={confirm !== "restore"} onClick={() => restoreTarget && restore.mutate(restoreTarget)} icon={<RotateCcw className="size-4" />}>
              {t("Restore and restart")}
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Alert tone="red" title={t("This replaces the Envoryx configuration")}>
            <p>{t("Database, CA, SSH keys, notification settings and project configuration are replaced by the backup; changes since then are lost. A backup of the current state is taken first (pre-restore).")}</p>
            <p className="mt-1">{t("Project files and Docker containers are not touched. Projects created after the backup show up as orphans and can be cleaned up afterwards.")}</p>
            <p className="mt-1">{t("Envoryx restarts to apply the restore; all sessions end.")}</p>
          </Alert>
          <Field label={t("Type restore to confirm")} htmlFor="instance-restore-confirm">
            <Input id="instance-restore-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
          </Field>
        </div>
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title={t("Delete backup?")}
        description={deleteTarget ? `${deleteTarget.id} · ${formatBytes(deleteTarget.sizeBytes)}` : undefined}
        footer={
          <>
            <Button onClick={() => setDeleteTarget(null)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => deleteTarget && remove.mutate(deleteTarget)} icon={<Trash2 className="size-4" />}>
              {t("Delete")}
            </Button>
          </>
        }
      />
    </Card>
  );
}
