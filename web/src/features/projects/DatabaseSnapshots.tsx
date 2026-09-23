import { Camera, Copy, Download, History, RotateCcw, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useProjects, useSnapshotMutations, useSnapshots } from "@/api/hooks";
import type { BackupInfo, DatabaseInfo, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Dialog, ErrorState, Field, Input, Select, Spinner } from "@/components/ui";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";

type Message = { tone: "green" | "red"; text: string };

/** How many snapshots the server keeps per project (`snapshotKeep` in internal/project). */
const SNAPSHOT_KEEP = 10;

/** Where a snapshot came from: taken by hand, by a clone or by a version upgrade. */
function sourceBadge(snapshot: BackupInfo, t: (key: string) => string) {
  if (snapshot.meta.source === "upgrade") return <Badge tone="amber">{t("before upgrade")}</Badge>;
  if (snapshot.meta.source === "scheduled") return <Badge tone="blue">{t("scheduled")}</Badge>;
  return null;
}

/**
 * Snapshots of the database alone: the dump to take before a migration and to put back
 * when it went wrong. They are ordinary backups, so the Backups tab lists them too.
 */
export function SnapshotsCard({ project, database, onMessage }: { project: Project; database: DatabaseInfo; onMessage: (m: Message) => void }) {
  const { t } = useTranslation();
  const snapshots = useSnapshots(project.id, true);
  const { create, restore, remove } = useSnapshotMutations(project.id);
  const [note, setNote] = useState("");
  const [restoreTarget, setRestoreTarget] = useState<BackupInfo | null>(null);
  const [confirm, setConfirm] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<BackupInfo | null>(null);
  const fail = (err: unknown, fallback: string) => onMessage({ tone: "red", text: errorText(err, t, fallback) });

  return (
    <>
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <History className="size-4 text-accent-500" aria-hidden /> {t("Snapshots")}
            </span>
          }
          description={t("A dump of the database “{{name}}” and nothing else – taken before a migration or a mass update, put back with one click. The project does not have to be running for it, and only the {{keep}} newest snapshots are kept.", {
            name: database.database,
            keep: SNAPSHOT_KEEP,
          })}
        />
        <div className="space-y-4 p-5">
          <form
            className="flex items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate(note.trim(), {
                onSuccess: (s) => {
                  setNote("");
                  onMessage({ tone: "green", text: t("Snapshot taken ({{size}}).", { size: formatBytes(s.sizeBytes) }) });
                },
                onError: (err) => fail(err, t("Taking the snapshot failed")),
              });
            }}
          >
            <Field label={t("Note (optional)")} htmlFor="snapshot-note">
              <Input id="snapshot-note" value={note} onChange={(e) => setNote(e.target.value)} placeholder={t("before the orders migration")} maxLength={500} />
            </Field>
            <Button type="submit" variant="primary" loading={create.isPending} icon={<Camera className="size-4" />}>
              {t("Take snapshot")}
            </Button>
          </form>

          {snapshots.isPending ? (
            <Spinner />
          ) : snapshots.isError ? (
            <ErrorState message={errorText(snapshots.error, t)} />
          ) : snapshots.data.length === 0 ? (
            <p className="py-4 text-center text-sm text-muted">{t("No snapshots yet.")}</p>
          ) : (
            <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
              {snapshots.data.map((s) => (
                <li key={s.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2">
                  <div className="min-w-[12rem] flex-1">
                    <p className="text-sm font-medium text-fg">
                      {formatDateTime(s.createdAt)}
                      <span className="ml-2">{sourceBadge(s, t)}</span>
                      {s.meta.note && <span className="ml-2 font-normal text-muted">– {s.meta.note}</span>}
                    </p>
                    <p className="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-subtle">
                      {s.meta.database && <Badge tone="amber">{s.meta.database.type} {s.meta.database.version} · {s.meta.database.name}</Badge>}
                      {s.missing && <Badge tone="red">{t("dump missing")}</Badge>}
                    </p>
                  </div>
                  <span className="text-xs tabular-nums text-muted">{formatBytes(s.sizeBytes)}</span>
                  <div className="flex items-center gap-1.5">
                    <a
                      href={`/api/v1/projects/${encodeURIComponent(project.id)}/backups/${encodeURIComponent(s.id)}/download`}
                      className="inline-flex h-8 items-center gap-1.5 rounded-md border border-default bg-elevated px-2.5 text-xs font-medium text-fg hover:bg-muted"
                      download
                    >
                      <Download className="size-3.5" aria-hidden /> {t("Download")}
                    </a>
                    <Button
                      size="sm"
                      disabled={s.missing}
                      icon={<RotateCcw className="size-3.5" />}
                      onClick={() => {
                        setConfirm("");
                        setRestoreTarget(s);
                      }}
                    >
                      {t("Restore")}
                    </Button>
                    <Button variant="ghost" size="sm" aria-label={t("Delete snapshot")} onClick={() => setDeleteTarget(s)}>
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </Card>

      <Dialog
        open={restoreTarget !== null}
        onClose={() => setRestoreTarget(null)}
        title={t("Restore this snapshot?")}
        description={restoreTarget ? `${formatDateTime(restoreTarget.createdAt)}${restoreTarget.meta.note ? ` – ${restoreTarget.meta.note}` : ""}` : undefined}
        footer={
          <>
            <Button onClick={() => setRestoreTarget(null)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              loading={restore.isPending}
              disabled={confirm !== project.slug}
              icon={<RotateCcw className="size-4" />}
              onClick={() =>
                restoreTarget &&
                restore.mutate(
                  { snapshotId: restoreTarget.id, confirm },
                  {
                    onSuccess: () => {
                      setRestoreTarget(null);
                      onMessage({ tone: "green", text: t("Snapshot restored.") });
                    },
                    onError: (err) => {
                      setRestoreTarget(null);
                      fail(err, t("Restoring the snapshot failed"));
                    },
                  },
                )
              }
            >
              {t("Restore")}
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Alert tone="red" title={t("This overwrites current data")}>
            {t("The database “{{name}}” is replaced by the dump; everything written since the snapshot is lost.", { name: database.database })}
          </Alert>
          <Field label={t("Type {{slug}} to confirm", { slug: project.slug })} htmlFor="snapshot-restore-confirm">
            <Input id="snapshot-restore-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
          </Field>
        </div>
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title={t("Delete snapshot?")}
        description={deleteTarget ? `${formatDateTime(deleteTarget.createdAt)} · ${formatBytes(deleteTarget.sizeBytes)}` : undefined}
        footer={
          <>
            <Button onClick={() => setDeleteTarget(null)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              loading={remove.isPending}
              icon={<Trash2 className="size-4" />}
              onClick={() =>
                deleteTarget &&
                remove.mutate(deleteTarget.id, {
                  onSuccess: () => setDeleteTarget(null),
                  onError: (err) => {
                    setDeleteTarget(null);
                    fail(err, t("Deleting the snapshot failed"));
                  },
                })
              }
            >
              {t("Delete")}
            </Button>
          </>
        }
      />
    </>
  );
}

/** Replace this project's database contents with another project's – staging into local. */
export function CloneDatabaseCard({ project, database, onMessage }: { project: Project; database: DatabaseInfo; onMessage: (m: Message) => void }) {
  const { t } = useTranslation();
  const projects = useProjects();
  const { clone } = useSnapshotMutations(project.id);
  const [source, setSource] = useState("");
  const [snapshot, setSnapshot] = useState(true);
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState("");

  // Only projects that run the same engine can hand their dump over.
  const candidates = (projects.data ?? []).filter(
    (p) => p.id !== project.id && p.lifecycle === "ready" && p.services.some((s) => s.kind === "database" && s.enabled && s.variant === database.type),
  );
  const selected = candidates.find((p) => p.id === source) ?? candidates[0];

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Copy className="size-4 text-accent-500" aria-hidden /> {t("Clone from another project")}
          </span>
        }
        description={t("Copies the contents of another project's database into “{{name}}”. The dump is piped straight over, nothing is written to disk in between, and the other project is only read.", { name: database.database })}
      />
      <div className="space-y-4 p-5">
        {candidates.length === 0 ? (
          <p className="text-sm text-muted">{t("No other project runs {{engine}}, so there is nothing to clone from.", { engine: database.type })}</p>
        ) : (
          <>
            <div className="flex items-end gap-2">
              <Field label={t("Source project")} htmlFor="clone-source" hint={t("Projects with a {{engine}} database", { engine: database.type })}>
                <Select id="clone-source" value={selected?.id ?? ""} onChange={(e) => setSource(e.target.value)}>
                  {candidates.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name} ({p.slug})
                    </option>
                  ))}
                </Select>
              </Field>
              <Button
                variant="primary"
                icon={<Copy className="size-4" />}
                disabled={!selected}
                onClick={() => {
                  setConfirm("");
                  setOpen(true);
                }}
              >
                {t("Clone database")}
              </Button>
            </div>
            <Checkbox
              label={t("Take a snapshot of this project's database first")}
              description={t("The way back: the snapshot holds what the clone overwrites.")}
              checked={snapshot}
              onChange={(e) => setSnapshot(e.target.checked)}
            />
          </>
        )}
      </div>

      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        title={t("Clone the database of {{source}}?", { source: selected?.slug ?? "" })}
        footer={
          <>
            <Button onClick={() => setOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              loading={clone.isPending}
              disabled={confirm !== project.slug || !selected}
              icon={<Copy className="size-4" />}
              onClick={() =>
                selected &&
                clone.mutate(
                  { source: selected.id, snapshot, confirm },
                  {
                    onSuccess: (res) => {
                      setOpen(false);
                      onMessage({
                        tone: "green",
                        text: res.snapshot
                          ? t("Database of {{source}} cloned into “{{name}}”. The state before it is the newest snapshot.", { source: res.source, name: res.database })
                          : t("Database of {{source}} cloned into “{{name}}”.", { source: res.source, name: res.database }),
                      });
                    },
                    onError: (err) => {
                      setOpen(false);
                      onMessage({ tone: "red", text: errorText(err, t, t("Cloning the database failed")) });
                    },
                  },
                )
              }
            >
              {t("Clone database")}
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Alert tone="red" title={t("This overwrites current data")}>
            {t("The database “{{name}}” is replaced by the contents of {{source}}; everything in it now is lost.", { name: database.database, source: selected?.slug ?? "" })}
            {snapshot ? ` ${t("A snapshot is taken first, so it can be put back.")}` : ` ${t("No snapshot is taken – there is no way back.")}`}
          </Alert>
          <Field label={t("Type {{slug}} to confirm", { slug: project.slug })} htmlFor="clone-confirm">
            <Input id="clone-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
          </Field>
        </div>
      </Dialog>
    </Card>
  );
}
