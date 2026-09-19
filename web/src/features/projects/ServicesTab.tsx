import { ExternalLink, Mail, Plus, Server, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { useExtraServices, usePublicHost, useRuntimes, useStorage, useUpdateProject } from "@/api/hooks";
import type { ExtraServiceInfo, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Dialog, ErrorState, Field, Input, Select, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone } from "@/lib/format";
import { AddStorageCard, StorageCard } from "./StorageCard";

function ServiceCard({ project, info, onMessage }: { project: Project; info: ExtraServiceInfo; onMessage: (m: { tone: "green" | "red"; text: string }) => void }) {
  const { t } = useTranslation();
  const update = useUpdateProject(project.id);
  const publicHost = usePublicHost();
  const runtimes = useRuntimes();
  const versions = runtimes.data?.runtimes.find((r) => r.key === info.kind)?.versions ?? [];
  const [version, setVersion] = useState(info.version);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const host = publicHost || window.location.hostname;
  const key = info.kind as "redis" | "mailpit";
  const title = info.kind === "redis" ? "Redis" : "Mailpit";
  const fail = (err: unknown, fallback: string) => onMessage({ tone: "red", text: err instanceof ApiError ? err.message : fallback });

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            {info.kind === "mailpit" ? <Mail className="size-4 text-accent-500" aria-hidden /> : <Server className="size-4 text-accent-500" aria-hidden />}
            {title} {info.version}
          </span>
        }
        actions={
          <span className="inline-flex items-center gap-1.5 text-xs">
            <StatusDot tone={containerStateTone(info.state)} />
            {info.state}
            {info.health && <Badge tone={info.health === "healthy" ? "green" : info.health === "starting" ? "blue" : "red"}>{info.health}</Badge>}
          </span>
        }
      />
      <div className="space-y-4 p-5">
        <dl className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[9rem_1fr]">
          <dt className="text-muted">{t("Internal host")}</dt>
          <dd className="font-mono text-xs">
            {info.host}:{info.port}
          </dd>
          <dt className="text-muted">{t("Injected")}</dt>
          <dd className="font-mono text-xs">{info.injectedEnv.join(", ")}</dd>
          {info.volumeName && (
            <>
              <dt className="text-muted">{t("Volume")}</dt>
              <dd className="font-mono text-xs">{info.volumeName}</dd>
            </>
          )}
          {info.kind === "mailpit" && (
            <>
              <dt className="text-muted">{t("Inbox")}</dt>
              <dd>
                {info.webUiPort ? (
                  <a href={`http://${host}:${info.webUiPort}`} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
                    http://{host}:{info.webUiPort} <ExternalLink className="size-3" />
                  </a>
                ) : (
                  <span className="text-xs text-subtle">{t("no port")}</span>
                )}
              </dd>
            </>
          )}
        </dl>

        {info.kind === "redis" && (
          <Checkbox
            label={t("Publish port on the host")}
            description={info.hostPort ? t("Reachable at {{address}}", { address: `${host}:${info.hostPort}` }) : t("For desktop clients like RedisInsight.")}
            checked={info.hostPort > 0}
            disabled={update.isPending}
            onChange={(e) => update.mutate({ [key]: { enabled: true, version: info.version, exposePort: e.target.checked } }, { onError: (err) => fail(err, t("Changing the port failed")) })}
          />
        )}

        {versions.length > 1 && (
          <div className="flex items-end gap-2">
            <Field label={t("Version")} htmlFor={`${info.kind}-version`}>
              <Select id={`${info.kind}-version`} value={version} onChange={(e) => setVersion(e.target.value)}>
                {versions.map((v) => (
                  <option key={v.version} value={v.version}>
                    {v.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Button
              variant="primary"
              disabled={version === info.version}
              loading={update.isPending}
              onClick={() =>
                update.mutate(
                  { [key]: { enabled: true, version, exposePort: info.hostPort > 0 } },
                  { onSuccess: () => onMessage({ tone: "green", text: t("{{service}} updated to {{version}}.", { service: title, version }) }), onError: (err) => fail(err, t("Version change failed")) },
                )
              }
            >
              {t("Apply")}
            </Button>
          </div>
        )}

        <Button variant="ghost" size="sm" className="text-red-600 dark:text-red-400" icon={<Trash2 className="size-3.5" />} onClick={() => setRemoveOpen(true)}>
          {info.volumeName ? t("Remove {{service}} and data", { service: title }) : t("Remove {{service}}", { service: title })}
        </Button>
      </div>

      <Dialog
        open={removeOpen}
        onClose={() => setRemoveOpen(false)}
        title={t("Remove {{service}}?", { service: title })}
        description={info.volumeName ? t("This removes the container and deletes the volume {{volume}} with all data. PHP is recreated without the {{service}} variables.", { volume: info.volumeName, service: title }) : t("This removes the container. PHP is recreated without the {{service}} variables.", { service: title })}
        footer={
          <>
            <Button onClick={() => setRemoveOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              disabled={!!info.volumeName && confirm !== info.kind}
              loading={update.isPending}
              onClick={() =>
                update.mutate(
                  { [key]: { enabled: false, removeData: true } },
                  { onSuccess: () => { setRemoveOpen(false); onMessage({ tone: "green", text: t("{{service}} removed.", { service: title }) }); }, onError: (err) => { setRemoveOpen(false); fail(err, t("Removing failed")); } },
                )
              }
            >
              {t("Remove")}
            </Button>
          </>
        }
      >
        {info.volumeName && (
          <Field label={t("Type {{slug}} to confirm", { slug: info.kind })} htmlFor="remove-extra">
            <Input id="remove-extra" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
          </Field>
        )}
      </Dialog>
    </Card>
  );
}

function AddServiceCard({ project, kind, onMessage }: { project: Project; kind: "redis" | "mailpit"; onMessage: (m: { tone: "green" | "red"; text: string }) => void }) {
  const { t } = useTranslation();
  const update = useUpdateProject(project.id);
  const runtimes = useRuntimes();
  const rt = runtimes.data?.runtimes.find((r) => r.key === kind);
  const [version, setVersion] = useState("");
  const [expose, setExpose] = useState(false);
  const title = kind === "redis" ? "Redis" : "Mailpit";
  return (
    <Card>
      <CardHeader title={title} description={rt?.description ?? ""} />
      <div className="space-y-4 p-5">
        {rt && rt.versions.length > 1 && (
          <Field label={t("Version")} htmlFor={`add-${kind}-version`}>
            <Select id={`add-${kind}-version`} value={version || rt.versions.find((v) => v.default)?.version || ""} onChange={(e) => setVersion(e.target.value)}>
              {rt.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {kind === "redis" && <Checkbox label={t("Publish port on the host")} checked={expose} onChange={(e) => setExpose(e.target.checked)} />}
        <Button
          variant="primary"
          icon={<Plus className="size-4" />}
          loading={update.isPending}
          onClick={() =>
            update.mutate(
              { [kind]: { enabled: true, version: version || undefined, exposePort: expose } },
              { onSuccess: () => onMessage({ tone: "green", text: t("{{service}} added. PHP was recreated with the new variables.", { service: title }) }), onError: (err) => onMessage({ tone: "red", text: err instanceof ApiError ? err.message : t("Adding failed") }) },
            )
          }
        >
          {t("Add {{service}}", { service: title })}
        </Button>
      </div>
    </Card>
  );
}

export function ServicesTab({ project }: { project: Project }) {
  const extras = useExtraServices(project.id);
  const storage = useStorage(project.id);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  if (extras.isPending || storage.isPending) return <Spinner />;
  if (extras.isError) return <ErrorState message={extras.error.message} />;
  const has = (k: string) => extras.data.some((s) => s.kind === k);
  return (
    <div className="space-y-6">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <div className="grid gap-6 lg:grid-cols-2">
        {storage.data && <StorageCard project={project} onMessage={setMsg} />}
        {extras.data.map((s) => (
          <ServiceCard key={s.kind} project={project} info={s} onMessage={setMsg} />
        ))}
        {!has("redis") && <AddServiceCard project={project} kind="redis" onMessage={setMsg} />}
        {!has("mailpit") && <AddServiceCard project={project} kind="mailpit" onMessage={setMsg} />}
        {!storage.data && <AddStorageCard project={project} onMessage={setMsg} />}
      </div>
    </div>
  );
}
