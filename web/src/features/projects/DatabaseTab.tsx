import { Check, Copy, Database, Eye, EyeOff, KeyRound, Plus, Trash2, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import { useDatabaseInfo, useDatabaseList, useDatabaseMutations, usePublicHost, useRuntimes, useUpdateProject } from "@/api/hooks";
import type { DatabaseCredentials, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Dialog, ErrorState, Field, Input, Select, Spinner, StatusDot } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { containerStateTone } from "@/lib/format";

export function CopyButton({ value, label }: { value: string; label: string }) {
  const { t } = useTranslation();
  const [state, setState] = useState<"idle" | "done" | "failed">("idle");
  return (
    <Button
      variant="ghost"
      size="sm"
      aria-label={t("Copy {{label}}", { label })}
      title={state === "failed" ? t("Copying failed – select the value and copy it manually") : t("Copy {{label}}", { label })}
      onClick={async () => {
        const ok = await copyText(value);
        setState(ok ? "done" : "failed");
        setTimeout(() => setState("idle"), 2000);
      }}
    >
      {state === "done" ? <Check className="size-4 text-emerald-500" /> : state === "failed" ? <X className="size-4 text-red-500" /> : <Copy className="size-4" />}
    </Button>
  );
}

export function CopyRow({ label, value, secret = false, mono = true }: { label: string; value: string; secret?: boolean; mono?: boolean }) {
  const { t } = useTranslation();
  const [show, setShow] = useState(false);
  const display = secret && !show ? "•".repeat(Math.min(value.length, 24)) : value;
  return (
    <div className="flex items-center justify-between gap-3 py-1.5">
      <dt className="w-36 shrink-0 text-sm text-muted">{label}</dt>
      <dd className={`min-w-0 flex-1 truncate text-sm select-all ${mono ? "font-mono text-xs" : ""}`} title={secret && !show ? undefined : value}>
        {display}
      </dd>
      <div className="flex shrink-0 items-center">
        {secret && (
          <Button variant="ghost" size="sm" onClick={() => setShow(!show)} aria-label={show ? t("Hide {{label}}", { label }) : t("Show {{label}}", { label })}>
            {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
          </Button>
        )}
        <CopyButton value={value} label={label} />
      </div>
    </div>
  );
}

function AddDatabaseCard({ project }: { project: Project }) {
  const { t } = useTranslation();
  const runtimes = useRuntimes();
  const update = useUpdateProject(project.id);
  const [type, setType] = useState("mariadb");
  const [version, setVersion] = useState("");
  const [expose, setExpose] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const dbs = runtimes.data?.runtimes.filter((r) => r.kind === "database" && r.available) ?? [];
  const selected = dbs.find((d) => d.key === type);

  return (
    <Card>
      <CardHeader title={t("Database")} description={t("This project has no database yet. Adding one creates a container with a persistent volume and injects the connection variables into PHP.")} />
      <div className="space-y-4 p-5">
        {error && <Alert tone="red">{error}</Alert>}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("Type")} htmlFor="add-db-type">
            <Select id="add-db-type" value={type} onChange={(e) => { setType(e.target.value); setVersion(""); }}>
              {dbs.map((d) => (
                <option key={d.key} value={d.key}>
                  {d.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("Version")} htmlFor="add-db-version">
            <Select id="add-db-version" value={version || selected?.versions.find((v) => v.default)?.version || ""} onChange={(e) => setVersion(e.target.value)}>
              {selected?.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <Checkbox label={t("Publish database port on the host")} description={t("For external clients such as TablePlus or DBeaver.")} checked={expose} onChange={(e) => setExpose(e.target.checked)} />
        <Button
          variant="primary"
          icon={<Plus className="size-4" />}
          loading={update.isPending}
          disabled={!selected}
          onClick={() => {
            setError(null);
            update.mutate(
              { database: { enabled: true, type, version: version || selected?.versions.find((v) => v.default)?.version || "", exposePort: expose } },
              { onError: (err) => setError(err instanceof ApiError ? err.message : t("Adding the database failed")) },
            );
          }}
        >
          {t("Add database")}
        </Button>
      </div>
    </Card>
  );
}

export function DatabaseTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const hasDb = project.services.some((s) => s.kind === "database" && s.enabled);
  const info = useDatabaseInfo(project.id, hasDb);
  const publicHost = usePublicHost();
  const runtimes = useRuntimes();
  const update = useUpdateProject(project.id);
  const { rotate, expose, create, drop } = useDatabaseMutations(project.id);
  const running = info.data?.state === "running";
  const list = useDatabaseList(project.id, hasDb && running);

  const [creds, setCreds] = useState<DatabaseCredentials | null>(null);
  const [credsError, setCredsError] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [dropConfirm, setDropConfirm] = useState("");
  const [removeOpen, setRemoveOpen] = useState(false);
  const [removeConfirm, setRemoveConfirm] = useState("");
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [version, setVersion] = useState<string | null>(null);

  if (!hasDb) return <AddDatabaseCard project={project} />;
  if (info.isPending) return <Spinner />;
  if (info.isError) return <ErrorState message={info.error.message} />;
  const d = info.data;
  const externalHost = publicHost || window.location.hostname;
  const versions = runtimes.data?.runtimes.find((r) => r.key === d.type)?.versions ?? [];
  const currentVersion = version ?? d.version;
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : fallback });

  const revealCredentials = async () => {
    setCredsError(null);
    try {
      setCreds((await api.database.credentials(project.id)).credentials);
    } catch (err) {
      setCredsError(err instanceof ApiError ? err.message : t("Could not load credentials"));
    }
  };

  const createDb = (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    create.mutate(newName.trim(), {
      onSuccess: () => {
        setNewName("");
        setMsg({ tone: "green", text: `Database “${newName.trim()}” created.` });
      },
      onError: (err) => fail(err, t("Creating the database failed")),
    });
  };

  return (
    <div className="space-y-6">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Database className="size-4 text-accent-500" aria-hidden />
                {t("Connection")}
              </span>
            }
            description={t("Inside the project network. These values are injected into the PHP container.")}
            actions={
              <span className="inline-flex items-center gap-1.5 text-xs">
                <StatusDot tone={containerStateTone(d.state)} />
                {d.state}
                {d.health && <Badge tone={d.health === "healthy" ? "green" : d.health === "starting" ? "blue" : "red"}>{d.health}</Badge>}
              </span>
            }
          />
          <dl className="px-5 py-3">
            <CopyRow label={t("Host")} value={d.host} />
            <CopyRow label={t("Port")} value={String(d.port)} />
            <CopyRow label={t("Database")} value={d.database} />
            <CopyRow label={t("Username")} value={d.username} />
            {creds ? (
              <>
                <CopyRow label={t("Password")} value={creds.password} secret />
                {creds.rootPassword && <CopyRow label={t("Root password")} value={creds.rootPassword} secret />}
                <CopyRow label="DATABASE_URL" value={creds.url} secret />
              </>
            ) : (
              <div className="flex items-center justify-between gap-3 py-1.5">
                <dt className="w-36 shrink-0 text-sm text-muted">{t("Password")}</dt>
                <dd className="flex-1 font-mono text-xs text-subtle">••••••••••••</dd>
                <Button size="sm" onClick={() => void revealCredentials()} icon={<Eye className="size-3.5" />}>
                  {t("Reveal")}
                </Button>
              </div>
            )}
            {credsError && <p className="py-1 text-xs text-red-500">{credsError}</p>}
          </dl>
          <div className="flex flex-wrap items-center justify-between gap-2 border-t border-default px-5 py-3">
            <p className="text-xs text-subtle">{t("Injected")}: {d.injectedEnv.map((k) => k).join(", ")}</p>
            <Button
              size="sm"
              icon={<KeyRound className="size-3.5" />}
              loading={rotate.isPending}
              disabled={!running}
              title={running ? t("Generate a new password for the project user") : t("Start the project to rotate the password")}
              onClick={() => {
                setMsg(null);
                setCreds(null);
                rotate.mutate(undefined, {
                  onSuccess: () => setMsg({ tone: "green", text: t("Password rotated. The PHP container was recreated with the new credentials.") }),
                  onError: (err) => fail(err, t("Rotation failed")),
                });
              }}
            >
              {t("Rotate password")}
            </Button>
          </div>
        </Card>

        <div className="space-y-6">
          <Card>
            <CardHeader title={t("External access")} description={t("Connect from your workstation with a database client.")} />
            <div className="space-y-3 p-5">
              <Checkbox
                label={t("Publish port on the host")}
                description={d.hostPort ? t("Reachable at {{address}}", { address: `${externalHost}:${d.hostPort}` }) : t("A free port from the project port range is assigned automatically.")}
                checked={d.hostPort > 0}
                disabled={expose.isPending}
                onChange={(e) => {
                  setMsg(null);
                  expose.mutate(e.target.checked, { onError: (err) => fail(err, t("Changing the port failed")) });
                }}
              />
              {d.hostPort > 0 && (
                <dl>
                  <CopyRow label={t("Host")} value={externalHost} />
                  <CopyRow label={t("Port")} value={String(d.hostPort)} />
                </dl>
              )}
            </div>
          </Card>

          <Card>
            <CardHeader title={t("Server")} />
            <div className="space-y-4 p-5">
              <div className="flex items-end gap-2">
                <Field label={t("{{engine}} version", { engine: ({ mariadb: "MariaDB", mysql: "MySQL", postgresql: "PostgreSQL", mongodb: "MongoDB" } as Record<string, string>)[d.type] ?? d.type })} htmlFor="db-version" hint={d.type === "postgresql" ? t("PostgreSQL cannot upgrade an existing data directory in place.") : d.type === "mongodb" ? t("MongoDB upgrades one major version at a time; back up first.") : t("Upgrades keep the data volume; downgrades are refused.")}>
                  <Select id="db-version" value={currentVersion} onChange={(e) => setVersion(e.target.value)}>
                    {versions.map((v) => (
                      <option key={v.version} value={v.version}>
                        {v.label}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Button
                  variant="primary"
                  disabled={currentVersion === d.version}
                  loading={update.isPending}
                  onClick={() => {
                    setMsg(null);
                    update.mutate(
                      { database: { enabled: true, version: currentVersion, exposePort: d.hostPort > 0 } },
                      { onSuccess: () => { setVersion(null); setMsg({ tone: "green", text: t("Version changed. The database container was recreated.") }); }, onError: (err) => fail(err, t("Version change failed")) },
                    );
                  }}
                >
                  {t("Apply")}
                </Button>
              </div>
              <dl className="text-sm">
                <CopyRow label={t("Image")} value={d.image} />
                <CopyRow label={t("Volume")} value={d.volumeName} />
              </dl>
              <Button variant="ghost" size="sm" className="text-red-600 dark:text-red-400" icon={<Trash2 className="size-3.5" />} onClick={() => setRemoveOpen(true)}>
                {t("Remove database and data")}
              </Button>
            </div>
          </Card>
        </div>
      </div>

      <Card>
        <CardHeader title={t("Databases")} description={t("Databases on this server. The project user “{{user}}” gets full access to databases created here.", { user: d.username })} />
        <div className="p-5">
          {!running ? (
            <p className="text-sm text-muted">{t("Start the project to manage databases.")}</p>
          ) : list.isPending ? (
            <Spinner />
          ) : list.isError ? (
            <ErrorState message={list.error.message} />
          ) : (
            <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
              {list.data.map((name) => (
                <li key={name} className="flex items-center justify-between px-3 py-2">
                  <span className="font-mono text-sm">
                    {name}
                    {name === d.database && <Badge className="ml-2">{t("primary")}</Badge>}
                  </span>
                  {name !== d.database && (
                    <Button variant="ghost" size="sm" aria-label={t("Drop {{name}}", { name })} onClick={() => { setDropTarget(name); setDropConfirm(""); }}>
                      <Trash2 className="size-4" />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {running && (
            <form onSubmit={createDb} className="mt-4 flex items-end gap-2">
              <Field label={t("New database")} htmlFor="new-db" hint={t("Lower-case letters, digits and underscores.")}>
                <Input id="new-db" value={newName} onChange={(e) => setNewName(e.target.value.toLowerCase().replace(/[^a-z0-9_]/g, ""))} placeholder="reports" spellCheck={false} />
              </Field>
              <Button type="submit" variant="primary" icon={<Plus className="size-4" />} loading={create.isPending} disabled={!newName}>
                {t("Create")}
              </Button>
            </form>
          )}
        </div>
      </Card>

      <Dialog
        open={dropTarget !== null}
        onClose={() => setDropTarget(null)}
        title={t("Drop database “{{name}}”?", { name: dropTarget ?? "" })}
        description={t("All tables and data in this database are deleted permanently.")}
        footer={
          <>
            <Button onClick={() => setDropTarget(null)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              disabled={dropConfirm !== dropTarget}
              loading={drop.isPending}
              onClick={() =>
                dropTarget &&
                drop.mutate(dropTarget, {
                  onSuccess: () => { setDropTarget(null); setMsg({ tone: "green", text: t("Database “{{name}}” dropped.", { name: dropTarget }) }); },
                  onError: (err) => { setDropTarget(null); fail(err, t("Dropping the database failed")); },
                })
              }
            >
              {t("Drop database")}
            </Button>
          </>
        }
      >
        <Field label={t("Type {{slug}} to confirm", { slug: dropTarget ?? "" })} htmlFor="drop-confirm">
          <Input id="drop-confirm" value={dropConfirm} onChange={(e) => setDropConfirm(e.target.value)} autoComplete="off" />
        </Field>
      </Dialog>

      <Dialog
        open={removeOpen}
        onClose={() => setRemoveOpen(false)}
        title={t("Remove database service?")}
        description={t("This stops and removes the database container {{container}} and deletes the volume {{volume}} with all data. The PHP container is recreated without database variables.", { container: `staqio-${project.slug}-database`, volume: d.volumeName })}
        footer={
          <>
            <Button onClick={() => setRemoveOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              disabled={removeConfirm !== d.database}
              loading={update.isPending}
              icon={<Trash2 className="size-4" />}
              onClick={() =>
                update.mutate(
                  { database: { enabled: false, removeData: true } },
                  { onSuccess: () => setRemoveOpen(false), onError: (err) => { setRemoveOpen(false); fail(err, t("Removing the database failed")); } },
                )
              }
            >
              {t("Remove database and data")}
            </Button>
          </>
        }
      >
        <Field label={t("Type {{slug}} to confirm", { slug: d.database })} htmlFor="remove-confirm">
          <Input id="remove-confirm" value={removeConfirm} onChange={(e) => setRemoveConfirm(e.target.value)} autoComplete="off" />
        </Field>
      </Dialog>
    </div>
  );
}
