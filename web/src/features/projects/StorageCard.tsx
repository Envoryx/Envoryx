import { Cloud, ExternalLink, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { usePublicHost, useRuntimes, useStorage, useUpdateProject } from "@/api/hooks";
import type { Project, StorageInfo } from "@/api/types";
import { Badge, Button, Card, CardHeader, Checkbox, Dialog, Field, Input, Select, StatusDot } from "@/components/ui";
import { containerStateTone } from "@/lib/format";
import { CopyButton, CopyRow } from "./DatabaseTab";

type Message = { tone: "green" | "red"; text: string };

function laravelEnv(info: StorageInfo): string {
  return [
    "FILESYSTEM_DISK=s3",
    `AWS_ACCESS_KEY_ID=${info.accessKey ?? "…"}`,
    `AWS_SECRET_ACCESS_KEY=${info.secretKey ?? "…"}`,
    `AWS_DEFAULT_REGION=${info.region}`,
    `AWS_BUCKET=${info.bucket}`,
    `AWS_ENDPOINT=${info.endpoint}`,
    "AWS_USE_PATH_STYLE_ENDPOINT=true",
    `AWS_URL=${info.publicUrl}`,
  ].join("\n");
}

function awsCli(info: StorageInfo, host: string): string {
  return `AWS_ACCESS_KEY_ID=${info.accessKey ?? "…"} AWS_SECRET_ACCESS_KEY=${info.secretKey ?? "…"} aws --endpoint-url http://${host}:${info.hostPort} s3 ls s3://${info.bucket}/`;
}

export function StorageCard({ project, onMessage }: { project: Project; onMessage: (m: Message) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const storage = useStorage(project.id);
  const update = useUpdateProject(project.id);
  const publicHost = usePublicHost();
  const host = publicHost || window.location.hostname;
  const [creds, setCreds] = useState<StorageInfo | null>(null);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [snippet, setSnippet] = useState<"laravel" | "cli">("laravel");
  const invalidate = () => void qc.invalidateQueries({ queryKey: ["projects", project.id, "storage"] });
  const fail = (err: unknown, fallback: string) => onMessage({ tone: "red", text: err instanceof ApiError ? err.message : fallback });

  const setPublic = useMutation({
    mutationFn: (publicRead: boolean) => api.storage.setPublic(project.id, publicRead),
    onSuccess: invalidate,
    onError: (err) => fail(err, t("Changing the bucket policy failed")),
  });
  const reveal = async () => {
    try {
      setCreds((await api.storage.credentials(project.id)).storage);
    } catch (err) {
      fail(err, t("Loading the credentials failed"));
    }
  };

  if (storage.isPending || storage.isError || !storage.data) return null;
  const info = creds ?? storage.data;
  const live = storage.data;

  return (
    <Card className="lg:col-span-2">
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Cloud className="size-4 text-accent-500" aria-hidden />
            {t("Object storage (S3)")} · RustFS {live.version}
          </span>
        }
        description={t("S3-compatible storage with its own bucket. Applications reach it as {{endpoint}}; the browser through {{publicUrl}}.", { endpoint: live.endpoint, publicUrl: live.publicUrl || `http://${host}:${live.hostPort}/${live.bucket}` })}
        actions={
          <span className="inline-flex items-center gap-1.5 text-xs">
            <StatusDot tone={containerStateTone(live.state)} />
            {live.state}
            {live.health && <Badge tone={live.health === "healthy" ? "green" : live.health === "starting" ? "blue" : "red"}>{live.health}</Badge>}
          </span>
        }
      />
      <div className="grid gap-6 p-5 lg:grid-cols-2">
        <div>
          <dl className="divide-y divide-[var(--border)]">
            <CopyRow label={t("Endpoint (from app)")} value={live.endpoint} />
            <CopyRow label={t("Endpoint (from host)")} value={`http://${host}:${live.hostPort}`} />
            <CopyRow label={t("Public URL")} value={live.publicUrl || `http://${host}:${live.hostPort}/${live.bucket}`} />
            <CopyRow label={t("Bucket")} value={live.bucket} />
            <CopyRow label={t("Region")} value={live.region} />
            {creds ? (
              <>
                <CopyRow label={t("Access key")} value={creds.accessKey ?? ""} />
                <CopyRow label={t("Secret key")} value={creds.secretKey ?? ""} secret />
              </>
            ) : (
              <div className="py-2">
                <Button size="sm" onClick={reveal}>
                  {t("Show access keys")}
                </Button>
              </div>
            )}
          </dl>
          <p className="mt-3 text-xs text-muted">
            {t("Injected into the containers:")} <span className="font-mono">{live.injectedEnv.join(", ")}</span>
          </p>
          <div className="mt-3 flex flex-wrap items-center gap-3">
            <a href={`http://${host}:${live.consolePort}${live.consolePath}`} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-sm text-accent-600 hover:underline dark:text-accent-300">
              <ExternalLink className="size-3.5" aria-hidden />
              {t("Open console")}
            </a>
            <span className="text-xs text-muted">{t("Sign in with the access keys.")}</span>
          </div>
        </div>
        <div className="space-y-4">
          <Checkbox
            label={t("Anyone may read objects (public bucket)")}
            description={t("A bucket policy makes every object readable without credentials – what public-read ACLs do on real providers, which this server does not evaluate. Off: only presigned URLs and authenticated requests work.")}
            checked={live.publicRead}
            disabled={setPublic.isPending}
            onChange={(e) => setPublic.mutate(e.target.checked)}
          />
          <div>
            <div className="mb-1 flex items-center justify-between">
              <div className="flex gap-1">
                <Button size="sm" variant={snippet === "laravel" ? "primary" : "ghost"} onClick={() => setSnippet("laravel")}>
                  Laravel .env
                </Button>
                <Button size="sm" variant={snippet === "cli" ? "primary" : "ghost"} onClick={() => setSnippet("cli")}>
                  aws-cli
                </Button>
              </div>
              <CopyButton value={snippet === "laravel" ? laravelEnv(info) : awsCli(info, host)} label={t("snippet")} />
            </div>
            <pre className="overflow-x-auto rounded-md bg-muted p-3 font-mono text-[11px]">{snippet === "laravel" ? laravelEnv(info) : awsCli(info, host)}</pre>
            {!creds && <p className="mt-1 text-xs text-muted">{t("Show the access keys to fill in the placeholders.")}</p>}
            <p className="mt-2 text-xs text-muted">
              {t("Presigned URLs for the browser must be signed against the public endpoint (S3_PUBLIC_ENDPOINT); the signature covers the host name. Laravel's Storage::url() uses AWS_URL.")}
            </p>
          </div>
          <div className="flex justify-end">
            <Button variant="ghost" size="sm" icon={<Trash2 className="size-3.5" />} onClick={() => setRemoveOpen(true)}>
              {t("Remove object storage")}
            </Button>
          </div>
        </div>
      </div>
      <Dialog
        open={removeOpen}
        onClose={() => setRemoveOpen(false)}
        title={t("Remove object storage?")}
        footer={
          <>
            <Button onClick={() => setRemoveOpen(false)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              disabled={confirm !== live.bucket}
              loading={update.isPending}
              onClick={() =>
                update.mutate(
                  { storage: { enabled: false, removeData: true } },
                  {
                    onSuccess: () => {
                      setRemoveOpen(false);
                      invalidate();
                      onMessage({ tone: "green", text: t("Object storage removed. PHP was recreated without the variables.") });
                    },
                    onError: (err) => fail(err, t("Removing failed")),
                  },
                )
              }
            >
              {t("Delete bucket and remove")}
            </Button>
          </>
        }
      >
        <p className="text-sm">{t("All objects in the bucket are deleted with the volume. Type the bucket name to confirm.")}</p>
        <div className="mt-3">
          <Field label={t("Bucket name")} htmlFor="storage-remove-confirm">
            <Input id="storage-remove-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={live.bucket} />
          </Field>
        </div>
      </Dialog>
    </Card>
  );
}

export function AddStorageCard({ project, onMessage }: { project: Project; onMessage: (m: Message) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const update = useUpdateProject(project.id);
  const runtimes = useRuntimes();
  const rt = runtimes.data?.runtimes.find((r) => r.key === "rustfs");
  const [version, setVersion] = useState("");
  const [publicRead, setPublicRead] = useState(true);
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Cloud className="size-4 text-accent-500" aria-hidden />
            {t("Object storage (S3)")}
          </span>
        }
        description={rt?.description ?? t("S3-compatible object storage with a bucket for this project and a web console.")}
      />
      <div className="space-y-4 p-5">
        {rt && rt.versions.length > 1 && (
          <Field label={t("Version")} htmlFor="add-storage-version">
            <Select id="add-storage-version" value={version || rt.versions.find((v) => v.default)?.version || ""} onChange={(e) => setVersion(e.target.value)}>
              {rt.versions.map((v) => (
                <option key={v.version} value={v.version}>
                  {v.label}
                </option>
              ))}
            </Select>
          </Field>
        )}
        <Checkbox label={t("Anyone may read objects (public bucket)")} description={t("Can be changed later.")} checked={publicRead} onChange={(e) => setPublicRead(e.target.checked)} />
        <Button
          variant="primary"
          icon={<Plus className="size-4" />}
          loading={update.isPending}
          onClick={() =>
            update.mutate(
              { storage: { enabled: true, publicRead, ...(version ? { version } : {}) } },
              {
                onSuccess: () => {
                  void qc.invalidateQueries({ queryKey: ["projects", project.id, "storage"] });
                  onMessage({ tone: "green", text: t("Object storage added. PHP was recreated with the new variables.") });
                },
                onError: (err) => onMessage({ tone: "red", text: err instanceof ApiError ? err.message : t("Adding failed") }),
              },
            )
          }
        >
          {t("Add object storage")}
        </Button>
      </div>
    </Card>
  );
}
