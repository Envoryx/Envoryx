import { ExternalLink, Package, Plus, Save, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useProjectAddons, useSetAddon } from "@/api/hooks";
import type { AvailableAddon, Project, ProjectAddon } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Dialog, ErrorState, Field, Input, Select, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { CopyRow } from "./DatabaseTab";

type OnMessage = (m: { tone: "green" | "red"; text: string }) => void;

function AddonCard({ project, info, onMessage }: { project: Project; info: ProjectAddon; onMessage: OnMessage }) {
  const { t } = useTranslation();
  const set = useSetAddon(project.id);
  const canChange = (project.access ?? "admin") !== "read";
  const [version, setVersion] = useState(info.version);
  const [expose, setExpose] = useState(!!info.hostPort);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const dirty = version !== info.version || (info.publishPort && expose !== !!info.hostPort);
  const fail = (err: unknown) => onMessage({ tone: "red", text: errorText(err, t, t("Saving failed")) });
  const host = window.location.hostname;

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Package className="size-4 text-accent-500" aria-hidden /> {info.title} {info.version}
          </span>
        }
        description={info.description}
        actions={
          <span className="inline-flex items-center gap-1.5 text-xs">
            <StatusDot tone={containerStateTone(info.state)} />
            {info.state}
            {info.health && <Badge tone={info.health === "healthy" ? "green" : info.health === "starting" ? "blue" : "red"}>{info.health}</Badge>}
          </span>
        }
      />
      <div className="space-y-4 p-5">
        {!info.installed && <Alert tone="amber">{t("The addon file is no longer installed. The project keeps running its copy, but it cannot be changed.")}</Alert>}
        <dl className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[9rem_1fr]">
          {info.url && (
            <>
              <dt className="text-muted">{t("Web UI")}</dt>
              <dd>
                <a href={info.url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
                  {info.url} <ExternalLink className="size-3" />
                </a>
              </dd>
            </>
          )}
          {info.port ? (
            <>
              <dt className="text-muted">{t("Internal host")}</dt>
              <dd className="font-mono text-xs">
                {info.host}:{info.port}
              </dd>
            </>
          ) : null}
          {info.hostPort ? (
            <>
              <dt className="text-muted">{t("Host port")}</dt>
              <dd className="font-mono text-xs">
                {host}:{info.hostPort}
              </dd>
            </>
          ) : null}
          {info.injectedEnv.length > 0 && (
            <>
              <dt className="text-muted">{t("Injected")}</dt>
              <dd className="font-mono text-xs">{info.injectedEnv.join(", ")}</dd>
            </>
          )}
          {info.volumes.length > 0 && (
            <>
              <dt className="text-muted">{t("Volumes")}</dt>
              <dd className="font-mono text-xs">{info.volumes.join(", ")}</dd>
            </>
          )}
          <dt className="text-muted">{t("Image")}</dt>
          <dd className="font-mono text-xs">{info.image}</dd>
        </dl>
        {info.credentials.length > 0 && (
          <dl className="divide-y divide-[var(--border)]">
            {info.credentials.map((c) => (c.value ? <CopyRow key={c.label} label={c.label} value={c.value} secret={!!c.secret} /> : null))}
          </dl>
        )}
        {canChange && info.installed && (
          <div className="flex flex-wrap items-end gap-3">
            {info.versions.length > 1 && (
              <Field label={t("Version")} htmlFor={`addon-v-${info.name}`}>
                <Select id={`addon-v-${info.name}`} value={version} onChange={(e) => setVersion(e.target.value)}>
                  {info.versions.map((v) => (
                    <option key={v}>{v}</option>
                  ))}
                </Select>
              </Field>
            )}
            {info.publishPort && <Checkbox label={t("Publish the port on the host")} checked={expose} onChange={(e) => setExpose(e.target.checked)} />}
            {(info.versions.length > 1 || info.publishPort) && (
              <Button
                variant="primary"
                icon={<Save className="size-4" />}
                disabled={!dirty}
                loading={set.isPending && !set.variables?.removeData}
                onClick={() =>
                  set.mutate(
                    { name: info.name, enabled: true, version, exposePort: expose },
                    { onSuccess: () => onMessage({ tone: "green", text: t("{{name}} updated.", { name: info.title }) }), onError: fail },
                  )
                }
              >
                {t("Save")}
              </Button>
            )}
          </div>
        )}
        {canChange && (
          <Button variant="danger" size="sm" icon={<Trash2 className="size-4" />} onClick={() => setRemoveOpen(true)}>
            {t("Remove")}
          </Button>
        )}
      </div>
      <Dialog
        open={removeOpen}
        onClose={() => setRemoveOpen(false)}
        title={t("Remove {{name}}?", { name: info.title })}
        footer={
          <>
            <Button onClick={() => setRemoveOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              disabled={info.volumes.length > 0 && confirm !== info.name}
              loading={set.isPending}
              onClick={() =>
                set.mutate(
                  { name: info.name, enabled: false, removeData: true },
                  {
                    onSuccess: () => {
                      setRemoveOpen(false);
                      onMessage({ tone: "green", text: t("{{name}} removed.", { name: info.title }) });
                    },
                    onError: fail,
                  },
                )
              }
            >
              {t("Remove")}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          {info.volumes.length > 0
            ? t("The container and its volumes ({{volumes}}) are deleted. Type {{name}} to confirm.", { volumes: info.volumes.join(", "), name: info.name })
            : t("The container is deleted; the addon keeps no data.")}
        </p>
        {info.volumes.length > 0 && <Input aria-label={t("Confirmation")} className="mt-3" value={confirm} onChange={(e) => setConfirm(e.target.value)} />}
      </Dialog>
    </Card>
  );
}

function AddAddonCard({ project, addon, onMessage }: { project: Project; addon: AvailableAddon; onMessage: OnMessage }) {
  const { t } = useTranslation();
  const set = useSetAddon(project.id);
  const [version, setVersion] = useState(addon.versions[0] ?? "");
  const [expose, setExpose] = useState(false);
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Package className="size-4 text-subtle" aria-hidden /> {addon.title}
          </span>
        }
        description={addon.description}
      />
      <div className="flex flex-wrap items-end gap-3 p-5">
        {addon.versions.length > 1 && (
          <Field label={t("Version")} htmlFor={`addon-add-v-${addon.name}`}>
            <Select id={`addon-add-v-${addon.name}`} value={version} onChange={(e) => setVersion(e.target.value)}>
              {addon.versions.map((v) => (
                <option key={v}>{v}</option>
              ))}
            </Select>
          </Field>
        )}
        {addon.publishPort && <Checkbox label={t("Publish the port on the host")} checked={expose} onChange={(e) => setExpose(e.target.checked)} />}
        <Button
          icon={<Plus className="size-4" />}
          loading={set.isPending}
          onClick={() =>
            set.mutate(
              { name: addon.name, enabled: true, version, exposePort: expose },
              { onSuccess: () => onMessage({ tone: "green", text: t("{{name}} added.", { name: addon.title }) }), onError: (err) => onMessage({ tone: "red", text: errorText(err, t, t("Saving failed")) }) },
            )
          }
        >
          {t("Add {{name}}", { name: addon.title })}
        </Button>
      </div>
    </Card>
  );
}

/** The project's addons and the installed ones it can add. */
export function AddonsSection({ project }: { project: Project }) {
  const { t } = useTranslation();
  const q = useProjectAddons(project.id);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} />;
  const canAdd = (project.access ?? "admin") !== "read";
  const { addons, available } = q.data;
  if (addons.length === 0 && (available.length === 0 || !canAdd)) return null;
  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-base font-semibold">{t("Addons")}</h2>
        <p className="text-sm text-muted">{t("Services from addon files an admin installed under Settings, Addons.")}</p>
      </div>
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <div className="grid gap-6 lg:grid-cols-2">
        {addons.map((a) => (
          <AddonCard key={a.name} project={project} info={a} onMessage={setMsg} />
        ))}
        {canAdd && available.map((a) => <AddAddonCard key={a.name} project={project} addon={a} onMessage={setMsg} />)}
      </div>
    </section>
  );
}
