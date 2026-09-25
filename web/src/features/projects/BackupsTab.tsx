import { Archive, CalendarClock, CloudDownload, Download, RotateCcw, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys } from "@/api/hooks";
import type { BackupInfo, BackupMeta, BackupSchedule, Project } from "@/api/types";
import { databaseServices } from "./databases";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Dialog, ErrorState, Field, Input, Select, Spinner } from "@/components/ui";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { OffsiteBadges, OffsiteUploadButton, RemoteBackups, uploading } from "@/features/offsite/OffsiteParts";

/** Whether a backup holds a dump of any database. */
function hasDumps(meta: BackupMeta): boolean {
  return !!meta.database || (meta.databases?.length ?? 0) > 0;
}

export function BackupsTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const dbCount = databaseServices(project).length;
  const hasDb = dbCount > 0;
  const hasStorage = project.services.some((s) => s.kind === "storage" && s.enabled);
  const list = useQuery({
    queryKey: ["projects", project.id, "backups"],
    queryFn: () => api.backups.list(project.id),
    // Uploads run in the background: follow them until they are through.
    refetchInterval: (q) => (uploading(q.state.data?.offsite) ? 3000 : false),
  });
  const targets = list.data?.offsiteTargets ?? [];
  const enabledTargets = targets.filter((x) => x.enabled);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["projects", project.id, "backups"] });
    void qc.invalidateQueries({ queryKey: keys.project(project.id) });
  };
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: errorText(err, t, fallback) });

  const [withDb, setWithDb] = useState(hasDb);
  const [withFiles, setWithFiles] = useState(true);
  const [withStorage, setWithStorage] = useState(true);
  const [withDeps, setWithDeps] = useState(false);
  const [note, setNote] = useState("");
  const [withOffsite, setWithOffsite] = useState(false);
  const create = useMutation({
    mutationFn: () => api.backups.create(project.id, { database: withDb && hasDb, files: withFiles, storage: withStorage && hasStorage, includeDependencies: withDeps, note, ...(withOffsite && enabledTargets.length > 0 ? { offsite: true } : {}) }),
    onSuccess: (res) => {
      setNote("");
      setMsg(
        res.offsiteError
          ? { tone: "red", text: t("Backup created ({{size}}), but not copied offsite: {{error}}", { size: formatBytes(res.backup.sizeBytes), error: res.offsiteError }) }
          : { tone: "green", text: res.offsite?.length ? t("Backup created ({{size}}); the offsite copy is on its way.", { size: formatBytes(res.backup.sizeBytes) }) : t("Backup created ({{size}}).", { size: formatBytes(res.backup.sizeBytes) }) },
      );
      refresh();
    },
    onError: (err) => fail(err, t("Backup failed")),
  });

  const [restoreTarget, setRestoreTarget] = useState<BackupInfo | null>(null);
  const [rDb, setRDb] = useState(true);
  const [rFiles, setRFiles] = useState(true);
  const [rStorage, setRStorage] = useState(true);
  const [rWipeStorage, setRWipeStorage] = useState(false);
  const [rWipe, setRWipe] = useState(false);
  const [rConfirm, setRConfirm] = useState("");
  const restore = useMutation({
    mutationFn: (b: BackupInfo) =>
      api.backups.restore(project.id, b.id, { database: rDb && hasDumps(b.meta) && hasDb, files: rFiles && !!b.meta.files, storage: rStorage && !!b.meta.storage && hasStorage, wipeFiles: rWipe, wipeStorage: rWipeStorage, confirm: rConfirm }),
    onSuccess: () => {
      setRestoreTarget(null);
      setMsg({ tone: "green", text: t("Backup restored.") });
      refresh();
    },
    onError: (err) => {
      setRestoreTarget(null);
      fail(err, t("Restore failed"));
    },
  });

  const upload = useMutation({
    mutationFn: (b: BackupInfo) => api.backups.uploadOffsite(project.id, b.id),
    onSuccess: refresh,
    onError: (err) => fail(err, t("Copying offsite failed")),
  });
  const [fetching, setFetching] = useState<string | null>(null);
  const fetchRemote = useMutation({
    mutationFn: ({ targetId, key }: { targetId: string; key: string }) => {
      setFetching(key);
      return api.backups.fetchRemote(project.id, targetId, key);
    },
    onSuccess: (r) => {
      setMsg({ tone: "green", text: t("The backup from {{date}} is back in the list above; restore it from there.", { date: formatDateTime(r.backup.createdAt) }) });
      refresh();
      void qc.invalidateQueries({ queryKey: ["projects", project.id, "offsite-remote"] });
    },
    onError: (err) => fail(err, t("Fetching the backup failed")),
    onSettled: () => setFetching(null),
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
      fail(err, t("Delete failed"));
    },
  });

  const openRestore = (b: BackupInfo) => {
    setRDb(hasDumps(b.meta) && hasDb);
    setRFiles(!!b.meta.files);
    setRStorage(!!b.meta.storage && hasStorage);
    setRWipeStorage(false);
    setRWipe(false);
    setRConfirm("");
    setRestoreTarget(b);
  };

  return (
    <div className="space-y-6">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <ScheduleCard project={project} onSaved={refresh} />
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <Archive className="size-4 text-accent-500" aria-hidden /> {t("Create backup")}
            </span>
          }
          description={
            <>
              {t("Stored under")} <Code>/config/backups/{project.slug}/</Code>. {t("The database is dumped inside its container; files and bucket objects are archived by Envoryx.")}
            </>
          }
        />
        <div className="space-y-4 p-5">
          <div className="grid gap-3 sm:grid-cols-3">
            <Checkbox label={t("Database")} description={dbCount > 1 ? t("Logical dumps of all {{number}} databases of the project", { number: dbCount }) : hasDb ? t("Logical dump of the primary database") : t("Project has no database")} checked={withDb && hasDb} disabled={!hasDb} onChange={(e) => setWithDb(e.target.checked)} />
            <Checkbox label={t("Project files")} description={t("Everything in the project directory")} checked={withFiles} onChange={(e) => setWithFiles(e.target.checked)} />
            {hasStorage && <Checkbox label={t("Object storage")} description={t("Every object of the bucket, as plain files in an archive")} checked={withStorage} onChange={(e) => setWithStorage(e.target.checked)} />}
            <Checkbox label={t("Include dependencies")} description={t("Keep vendor/, node_modules/ and framework build caches (.next, .nuxt, .output)")} checked={withDeps} disabled={!withFiles} onChange={(e) => setWithDeps(e.target.checked)} />
            {enabledTargets.length > 0 && (
              <Checkbox label={t("Also copy offsite")} description={enabledTargets.map((x) => x.name).join(", ")} checked={withOffsite} onChange={(e) => setWithOffsite(e.target.checked)} />
            )}
          </div>
          <div className="flex items-end gap-2">
            <Field label={t("Note (optional)")} htmlFor="backup-note">
              <Input id="backup-note" value={note} onChange={(e) => setNote(e.target.value)} placeholder={t("before upgrade to Laravel 13")} maxLength={500} />
            </Field>
            <Button variant="primary" loading={create.isPending} disabled={!(withDb && hasDb) && !withFiles && !(withStorage && hasStorage)} onClick={() => create.mutate()} icon={<Archive className="size-4" />}>
              {t("Create backup")}
            </Button>
          </div>
        </div>
      </Card>

      <Card>
        <CardHeader title={t("Backups")} description={t("Newest first. Restoring overwrites the current database and/or files – Envoryx asks for confirmation.")} />
        {list.isPending ? (
          <Spinner />
        ) : list.isError ? (
          <ErrorState message={errorText(list.error, t)} />
        ) : list.data.backups.length === 0 ? (
          <p className="px-5 py-8 text-center text-sm text-muted">{t("No backups yet.")}</p>
        ) : (
          <ul className="divide-y divide-[var(--border)]">
            {list.data.backups.map((b) => (
              <li key={b.id} className="flex flex-wrap items-center gap-x-6 gap-y-2 px-5 py-3">
                <div className="min-w-[14rem] flex-1">
                  <p className="text-sm font-medium text-fg">
                    {formatDateTime(b.createdAt)}
                    {b.meta.source === "scheduled" && <Badge tone="blue" className="ml-2">{t("scheduled")}</Badge>}
                    {b.meta.source === "upgrade" && <Badge tone="amber" className="ml-2">{t("before upgrade")}</Badge>}
                    {b.meta.note && <span className="ml-2 font-normal text-muted">– {b.meta.note}</span>}
                  </p>
                  <p className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-subtle">
                    {b.meta.database && <Badge tone="amber">{b.meta.database.type} {b.meta.database.version} · {formatBytes(b.meta.database.bytes)}</Badge>}
                    {b.meta.databases?.map((d) => (
                      <Badge key={d.db} tone="amber">
                        {d.db}: {d.type} {d.version} · {formatBytes(d.bytes)}
                      </Badge>
                    ))}
                    {b.meta.files && (
                      <Badge>
                        {t("{{count}} files", { count: b.meta.files.entries })} · {formatBytes(b.meta.files.bytes)}
                        {b.meta.files.includeDependencies ? t(" · with deps") : ""}
                      </Badge>
                    )}
                    {b.meta.storage && (
                      <Badge tone="blue">
                        {t("{{count}} objects", { count: b.meta.storage.objects })} · {formatBytes(b.meta.storage.bytes)}
                      </Badge>
                    )}
                    {b.missing && <Badge tone="red">{t("files missing")}</Badge>}
                    <OffsiteBadges copies={list.data?.offsite?.[b.id]} />
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
                    <Download className="size-3.5" aria-hidden /> {t("Download")}
                  </a>
                  {!b.missing && <OffsiteUploadButton targets={targets} copies={list.data?.offsite?.[b.id]} busy={upload.isPending && upload.variables?.id === b.id} onUpload={() => upload.mutate(b)} />}
                  <Button size="sm" onClick={() => openRestore(b)} disabled={b.missing} icon={<RotateCcw className="size-3.5" />}>
                    {t("Restore")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setDeleteTarget(b)} aria-label={t("Delete backup")}>
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>

      {targets.length > 0 && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <CloudDownload className="size-4 text-accent-500" aria-hidden /> {t("Offsite copies")}
              </span>
            }
            description={t("The backups of this project on the offsite targets – also those no longer here. Fetching one puts it back into the list above, from where it is restored as usual.")}
          />
          <div className="p-5">
            <RemoteBackups
              targets={targets}
              queryKey={["projects", project.id, "offsite-remote"]}
              load={(targetId) => api.backups.remote(project.id, targetId)}
              local={(r) => (list.data?.backups ?? []).some((b) => b.dir === r.id && !b.missing)}
              fetching={fetching}
              onFetch={(targetId, r) => fetchRemote.mutate({ targetId, key: r.key })}
            />
          </div>
        </Card>
      )}

      <Dialog
        open={restoreTarget !== null}
        onClose={() => setRestoreTarget(null)}
        title={t("Restore backup?")}
        description={restoreTarget ? `${t("From {{date}}", { date: formatDateTime(restoreTarget.createdAt) })}${restoreTarget.meta.note ? ` – ${restoreTarget.meta.note}` : ""}` : undefined}
        footer={
          <>
            <Button onClick={() => setRestoreTarget(null)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={restore.isPending} disabled={rConfirm !== project.slug || (!rDb && !rFiles && !rStorage)} onClick={() => restoreTarget && restore.mutate(restoreTarget)} icon={<RotateCcw className="size-4" />}>
              {t("Restore")}
            </Button>
          </>
        }
      >
        {restoreTarget && (
          <div className="space-y-4">
            <Alert tone="red" title={t("This overwrites current data")}>
              {rDb && restoreTarget.meta.database && <p>{t("The database “{{name}}” is replaced by the dump; changes since the backup are lost.", { name: restoreTarget.meta.database.name })}</p>}
              {rDb && restoreTarget.meta.databases?.map((d) => <p key={d.db}>{t("The database “{{name}}” is replaced by the dump; changes since the backup are lost.", { name: d.db })}</p>)}
              {rFiles && restoreTarget.meta.files && <p>{rWipe ? t("Files in the archive overwrite the project directory; everything else in the directory is deleted first.") : t("Files in the archive overwrite the project directory; files not in the backup are kept.")}</p>}
              {rStorage && restoreTarget.meta.storage && <p>{rWipeStorage ? t("Objects in the archive are uploaded into the bucket; everything else in the bucket is deleted first.") : t("Objects in the archive are uploaded into the bucket; objects not in the backup are kept.")}</p>}
            </Alert>
            <Checkbox label={t("Restore database")} checked={rDb} disabled={!hasDumps(restoreTarget.meta) || !hasDb} onChange={(e) => setRDb(e.target.checked)} description={!hasDumps(restoreTarget.meta) ? t("not in this backup") : !hasDb ? t("project has no database") : undefined} />
            <Checkbox label={t("Restore files")} checked={rFiles} disabled={!restoreTarget.meta.files} onChange={(e) => setRFiles(e.target.checked)} description={!restoreTarget.meta.files ? t("not in this backup") : undefined} />
            {rFiles && <Checkbox label={t("Empty the project directory first")} description={t("Makes the directory match the backup exactly (also removes vendor/, node_modules/ and build caches if they were not included).")} checked={rWipe} onChange={(e) => setRWipe(e.target.checked)} />}
            <Checkbox label={t("Restore object storage")} checked={rStorage} disabled={!restoreTarget.meta.storage || !hasStorage} onChange={(e) => setRStorage(e.target.checked)} description={!restoreTarget.meta.storage ? t("not in this backup") : !hasStorage ? t("project has no object storage") : undefined} />
            {rStorage && <Checkbox label={t("Empty the bucket first")} description={t("Makes the bucket match the backup exactly.")} checked={rWipeStorage} onChange={(e) => setRWipeStorage(e.target.checked)} />}
            <Field label={t("Type {{slug}} to confirm", { slug: project.slug })} htmlFor="restore-confirm">
              <Input id="restore-confirm" value={rConfirm} onChange={(e) => setRConfirm(e.target.value)} autoComplete="off" />
            </Field>
          </div>
        )}
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title={t("Delete backup?")}
        description={deleteTarget ? `${formatDateTime(deleteTarget.createdAt)} · ${formatBytes(deleteTarget.sizeBytes)}` : undefined}
        footer={
          <>
            <Button onClick={() => setDeleteTarget(null)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => deleteTarget && remove.mutate(deleteTarget)} icon={<Trash2 className="size-4" />}>
              {t("Delete")}
            </Button>
          </>
        }
      />
    </div>
  );
}

const weekdays = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

function ScheduleCard({ project, onSaved }: { project: Project; onSaved: () => void }) {
  const { t } = useTranslation();
  const current = project.backupSchedule;
  const [form, setForm] = useState<Omit<BackupSchedule, "lastRun">>({ schedule: current.schedule, hour: current.hour, weekday: current.weekday, keep: current.keep, includeDependencies: current.includeDependencies });
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  useEffect(() => {
    setForm({ schedule: current.schedule, hour: current.hour, weekday: current.weekday, keep: current.keep, includeDependencies: current.includeDependencies });
  }, [current.schedule, current.hour, current.weekday, current.keep, current.includeDependencies]);
  const save = useMutation({
    mutationFn: () => api.backups.setSchedule(project.id, form),
    onSuccess: () => {
      setMsg({ tone: "green", text: form.schedule ? t("Schedule saved.") : t("Scheduled backups disabled.") });
      onSaved();
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }));
  const dirty = JSON.stringify(form) !== JSON.stringify({ schedule: current.schedule, hour: current.hour, weekday: current.weekday, keep: current.keep, includeDependencies: current.includeDependencies });

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <CalendarClock className="size-4 text-accent-500" aria-hidden /> {t("Scheduled backups")}
          </span>
        }
        description={current.lastRun ? t("Last automatic backup {{date}}.", { date: formatDateTime(current.lastRun) }) : t("Automatic database, file and object storage backups; the oldest scheduled backups are removed beyond the keep count. Manual backups are never touched.")}
        actions={
          <Button variant="primary" size="sm" loading={save.isPending} disabled={!dirty || (!!form.schedule && form.keep < 1)} onClick={() => { setMsg(null); save.mutate(); }}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <div className="grid gap-4 sm:grid-cols-4">
          <Field label={t("Frequency")} htmlFor="sched-freq">
            <Select id="sched-freq" value={form.schedule} onChange={(e) => set({ schedule: e.target.value as BackupSchedule["schedule"] })}>
              <option value="">{t("Off")}</option>
              <option value="daily">{t("Daily")}</option>
              <option value="weekly">{t("Weekly")}</option>
            </Select>
          </Field>
          {form.schedule === "weekly" && (
            <Field label={t("Weekday")} htmlFor="sched-day">
              <Select id="sched-day" value={form.weekday} onChange={(e) => set({ weekday: Number(e.target.value) })}>
                {weekdays.map((d, i) => (
                  <option key={d} value={i}>{t(d)}</option>
                ))}
              </Select>
            </Field>
          )}
          {form.schedule && (
            <>
              <Field label={t("Time")} htmlFor="sched-hour" hint={t("Server local time")}>
                <Select id="sched-hour" value={form.hour} onChange={(e) => set({ hour: Number(e.target.value) })}>
                  {Array.from({ length: 24 }, (_, h) => (
                    <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>
                  ))}
                </Select>
              </Field>
              <Field label={t("Keep")} htmlFor="sched-keep" hint={t("Number of scheduled backups")}>
                <Input id="sched-keep" type="number" min={1} max={365} value={form.keep} onChange={(e) => set({ keep: Number(e.target.value) })} />
              </Field>
            </>
          )}
        </div>
        {form.schedule && <Checkbox label={t("Include vendor/, node_modules/ and framework build caches")} description={t("Larger archives; usually not needed since dependencies can be reinstalled.")} checked={form.includeDependencies} onChange={(e) => set({ includeDependencies: e.target.checked })} />}
      </div>
    </Card>
  );
}
