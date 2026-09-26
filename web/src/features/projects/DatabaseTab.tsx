import { clsx } from "clsx";
import { Check, Copy, Database, Eye, EyeOff, ExternalLink, KeyRound, Pencil, Plus, Trash2, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState, type FormEvent } from "react";
import { api } from "@/api/client";
import { useDatabaseInfo, useDatabaseList, useDatabaseMutations, useDBTool, useOpenDBTool, usePublicHost, useRuntimes, useUpdateProject } from "@/api/hooks";
import type { DatabaseCredentials, ExternalDatabase, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Dialog, ErrorState, Field, Input, Select, Spinner, StatusDot } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { PublicHostNotice } from "@/components/PublicHostNotice";
import { CloneDatabaseCard, SnapshotsCard } from "./DatabaseSnapshots";
import { databaseEngineNames, databaseEnvPrefix, databaseNamePattern, databaseServices } from "./databases";
import { containerStateTone } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { emptyExternalDatabase, externalDatabaseComplete, ExternalDatabaseFields, externalDatabaseTypes } from "./ExternalConnection";

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

function AddDatabaseCard({ project, onAdded, onCancel }: { project: Project; onAdded: (name: string) => void; onCancel?: () => void }) {
  const { t } = useTranslation();
  const runtimes = useRuntimes();
  const update = useUpdateProject(project.id);
  const existing = databaseServices(project);
  // Without a primary the first database becomes it (host "database", DB_*); every further one needs a name.
  const hasPrimary = existing.some((d) => d.name === "");
  const [name, setName] = useState("");
  const [type, setType] = useState("mariadb");
  const [version, setVersion] = useState("");
  const [expose, setExpose] = useState(false);
  const [external, setExternal] = useState(false);
  const [conn, setConn] = useState<ExternalDatabase>(emptyExternalDatabase);
  const [error, setError] = useState<string | null>(null);
  const dbs = runtimes.data?.runtimes.filter((r) => r.kind === "database" && r.available && (!external || externalDatabaseTypes.includes(r.key))) ?? [];
  const selected = dbs.find((d) => d.key === type);
  const chosenVersion = version || selected?.versions.find((v) => v.default)?.version || "";
  const trimmed = name.trim();
  const nameError =
    trimmed && !databaseNamePattern.test(trimmed)
      ? t("Lowercase letters, digits and dashes, starting with a letter.")
      : existing.some((d) => d.name === trimmed && trimmed)
        ? t("The project already has a database of this name.")
        : undefined;
  const needsName = hasPrimary && !trimmed;

  return (
    <Card>
      <CardHeader
        title={existing.length === 0 ? t("Database") : t("Add database")}
        description={
          existing.length === 0
            ? t("This project has no database yet. Adding one creates a container with a persistent volume and injects the connection variables into the application containers (PHP, Python, Go, Ruby, Node).")
            : t("An additional database runs in a container of its own with its own volume and credentials. It is reached at its name as host and injects variables that start with its name (ANALYTICS_DB_HOST, ANALYTICS_DATABASE_URL …).")
        }
      />
      <div className="space-y-4 p-5">
        {error && <Alert tone="red">{error}</Alert>}
        {existing.length > 0 && (
          <Field
            label={hasPrimary ? t("Name") : t("Name (optional)")}
            htmlFor="add-db-name"
            error={nameError}
            hint={trimmed && !nameError ? t("Host {{host}}, variables {{prefix}}_DB_HOST, {{prefix}}_DATABASE_URL …", { host: trimmed, prefix: databaseEnvPrefix(trimmed) }) : hasPrimary ? t("Becomes the host name and the prefix of the variables, e.g. analytics.") : t("Leave empty to add the primary database (host database, DB_* variables).")}
          >
            <Input id="add-db-name" value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="analytics" spellCheck={false} autoComplete="off" />
          </Field>
        )}
        <div className="flex flex-wrap gap-4" role="radiogroup" aria-label={t("Where the database runs")}>
          <label className="inline-flex items-center gap-2 text-sm">
            <input type="radio" name="add-db-where" checked={!external} onChange={() => setExternal(false)} />
            {t("In a container of the project")}
          </label>
          <label className="inline-flex items-center gap-2 text-sm">
            <input type="radio" name="add-db-where" checked={external} onChange={() => { setExternal(true); if (!externalDatabaseTypes.includes(type)) setType("mariadb"); }} />
            {t("On an external server")}
          </label>
        </div>
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
          <Field label={t("Version")} htmlFor="add-db-version" hint={external ? t("Picks the client tools for backups and the connection; choose the server's major version.") : undefined}>
            <Select id="add-db-version" value={chosenVersion} onChange={(e) => setVersion(e.target.value)}>
              {selected?.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {external ? (
          <ExternalDatabaseFields id="add-db-ext" type={type} version={chosenVersion} value={conn} onChange={setConn} />
        ) : (
          <Checkbox label={t("Publish database port on the host")} description={t("For external clients such as TablePlus or DBeaver.")} checked={expose} onChange={(e) => setExpose(e.target.checked)} />
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="primary"
            icon={<Plus className="size-4" />}
            loading={update.isPending}
            disabled={!selected || !!nameError || needsName || (external && !externalDatabaseComplete(conn))}
            onClick={() => {
              setError(null);
              const spec = external ? { enabled: true, type, version: chosenVersion, external: conn } : { enabled: true, type, version: chosenVersion, exposePort: expose };
              update.mutate(trimmed ? { databases: { [trimmed]: spec } } : { database: spec }, {
                onSuccess: () => onAdded(trimmed),
                onError: (err) => setError(errorText(err, t, t("Adding the database failed"))),
              });
            }}
          >
            {t("Add database")}
          </Button>
          {onCancel && <Button onClick={onCancel}>{t("Cancel")}</Button>}
        </div>
      </div>
    </Card>
  );
}

/** The Database tab: every database of the project, one at a time, and adding another. */
export function DatabaseTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const dbs = databaseServices(project);
  const [selected, setSelected] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  if (dbs.length === 0) return <AddDatabaseCard project={project} onAdded={(name) => setSelected(name)} />;
  const current = selected !== null && dbs.some((d) => d.name === selected) ? selected : dbs[0]!.name;
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label={t("Databases of the project")}>
        {dbs.map((d) => (
          <button
            key={d.kind}
            type="button"
            aria-pressed={!adding && d.name === current}
            onClick={() => { setAdding(false); setSelected(d.name); }}
            className={clsx(
              "inline-flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm",
              !adding && d.name === current ? "border-accent-500 bg-accent-500/10 font-medium text-accent-700 dark:text-accent-300" : "border-default hover:bg-muted",
            )}
          >
            <Database className="size-3.5" aria-hidden />
            <span className={d.name ? "font-mono" : undefined}>{d.name || t("Primary")}</span>
            <span className="text-xs text-subtle">{databaseEngineNames[d.variant] ?? d.variant}</span>
          </button>
        ))}
        <Button size="sm" variant={adding ? "primary" : "secondary"} icon={<Plus className="size-3.5" />} onClick={() => setAdding(true)}>
          {t("Add database")}
        </Button>
      </div>
      {adding ? (
        <AddDatabaseCard project={project} onAdded={(name) => { setAdding(false); setSelected(name); }} onCancel={() => setAdding(false)} />
      ) : (
        <DatabasePanel key={current} project={project} db={current} onRemoved={() => setSelected(null)} />
      )}
    </div>
  );
}

/** One database of the project: connection, access, server, snapshots and the databases on it. */
function DatabasePanel({ project, db, onRemoved }: { project: Project; db: string; onRemoved: () => void }) {
  const { t } = useTranslation();
  const info = useDatabaseInfo(project.id, true, db);
  const publicHost = usePublicHost();
  const runtimes = useRuntimes();
  const update = useUpdateProject(project.id);
  const { rotate, expose, create, drop } = useDatabaseMutations(project.id, db);
  const dbTool = useDBTool();
  const openTool = useOpenDBTool(project.id);
  const running = info.data?.state === "running";
  // An external server is there whether or not the project runs.
  const external = !!info.data?.external;
  const usable = running || external;
  const list = useDatabaseList(project.id, usable, db);

  const [creds, setCreds] = useState<DatabaseCredentials | null>(null);
  const [credsError, setCredsError] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [dropConfirm, setDropConfirm] = useState("");
  const [removeOpen, setRemoveOpen] = useState(false);
  const [removeConfirm, setRemoveConfirm] = useState("");
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [version, setVersion] = useState<string | null>(null);
  const [editOpen, setEditOpen] = useState(false);
  const [conn, setConn] = useState<ExternalDatabase>(emptyExternalDatabase);

  if (info.isPending) return <Spinner />;
  if (info.isError) return <ErrorState message={errorText(info.error, t)} />;
  const d = info.data;
  const externalHost = publicHost || window.location.hostname;
  const versions = runtimes.data?.runtimes.find((r) => r.key === d.type)?.versions ?? [];
  const currentVersion = version ?? d.version;
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: errorText(err, t, fallback) });

  const revealCredentials = async () => {
    setCredsError(null);
    try {
      setCreds((await api.database.credentials(project.id, db)).credentials);
    } catch (err) {
      setCredsError(errorText(err, t, t("Could not load credentials")));
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
      <PublicHostNotice />
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
            description={
              external
                ? t("An external server Envoryx does not run. These values are injected into the application containers (PHP, Python, Go, Ruby, Node).")
                : db
                  ? t("Inside the project network at the host “{{host}}”. The application containers (PHP, Python, Go, Ruby, Node) receive these values as {{prefix}}_DB_* and {{prefix}}_DATABASE_URL.", { host: d.host, prefix: databaseEnvPrefix(db) })
                  : t("Inside the project network. These values are injected into the application containers (PHP, Python, Go, Ruby, Node).")
            }
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
            {external ? (
              <Button
                size="sm"
                icon={<Pencil className="size-3.5" />}
                onClick={() => {
                  setConn({ host: d.host, port: d.port, username: d.username, password: "", database: d.database });
                  setEditOpen(true);
                }}
              >
                {t("Edit connection")}
              </Button>
            ) : (
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
                    onSuccess: () => setMsg({ tone: "green", text: t("Password rotated. The application containers were recreated with the new credentials.") }),
                    onError: (err) => fail(err, t("Rotation failed")),
                  });
                }}
              >
                {t("Rotate password")}
              </Button>
            )}
          </div>
        </Card>

        <div className="space-y-6">
          <Card>
            <CardHeader title={t("External access")} description={t("Connect from your workstation with a database client.")} />
            <div className="space-y-3 p-5">
              {dbTool.data?.supported.includes(d.type) && (
                <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-default px-3 py-2">
                  <div>
                    <p className="text-sm font-medium">{t("Open in the browser")}</p>
                    <p className="text-xs text-muted">
                      {dbTool.data.enabled ? t("Adminer, logged in as the project user. Opens in a new tab.") : t("Enable the database browser in Settings first.")}
                    </p>
                  </div>
                  <Button
                    size="sm"
                    icon={<ExternalLink className="size-3.5" />}
                    disabled={!dbTool.data.enabled || !usable}
                    loading={openTool.isPending}
                    title={!usable ? t("Start the project first") : undefined}
                    onClick={() => {
                      setMsg(null);
                      openTool.mutate(db, {
                        onSuccess: (link) => {
                          if (!window.open(link.url, "_blank", "noopener")) {
                            setMsg({ tone: "red", text: t("The browser blocked the new tab; allow pop-ups for Envoryx.") });
                          }
                        },
                        onError: (err) => fail(err, t("Opening the database browser failed")),
                      });
                    }}
                  >
                    {t("Open database")}
                  </Button>
                </div>
              )}
              {external ? (
                <p className="text-sm text-muted">{t("Connect your client to the server itself, at {{address}}.", { address: `${d.host === "host.docker.internal" ? externalHost : d.host}:${d.port}` })}</p>
              ) : (
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
              )}
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
                <Field label={t("{{engine}} version", { engine: ({ mariadb: "MariaDB", mysql: "MySQL", postgresql: "PostgreSQL", mongodb: "MongoDB" } as Record<string, string>)[d.type] ?? d.type })} htmlFor="db-version" hint={external ? t("Picks the client tools for backups and the connection; choose the server's major version.") : d.type === "postgresql" ? t("PostgreSQL cannot upgrade an existing data directory in place.") : d.type === "mongodb" ? t("MongoDB upgrades one major version at a time; a database backup is taken automatically first.") : t("Upgrades keep the data volume and take a database backup first; downgrades are refused.")}>
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
                    const spec = { enabled: true, version: currentVersion, exposePort: d.hostPort > 0 };
                    update.mutate(
                      db ? { databases: { [db]: spec } } : { database: spec },
                      { onSuccess: () => { setVersion(null); setMsg({ tone: "green", text: external ? t("Client version changed.") : t("Version changed. The database container was recreated.") }); }, onError: (err) => fail(err, t("Version change failed")) },
                    );
                  }}
                >
                  {t("Apply")}
                </Button>
              </div>
              <dl className="text-sm">
                <CopyRow label={t("Image")} value={d.image} />
                {!external && <CopyRow label={t("Volume")} value={d.volumeName} />}
              </dl>
              <Button variant="ghost" size="sm" className="text-red-600 dark:text-red-400" icon={<Trash2 className="size-3.5" />} onClick={() => setRemoveOpen(true)}>
                {external ? t("Remove connection") : t("Remove database and data")}
              </Button>
            </div>
          </Card>
        </div>
      </div>

      <SnapshotsCard project={project} database={d} onMessage={setMsg} />
      <CloneDatabaseCard project={project} database={d} onMessage={setMsg} />

      <Card>
        <CardHeader
          title={t("Databases")}
          description={external ? t("Databases on the external server that “{{user}}” can see. Envoryx creates databases there but never drops any.", { user: d.username }) : t("Databases on this server. The project user “{{user}}” gets full access to databases created here.", { user: d.username })}
        />
        <div className="p-5">
          {!usable ? (
            <p className="text-sm text-muted">{t("Start the project to manage databases.")}</p>
          ) : list.isPending ? (
            <Spinner />
          ) : list.isError ? (
            <ErrorState message={errorText(list.error, t)} />
          ) : (
            <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
              {list.data.map((name) => (
                <li key={name} className="flex items-center justify-between px-3 py-2">
                  <span className="font-mono text-sm">
                    {name}
                    {name === d.database && <Badge className="ml-2">{t("primary")}</Badge>}
                  </span>
                  {name !== d.database && !external && (
                    <Button variant="ghost" size="sm" aria-label={t("Drop {{name}}", { name })} onClick={() => { setDropTarget(name); setDropConfirm(""); }}>
                      <Trash2 className="size-4" />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {usable && (
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
        open={removeOpen && external}
        onClose={() => setRemoveOpen(false)}
        title={t("Remove the connection?")}
        description={t("Envoryx forgets the connection to {{address}}; the server and its data are not touched. The application containers are recreated without these database variables.", { address: `${d.host}:${d.port}` })}
        footer={
          <>
            <Button onClick={() => setRemoveOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              loading={update.isPending}
              icon={<Trash2 className="size-4" />}
              onClick={() =>
                update.mutate(db ? { databases: { [db]: { enabled: false } } } : { database: { enabled: false } }, {
                  onSuccess: () => { setRemoveOpen(false); onRemoved(); },
                  onError: (err) => { setRemoveOpen(false); fail(err, t("Removing the database failed")); },
                })
              }
            >
              {t("Remove connection")}
            </Button>
          </>
        }
      />

      <Dialog
        open={editOpen}
        onClose={() => setEditOpen(false)}
        title={t("Edit connection")}
        description={t("The new connection is tested before it is stored; the application containers are then recreated with it.")}
        footer={
          <>
            <Button onClick={() => setEditOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="primary"
              disabled={!externalDatabaseComplete(conn, true)}
              loading={update.isPending}
              onClick={() => {
                const spec = { enabled: true, version: d.version, external: conn };
                update.mutate(db ? { databases: { [db]: spec } } : { database: spec }, {
                  onSuccess: () => { setEditOpen(false); setCreds(null); setMsg({ tone: "green", text: t("Connection saved. The application containers were recreated with it.") }); },
                  onError: (err) => { setEditOpen(false); fail(err, t("Saving the connection failed")); },
                });
              }}
            >
              {t("Save")}
            </Button>
          </>
        }
      >
        <ExternalDatabaseFields id={`edit-db-${db || "primary"}`} type={d.type} version={d.version} value={conn} onChange={setConn} passwordOptional />
      </Dialog>

      <Dialog
        open={removeOpen && !external}
        onClose={() => setRemoveOpen(false)}
        title={t("Remove database service?")}
        description={t("This stops and removes the database container {{container}} and deletes the volume {{volume}} with all data. The application containers are recreated without database variables.", { container: `envoryx-${project.slug}-${d.service || "database"}`, volume: d.volumeName })}
        footer={
          <>
            <Button onClick={() => setRemoveOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              disabled={removeConfirm !== (db || d.database)}
              loading={update.isPending}
              icon={<Trash2 className="size-4" />}
              onClick={() =>
                update.mutate(
                  db ? { databases: { [db]: { enabled: false, removeData: true } } } : { database: { enabled: false, removeData: true } },
                  { onSuccess: () => { setRemoveOpen(false); onRemoved(); }, onError: (err) => { setRemoveOpen(false); fail(err, t("Removing the database failed")); } },
                )
              }
            >
              {t("Remove database and data")}
            </Button>
          </>
        }
      >
        <Field label={t("Type {{slug}} to confirm", { slug: db || d.database })} htmlFor="remove-confirm">
          <Input id="remove-confirm" value={removeConfirm} onChange={(e) => setRemoveConfirm(e.target.value)} autoComplete="off" />
        </Field>
      </Dialog>
    </div>
  );
}
