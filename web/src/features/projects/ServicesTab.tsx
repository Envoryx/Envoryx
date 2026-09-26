import { BrainCircuit, ExternalLink, Mail, MemoryStick, Pencil, Plus, Rabbit, Search, Server, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useExtraServices, usePublicHost, useRuntimes, useStorage, useUpdateProject } from "@/api/hooks";
import { api } from "@/api/client";
import type { ExternalRedis, ExtraServiceInfo, PHPConfig, Project, RabbitMQCredentials, SearchCredentials, UpdateProjectRequest } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Dialog, ErrorState, Field, Input, Select, Spinner, StatusDot } from "@/components/ui";
import { containerStateTone } from "@/lib/format";
import { AddStorageCard, StorageCard } from "./StorageCard";
import { PublicHostNotice } from "@/components/PublicHostNotice";
import { errorText } from "@/lib/errors";
import { CopyRow } from "./DatabaseTab";
import { OllamaModels } from "./OllamaModels";
import { emptyExternalRedis, ExternalRedisFields } from "./ExternalConnection";

type ExtraKind = "redis" | "memcached" | "mailpit" | "rabbitmq" | "meilisearch" | "typesense" | "opensearch" | "ollama";
const titles: Record<ExtraKind, string> = { redis: "Redis", memcached: "Memcached", mailpit: "Mailpit", rabbitmq: "RabbitMQ", meilisearch: "Meilisearch", typesense: "Typesense", opensearch: "OpenSearch", ollama: "Ollama" };
// Services whose port is always published because a web UI lives there.
const alwaysPublished = (kind: string) => kind === "mailpit" || kind === "meilisearch";
const isSearch = (kind: string): kind is "meilisearch" | "typesense" => kind === "meilisearch" || kind === "typesense";
// The PHP extension a service's clients need; the images ship them switched off.
const phpExtensions: Partial<Record<ExtraKind, string>> = { redis: "redis", memcached: "memcached", rabbitmq: "amqp" };

/** The PHP update that switches on the extension a service needs, or null when nothing is missing. */
function phpExtensionUpdate(project: Project, kind: ExtraKind): UpdateProjectRequest["php"] | null {
  const php = project.services.find((s) => s.kind === "php" && s.enabled);
  const ext = phpExtensions[kind];
  if (!php || !ext) return null;
  const config = php.config as unknown as PHPConfig;
  if (config.extensions?.includes(ext)) return null;
  return { enabled: true, version: php.version, config: { ...config, extensions: [...(config.extensions ?? []), ext].sort() } };
}

