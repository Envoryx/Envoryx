import { Check, CloudUpload, KeyRound, Pencil, Plus, Trash2, Wifi } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { OffsiteTarget, OffsiteTargetInput, OffsiteType } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Dialog, Field, Input, Select, Spinner } from "@/components/ui";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";

export const offsiteKey = ["offsite"] as const;

const typeLabel: Record<OffsiteType, string> = { s3: "S3", sftp: "SFTP", webdav: "WebDAV" };

function blank(type: OffsiteType): OffsiteTargetInput {
  const input: OffsiteTargetInput = {
    name: "",
    type,
    enabled: true,
    auto: true,
    instance: true,
    instanceHour: 3,
    prefix: "envoryx",
    keep: 14,
    instanceKeep: 14,
    encrypt: true,
    endpoint: "",
    region: "",
    bucket: "",
    accessKey: "",
    host: "",
    user: "",
    hostKey: "",
    url: "",
  };
  if (type === "sftp") input.port = 22;
  return input;
}

function fromTarget(t: OffsiteTarget): OffsiteTargetInput {
  const { secrets, location, last, ...rest } = t;
  return { ...rest };
}

/** Settings → Backups: where offsite copies go. */
export function OffsiteTargetsCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: offsiteKey, queryFn: api.offsite.list });
  const [editing, setEditing] = useState<{ input: OffsiteTargetInput; stored?: OffsiteTarget } | null>(null);
  const [deleting, setDeleting] = useState<OffsiteTarget | null>(null);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: offsiteKey });
    void qc.invalidateQueries({ queryKey: ["instance-backups"] });
  };
  const remove = useMutation({
    mutationFn: (x: OffsiteTarget) => api.offsite.remove(x.id),
    onSuccess: () => {
      setDeleting(null);
      refresh();
    },
    onError: (err) => {
      setDeleting(null);
      setMsg({ tone: "red", text: errorText(err, t, t("Delete failed")) });
    },
  });

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <CloudUpload className="size-4 text-accent-500" aria-hidden /> {t("Offsite backups")}
          </span>
        }
        description={t("Copies of the backups outside this host: an S3-compatible bucket (AWS, Backblaze B2, Wasabi, Hetzner, Cloudflare R2, MinIO), an SFTP server (Hetzner Storage Box, NAS) or a WebDAV share (Nextcloud). The local backups stay; the target keeps its own copies.")}
        actions={
          <Button size="sm" variant="primary" onClick={() => setEditing({ input: blank("s3") })} icon={<Plus className="size-3.5" />}>
            {t("Add target")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <Alert tone="red">{errorText(q.error, t)}</Alert>
        ) : q.data.targets.length === 0 ? (
          <p className="text-sm text-muted">{t("No offsite target yet. Until there is one, backups exist only on this host.")}</p>
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {q.data.targets.map((x) => (
              <li key={x.id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2.5 text-sm">
                <div className="min-w-0 space-y-1">
                  <p className="flex flex-wrap items-center gap-2 font-medium">
                    {x.name}
                    <Badge>{typeLabel[x.type]}</Badge>
                    {!x.enabled && <Badge tone="gray">{t("off")}</Badge>}
                    {x.enabled && x.auto && <Badge tone="blue">{t("scheduled project backups")}</Badge>}
                    {x.enabled && x.instance && <Badge tone="blue">{t("instance daily {{hour}}:00", { hour: String(x.instanceHour).padStart(2, "0") })}</Badge>}
                    {x.encrypt ? <Badge tone="green">{t("encrypted")}</Badge> : <Badge tone="amber">{t("not encrypted")}</Badge>}
                  </p>
                  <p className="truncate font-mono text-[11px] text-subtle">{x.location}</p>
                  {x.last && (
                    <p className={x.last.status === "failed" ? "text-xs text-red-600 dark:text-red-400" : "text-xs text-muted"}>
                      {x.last.status === "failed"
                        ? t("Last upload failed {{date}}: {{error}}", { date: formatDateTime(x.last.updatedAt), error: x.last.error ?? "" })
                        : t("Last upload {{date}} ({{size}})", { date: formatDateTime(x.last.updatedAt), size: formatBytes(x.last.sizeBytes) })}
                    </p>
                  )}
                </div>
                <div className="flex items-center gap-1">
                  <Button size="sm" variant="ghost" onClick={() => setEditing({ input: fromTarget(x), stored: x })} icon={<Pencil className="size-3.5" />}>
                    {t("Edit")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setDeleting(x)} aria-label={t("Delete {{name}}", { name: x.name })}>
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <p className="text-xs text-subtle">
          {t("Scheduled project backups go up by themselves when a target says so; other backups have a “Copy offsite” button. A failed upload is retried four times with growing pauses and reported through the notifications (event “Backup failed”).")}
        </p>
      </div>

      {editing && (
        <TargetDialog
          key={editing.stored?.id ?? "new"}
          initial={editing.input}
          stored={editing.stored}
          onClose={() => setEditing(null)}
          onSaved={(saved) => {
            setEditing(null);
            setMsg({ tone: "green", text: t("{{name}} saved.", { name: saved.name }) });
            refresh();
          }}
        />
      )}

      <Dialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title={t("Remove target?")}
        description={deleting?.location}
        footer={
          <>
            <Button onClick={() => setDeleting(null)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={remove.isPending} onClick={() => deleting && remove.mutate(deleting)} icon={<Trash2 className="size-4" />}>
              {t("Remove")}
            </Button>
          </>
        }
      >
        <p className="text-sm text-muted">{t("Envoryx stops copying to it. The copies already there stay where they are.")}</p>
      </Dialog>
    </Card>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <fieldset className="space-y-3 rounded-md border border-default p-3">
      <legend className="px-1 text-xs font-medium text-muted">{title}</legend>
      {children}
    </fieldset>
  );
}

function TargetDialog({ initial, stored, onClose, onSaved }: { initial: OffsiteTargetInput; stored?: OffsiteTarget | undefined; onClose: () => void; onSaved: (t: OffsiteTarget) => void }) {
  const { t } = useTranslation();
  const [form, setForm] = useState<OffsiteTargetInput>(initial);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const set = (patch: Partial<OffsiteTargetInput>) => {
    setMsg(null);
    setForm((f) => ({ ...f, ...patch }));
  };
  const has = stored?.secrets;
  const secretHint = (present: boolean | undefined) => (present ? t("Stored. Leave empty to keep it.") : undefined);

  const save = useMutation({
    mutationFn: () => (form.id ? api.offsite.update(form.id, form) : api.offsite.create(form)),
    onSuccess: (r) => onSaved(r.target),
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  const test = useMutation({
    mutationFn: () => api.offsite.test(form),
    onSuccess: (r) => {
      if (r.result.hostKey && !form.hostKey) set({ hostKey: r.result.hostKey });
      setMsg({ tone: "green", text: r.result.hostKey ? t("Connection works: written, listed, read and deleted a test file. Server key {{key}}.", { key: r.result.hostKey }) : t("Connection works: written, listed, read and deleted a test file.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("The test failed")) }),
  });

  return (
    <Dialog
      open
      onClose={onClose}
      title={form.id ? t("Edit {{name}}", { name: initial.name }) : t("Add offsite target")}
      footer={
        <>
          <Button onClick={onClose}>{t("Cancel")}</Button>
          <Button loading={test.isPending} disabled={save.isPending} onClick={() => test.mutate()} icon={<Wifi className="size-4" />}>
            {t("Test")}
          </Button>
          <Button variant="primary" loading={save.isPending} disabled={test.isPending} onClick={() => save.mutate()} icon={<Check className="size-4" />}>
            {t("Save")}
          </Button>
        </>
      }
    >
      <div className="max-h-[65vh] space-y-4 overflow-y-auto pr-1">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("Name")} htmlFor="ot-name">
            <Input id="ot-name" value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="Backblaze B2" maxLength={64} />
          </Field>
          <Field label={t("Type")} htmlFor="ot-type">
            <Select id="ot-type" value={form.type} disabled={!!form.id} onChange={(e) => set(e.target.value === "sftp" ? { type: "sftp", port: 22 } : { type: e.target.value as OffsiteType })}>
              <option value="s3">{t("S3-compatible (AWS, B2, Wasabi, Hetzner, R2, MinIO)")}</option>
              <option value="sftp">{t("SFTP (Storage Box, NAS, SSH server)")}</option>
              <option value="webdav">{t("WebDAV (Nextcloud, ownCloud, Storage Box)")}</option>
            </Select>
          </Field>
        </div>

        {form.type === "s3" && (
          <Section title={t("Bucket")}>
            <Field label={t("Endpoint")} htmlFor="ot-endpoint" hint={t("e.g. https://s3.eu-central-003.backblazeb2.com, https://fsn1.your-objectstorage.com, https://s3.eu-central-1.amazonaws.com")}>
              <Input id="ot-endpoint" value={form.endpoint ?? ""} onChange={(e) => set({ endpoint: e.target.value })} placeholder="https://" spellCheck={false} />
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("Bucket")} htmlFor="ot-bucket">
                <Input id="ot-bucket" value={form.bucket ?? ""} onChange={(e) => set({ bucket: e.target.value })} spellCheck={false} />
              </Field>
              <Field label={t("Region")} htmlFor="ot-region" hint={t("Empty = us-east-1")}>
                <Input id="ot-region" value={form.region ?? ""} onChange={(e) => set({ region: e.target.value })} placeholder="eu-central-003" spellCheck={false} />
              </Field>
              <Field label={t("Access key")} htmlFor="ot-access">
                <Input id="ot-access" value={form.accessKey ?? ""} onChange={(e) => set({ accessKey: e.target.value })} autoComplete="off" spellCheck={false} />
              </Field>
              <Field label={t("Secret key")} htmlFor="ot-secret" hint={secretHint(has?.secretKey)}>
                <Input id="ot-secret" type="password" value={form.secretKey ?? ""} onChange={(e) => set({ secretKey: e.target.value })} autoComplete="new-password" placeholder={has?.secretKey ? "••••••••" : ""} />
              </Field>
            </div>
          </Section>
        )}

        {form.type === "sftp" && (
          <Section title={t("Server")}>
            <div className="grid gap-3 sm:grid-cols-[1fr_6rem]">
              <Field label={t("Host")} htmlFor="ot-host">
                <Input id="ot-host" value={form.host ?? ""} onChange={(e) => set({ host: e.target.value })} placeholder="u12345.your-storagebox.de" spellCheck={false} />
              </Field>
              <Field label={t("Port")} htmlFor="ot-port" hint={t("Storage Box: 23")}>
                <Input id="ot-port" type="number" min={1} max={65535} value={form.port ?? 22} onChange={(e) => set({ port: Number(e.target.value) })} />
              </Field>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("User")} htmlFor="ot-user">
                <Input id="ot-user" value={form.user ?? ""} onChange={(e) => set({ user: e.target.value })} autoComplete="off" spellCheck={false} />
              </Field>
              <Field label={t("Password")} htmlFor="ot-password" hint={secretHint(has?.password)}>
                <Input id="ot-password" type="password" value={form.password ?? ""} onChange={(e) => set({ password: e.target.value })} autoComplete="new-password" placeholder={has?.password ? "••••••••" : ""} />
              </Field>
            </div>
            <Field label={t("Private key (instead of or next to the password)")} htmlFor="ot-key" hint={has?.privateKey ? t("Stored. Leave empty to keep it.") : t("OpenSSH format, without passphrase.")}>
              <textarea
                id="ot-key"
                value={form.privateKey ?? ""}
                onChange={(e) => set({ privateKey: e.target.value })}
                rows={3}
                spellCheck={false}
                placeholder={has?.privateKey ? "••••••••" : "-----BEGIN OPENSSH PRIVATE KEY-----"}
                className="w-full rounded-md border border-default bg-elevated px-3 py-2 font-mono text-xs text-fg"
              />
            </Field>
            <Field label={t("Server key")} htmlFor="ot-hostkey" hint={t("Recorded on the first connection (Test does it); a changed key stops the uploads. Clear it after replacing the server on purpose.")}>
              <div className="flex gap-2">
                <Input id="ot-hostkey" value={form.hostKey ?? ""} readOnly placeholder={t("not recorded yet")} className="font-mono text-xs" />
                {form.hostKey && (
                  <Button size="sm" onClick={() => set({ hostKey: "" })} icon={<KeyRound className="size-3.5" />}>
                    {t("Clear")}
                  </Button>
                )}
              </div>
            </Field>
          </Section>
        )}

        {form.type === "webdav" && (
          <Section title={t("Share")}>
            <Field label={t("WebDAV address")} htmlFor="ot-url" hint={t("Nextcloud: https://cloud.example.com/remote.php/dav/files/<user>")}>
              <Input id="ot-url" value={form.url ?? ""} onChange={(e) => set({ url: e.target.value })} placeholder="https://" spellCheck={false} />
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("User")} htmlFor="ot-dav-user">
                <Input id="ot-dav-user" value={form.user ?? ""} onChange={(e) => set({ user: e.target.value })} autoComplete="off" spellCheck={false} />
              </Field>
              <Field label={t("Password")} htmlFor="ot-dav-password" hint={has?.password ? t("Stored. Leave empty to keep it.") : t("Nextcloud: an app password")}>
                <Input id="ot-dav-password" type="password" value={form.password ?? ""} onChange={(e) => set({ password: e.target.value })} autoComplete="new-password" placeholder={has?.password ? "••••••••" : ""} />
              </Field>
            </div>
          </Section>
        )}

        <Field label={t("Folder")} htmlFor="ot-prefix" hint={t("Inside the bucket or share; several Envoryx instances can share a target with different folders.")}>
          <Input id="ot-prefix" value={form.prefix} onChange={(e) => set({ prefix: e.target.value })} placeholder="envoryx" spellCheck={false} />
        </Field>

        <Section title={t("Encryption")}>
          <Checkbox label={t("Encrypt before uploading")} description={t("Backups contain passwords, tokens and your data. With encryption the target only ever sees age-encrypted files.")} checked={form.encrypt} onChange={(e) => set({ encrypt: e.target.checked })} />
          {form.encrypt && (
            <>
              <Field label={t("Passphrase")} htmlFor="ot-passphrase" hint={has?.passphrase ? t("Stored. Leave empty to keep it.") : t("At least 12 characters.")}>
                <Input id="ot-passphrase" type="password" value={form.passphrase ?? ""} onChange={(e) => set({ passphrase: e.target.value })} autoComplete="new-password" placeholder={has?.passphrase ? "••••••••" : ""} />
              </Field>
              <Alert tone="amber">{t("Write the passphrase down somewhere else. Without it no copy can be restored – after losing this host, a fresh Envoryx needs it to read its own backups. The files open with the age tool too: age -d backup.tar.age > backup.tar")}</Alert>
            </>
          )}
        </Section>

        <Section title={t("What goes up")}>
          <Checkbox label={t("Enabled")} checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          <Checkbox label={t("Scheduled project backups")} description={t("Every scheduled backup of every project is copied once it is taken. Other backups on request.")} checked={form.auto} onChange={(e) => set({ auto: e.target.checked })} />
          {form.auto && (
            <Field label={t("Keep per project")} htmlFor="ot-keep" hint={t("Scheduled copies per project on the target, 0 = all. Copies made on request are never removed.")}>
              <Input id="ot-keep" type="number" min={0} max={1000} value={form.keep} onChange={(e) => set({ keep: Number(e.target.value) })} />
            </Field>
          )}
          <Checkbox label={t("Daily instance backup")} description={t("Database, settings, CA and keys of Envoryx itself – what a fresh Envoryx needs to come back after this host is lost.")} checked={form.instance} onChange={(e) => set({ instance: e.target.checked })} />
          {form.instance && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("Time")} htmlFor="ot-hour" hint={t("Server local time")}>
                <Select id="ot-hour" value={form.instanceHour} onChange={(e) => set({ instanceHour: Number(e.target.value) })}>
                  {Array.from({ length: 24 }, (_, h) => (
                    <option key={h} value={h}>
                      {String(h).padStart(2, "0")}:00
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label={t("Keep")} htmlFor="ot-instance-keep" hint={t("Daily copies on the target, 0 = all")}>
                <Input id="ot-instance-keep" type="number" min={0} max={1000} value={form.instanceKeep} onChange={(e) => set({ instanceKeep: Number(e.target.value) })} />
              </Field>
            </div>
          )}
        </Section>
        {stored && <p className="text-xs text-subtle">{t("Location")}: <Code>{stored.location}</Code></p>}
      </div>
    </Dialog>
  );
}