function ServiceCard({ project, info, onMessage }: { project: Project; info: ExtraServiceInfo; onMessage: (m: { tone: "green" | "red"; text: string }) => void }) {
  const { t } = useTranslation();
  const update = useUpdateProject(project.id);
  const publicHost = usePublicHost();
  const runtimes = useRuntimes();
  const versions = runtimes.data?.runtimes.find((r) => r.key === info.kind)?.versions ?? [];
  const [version, setVersion] = useState(info.version);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [creds, setCreds] = useState<RabbitMQCredentials | null>(null);
  const [searchCreds, setSearchCreds] = useState<SearchCredentials | null>(null);
  const [editOpen, setEditOpen] = useState(false);
  const [conn, setConn] = useState<ExternalRedis>(emptyExternalRedis);
  const host = publicHost || window.location.hostname;
  const key = info.kind as ExtraKind;
  const title = titles[key] ?? info.kind;
  const phpFix = phpExtensionUpdate(project, key);
  const fail = (err: unknown, fallback: string) => onMessage({ tone: "red", text: errorText(err, t, fallback) });
  const reveal = async () => {
    try {
      if (isSearch(info.kind)) setSearchCreds((await api.search.credentials(project.id, info.kind)).credentials);
      else setCreds((await api.rabbitmq.credentials(project.id)).credentials);
    } catch (err) {
      fail(err, t("Loading the credentials failed"));
    }
  };

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            {info.kind === "mailpit" ? <Mail className="size-4 text-accent-500" aria-hidden /> : info.kind === "rabbitmq" ? <Rabbit className="size-4 text-accent-500" aria-hidden /> : info.kind === "memcached" ? <MemoryStick className="size-4 text-accent-500" aria-hidden /> : isSearch(info.kind) || info.kind === "opensearch" ? <Search className="size-4 text-accent-500" aria-hidden /> : info.kind === "ollama" ? <BrainCircuit className="size-4 text-accent-500" aria-hidden /> : <Server className="size-4 text-accent-500" aria-hidden />}
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
          {info.kind === "rabbitmq" && (
            <>
              <dt className="text-muted">{t("Management UI")}</dt>
              <dd>
                {info.webUiPort ? (
                  <a href={`http://${host}:${info.webUiPort}`} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
                    http://{host}:{info.webUiPort} <ExternalLink className="size-3" />
                  </a>
                ) : (
                  <span className="text-xs text-subtle">{t("no port")}</span>
                )}
              </dd>
              <dt className="text-muted">{t("User")}</dt>
              <dd className="font-mono text-xs">{info.username}</dd>
            </>
          )}
          {info.kind === "opensearch" && info.dashboards && (
            <>
              <dt className="text-muted">OpenSearch Dashboards</dt>
              <dd className="flex flex-wrap items-center gap-2">
                {info.webUiPort ? (
                  <a href={`http://${host}:${info.webUiPort}`} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
                    http://{host}:{info.webUiPort} <ExternalLink className="size-3" />
                  </a>
                ) : (
                  <span className="text-xs text-subtle">{t("no port")}</span>
                )}
                <span className="inline-flex items-center gap-1.5 text-xs">
                  <StatusDot tone={containerStateTone(info.dashboards.state)} />
                  {info.dashboards.state}
                  {info.dashboards.health && <Badge tone={info.dashboards.health === "healthy" ? "green" : info.dashboards.health === "starting" ? "blue" : "red"}>{info.dashboards.health}</Badge>}
                </span>
              </dd>
            </>
          )}
          {info.external && (
            <>
              <dt className="text-muted">{t("Server")}</dt>
              <dd className="text-xs">{t("external – Envoryx does not run it")}</dd>
            </>
          )}
          {info.kind === "ollama" && (
            <>
              <dt className="text-muted">{t("Model store")}</dt>
              <dd className="text-xs">{t("shared by all projects")}</dd>
            </>
          )}
          {info.kind === "meilisearch" && (
            <>
              <dt className="text-muted">{t("Dashboard")}</dt>
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

        {info.kind === "rabbitmq" &&
          (creds ? (
            <dl className="divide-y divide-[var(--border)]">
              <CopyRow label={t("Password")} value={creds.password} secret />
              <CopyRow label="RABBITMQ_URL" value={creds.url} secret />
            </dl>
          ) : (
            <Button size="sm" onClick={reveal}>
              {t("Show password")}
            </Button>
          ))}

        {isSearch(info.kind) &&
          (searchCreds ? (
            <dl className="divide-y divide-[var(--border)]">
              <CopyRow label={info.kind === "meilisearch" ? t("Master key") : t("API key")} value={searchCreds.apiKey} secret />
            </dl>
          ) : (
            <Button size="sm" onClick={reveal}>
              {info.kind === "meilisearch" ? t("Show master key") : t("Show API key")}
            </Button>
          ))}

        {phpFix && (
          <Alert tone="blue">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span>{t("PHP clients need the {{ext}} extension, which is off in this project.", { ext: phpExtensions[key] })}</span>
              <Button
                size="sm"
                loading={update.isPending}
                onClick={() =>
                  update.mutate({ php: phpFix }, { onSuccess: () => onMessage({ tone: "green", text: t("PHP extension {{ext}} enabled.", { ext: phpExtensions[key] }) }), onError: (err) => fail(err, t("Saving failed")) })
                }
              >
                {t("Enable {{ext}}", { ext: phpExtensions[key] })}
              </Button>
            </div>
          </Alert>
        )}

        {info.external && (
          <Button
            size="sm"
            icon={<Pencil className="size-3.5" />}
            onClick={() => {
              setConn({ host: info.host, port: info.port, password: "" });
              setEditOpen(true);
            }}
          >
            {t("Edit connection")}
          </Button>
        )}

        {!alwaysPublished(info.kind) && !info.external && (
          <Checkbox
            label={t("Publish port on the host")}
            description={info.hostPort ? t("Reachable at {{address}}", { address: `${host}:${info.hostPort}` }) : info.kind === "redis" ? t("For desktop clients like RedisInsight.") : info.kind === "memcached" ? t("For tools on your machine, e.g. telnet or a cache inspector.") : info.kind === "typesense" || info.kind === "opensearch" || info.kind === "ollama" ? t("For clients and dashboards running on your machine.") : t("For AMQP clients running on your machine.")}
            checked={info.hostPort > 0}
            disabled={update.isPending}
            onChange={(e) => update.mutate({ [key]: { enabled: true, version: info.version, exposePort: e.target.checked } }, { onError: (err) => fail(err, t("Changing the port failed")) })}
          />
        )}

        {info.kind === "ollama" && (
          <Checkbox
            label={t("Use the GPU")}
            description={t("Hands the host's NVIDIA GPUs to Ollama. Docker needs the NVIDIA Container Toolkit for it (on Unraid: the Nvidia Driver plugin); Envoryx checks that before switching.")}
            checked={!!info.gpu}
            disabled={update.isPending}
            onChange={(e) => update.mutate({ ollama: { enabled: true, version: info.version, exposePort: info.hostPort > 0, gpu: e.target.checked } }, { onError: (err) => fail(err, t("Saving failed")) })}
          />
        )}

        {info.kind === "ollama" && <OllamaModels project={project} running={info.state === "running"} onMessage={onMessage} />}

        {info.kind === "opensearch" && (
          <Checkbox
            label="OpenSearch Dashboards"
            description={t("Web UI with the Dev Tools console, index management and Discover, on its own port. The image is about 2.6 GB and needs roughly 400 MB of RAM.")}
            checked={!!info.dashboards}
            disabled={update.isPending}
            onChange={(e) => update.mutate({ opensearch: { enabled: true, version: info.version, exposePort: info.hostPort > 0, dashboards: e.target.checked } }, { onError: (err) => fail(err, t("Saving failed")) })}
          />
        )}

        {versions.length > 1 && !info.external && (
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
          {info.external ? t("Remove connection") : info.volumeName ? t("Remove {{service}} and data", { service: title }) : t("Remove {{service}}", { service: title })}
        </Button>
      </div>

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
              disabled={!conn.host}
              loading={update.isPending}
              onClick={() =>
                update.mutate(
                  { [key]: { enabled: true, external: conn } },
                  { onSuccess: () => { setEditOpen(false); onMessage({ tone: "green", text: t("Connection saved. The application containers were recreated with it.") }); }, onError: (err) => { setEditOpen(false); fail(err, t("Saving the connection failed")); } },
                )
              }
            >
              {t("Save")}
            </Button>
          </>
        }
      >
        <ExternalRedisFields id={`edit-${info.kind}`} value={conn} onChange={setConn} passwordOptional />
      </Dialog>

      <Dialog
        open={removeOpen}
        onClose={() => setRemoveOpen(false)}
        title={info.external ? t("Remove the connection?") : t("Remove {{service}}?", { service: title })}
        description={
          info.external
            ? t("Envoryx forgets the connection to {{address}}; the server and its data are not touched. The application containers are recreated without the {{service}} variables.", { address: `${info.host}:${info.port}`, service: title })
            : info.volumeName
            ? t("This removes the container and deletes the volume {{volume}} with all data. The application containers (PHP, Python, Go, Node) are recreated without the {{service}} variables.", { volume: info.volumeName, service: title })
            : info.kind === "ollama"
              ? t("This removes the container. The application containers (PHP, Python, Go, Node) are recreated without the Ollama variables. The models stay in the shared store.")
              : t("This removes the container. The application containers (PHP, Python, Go, Node) are recreated without the {{service}} variables.", { service: title })
        }
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

function AddServiceCard({ project, kind, onMessage }: { project: Project; kind: ExtraKind; onMessage: (m: { tone: "green" | "red"; text: string }) => void }) {
  const { t } = useTranslation();
  const update = useUpdateProject(project.id);
  const runtimes = useRuntimes();
  const rt = runtimes.data?.runtimes.find((r) => r.key === kind);
  const [version, setVersion] = useState("");
  const [expose, setExpose] = useState(false);
  const [dashboards, setDashboards] = useState(false);
  const [gpu, setGpu] = useState(false);
  const [external, setExternal] = useState(false);
  const [conn, setConn] = useState<ExternalRedis>(emptyExternalRedis);
  const title = titles[kind];
  return (
    <Card>
      <CardHeader title={title} description={rt?.description ?? ""} />
      <div className="space-y-4 p-5">
        {kind === "redis" && (
          <div className="flex flex-wrap gap-4" role="radiogroup" aria-label={t("Where {{service}} runs", { service: title })}>
            <label className="inline-flex items-center gap-2 text-sm">
              <input type="radio" name="add-redis-where" checked={!external} onChange={() => setExternal(false)} />
              {t("In a container of the project")}
            </label>
            <label className="inline-flex items-center gap-2 text-sm">
              <input type="radio" name="add-redis-where" checked={external} onChange={() => setExternal(true)} />
              {t("On an external server")}
            </label>
          </div>
        )}
        {external && <ExternalRedisFields id="add-redis-ext" value={conn} onChange={setConn} />}
        {rt && rt.versions.length > 1 && !external && (
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
        {!alwaysPublished(kind) && !external && <Checkbox label={t("Publish port on the host")} checked={expose} onChange={(e) => setExpose(e.target.checked)} />}
        {kind === "opensearch" && <Checkbox label="OpenSearch Dashboards" description={t("Web UI with the Dev Tools console, index management and Discover, on its own port. The image is about 2.6 GB and needs roughly 400 MB of RAM.")} checked={dashboards} onChange={(e) => setDashboards(e.target.checked)} />}
        {kind === "ollama" && <Checkbox label={t("Use the GPU")} description={t("Hands the host's NVIDIA GPUs to Ollama. Docker needs the NVIDIA Container Toolkit for it (on Unraid: the Nvidia Driver plugin); Envoryx checks that before switching.")} checked={gpu} onChange={(e) => setGpu(e.target.checked)} />}
        <Button
          variant="primary"
          icon={<Plus className="size-4" />}
          loading={update.isPending}
          disabled={external && !conn.host}
          onClick={() =>
            update.mutate(
              // The PHP extension its clients need comes along in the same update.
              {
                [kind]: external ? { enabled: true, external: conn } : { enabled: true, version: version || undefined, exposePort: expose, ...(kind === "opensearch" ? { dashboards } : {}), ...(kind === "ollama" ? { gpu } : {}) },
                ...(phpExtensionUpdate(project, kind) ? { php: phpExtensionUpdate(project, kind)! } : {}),
              },
              { onSuccess: () => onMessage({ tone: "green", text: t("{{service}} added. The application containers were recreated with the new variables.", { service: title }) }), onError: (err) => onMessage({ tone: "red", text: errorText(err, t, t("Adding failed")) }) },
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
  const { t } = useTranslation();
  const extras = useExtraServices(project.id);
  const storage = useStorage(project.id);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  if (extras.isPending || storage.isPending) return <Spinner />;
  if (extras.isError) return <ErrorState message={errorText(extras.error, t)} />;
  const has = (k: string) => extras.data.some((s) => s.kind === k);
  return (
    <div className="space-y-6">
      <PublicHostNotice />
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <div className="grid gap-6 lg:grid-cols-2">
        {storage.data && <StorageCard project={project} onMessage={setMsg} />}
        {extras.data.map((s) => (
          <ServiceCard key={s.kind} project={project} info={s} onMessage={setMsg} />
        ))}
        {!has("redis") && <AddServiceCard project={project} kind="redis" onMessage={setMsg} />}
        {!has("memcached") && <AddServiceCard project={project} kind="memcached" onMessage={setMsg} />}
        {!has("mailpit") && <AddServiceCard project={project} kind="mailpit" onMessage={setMsg} />}
        {!has("rabbitmq") && <AddServiceCard project={project} kind="rabbitmq" onMessage={setMsg} />}
        {!has("meilisearch") && <AddServiceCard project={project} kind="meilisearch" onMessage={setMsg} />}
        {!has("typesense") && <AddServiceCard project={project} kind="typesense" onMessage={setMsg} />}
        {!has("opensearch") && <AddServiceCard project={project} kind="opensearch" onMessage={setMsg} />}
        {!has("ollama") && <AddServiceCard project={project} kind="ollama" onMessage={setMsg} />}
        {!storage.data && <AddStorageCard project={project} onMessage={setMsg} />}
      </div>
    </div>
  );
}
