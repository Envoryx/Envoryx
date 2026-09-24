import { CloudDownload, CloudUpload, Lock, RefreshCw } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import type { OffsiteTargetName, OffsiteUpload, RemoteBackup } from "@/api/types";
import { Alert, Badge, Button, Select, Spinner } from "@/components/ui";
import { formatBytes, formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";

/** Whether any copy is still on its way – lists poll while this is true. */
export function uploading(copies: Record<string, OffsiteUpload[]> | undefined): boolean {
  return Object.values(copies ?? {}).some((list) => list.some((c) => c.status === "pending" || c.status === "running"));
}

/** One badge per target a backup was copied to (or is on its way to). */
export function OffsiteBadges({ copies }: { copies: OffsiteUpload[] | undefined }) {
  const { t } = useTranslation();
  if (!copies?.length) return null;
  return (
    <>
      {copies.map((c) => {
        const retry = c.status === "failed" && c.nextAttemptAt ? t("next attempt {{date}}", { date: formatDateTime(c.nextAttemptAt) }) : "";
        const title = c.status === "failed" ? [c.error, retry].filter(Boolean).join(" · ") : c.status === "done" ? `${formatBytes(c.sizeBytes)} · ${formatDateTime(c.updatedAt)}` : undefined;
        return (
          <span key={c.targetId} title={title}>
            <Badge tone={c.status === "done" ? "green" : c.status === "failed" ? "red" : "amber"}>
              <CloudUpload className="mr-1 inline size-3" aria-hidden />
              {c.targetName}
              {" · "}
              {c.status === "done" ? t("copied") : c.status === "failed" ? t("failed") : c.status === "running" ? t("uploading…") : t("queued")}
            </Badge>
          </span>
        );
      })}
    </>
  );
}

/** A button that copies a backup to every enabled target (again, after a failure). */
export function OffsiteUploadButton({ targets, copies, onUpload, busy }: { targets: OffsiteTargetName[] | undefined; copies: OffsiteUpload[] | undefined; onUpload: () => void; busy: boolean }) {
  const { t } = useTranslation();
  const enabled = (targets ?? []).filter((x) => x.enabled);
  if (enabled.length === 0) return null;
  const done = enabled.every((x) => copies?.some((c) => c.targetId === x.id && (c.status === "done" || c.status === "pending" || c.status === "running")));
  if (done) return null;
  return (
    <Button size="sm" variant="ghost" loading={busy} onClick={onUpload} icon={<CloudUpload className="size-3.5" />} title={enabled.map((x) => x.name).join(", ")}>
      {t("Copy offsite")}
    </Button>
  );
}

/**
 * The backups on one target, with a button that fetches a copy into the local backups.
 * `local` names the copies that are there already (they need no fetching).
 */
export function RemoteBackups({
  targets,
  queryKey,
  load,
  local,
  onFetch,
  fetching,
}: {
  targets: OffsiteTargetName[];
  queryKey: readonly unknown[];
  load: (targetId: string) => Promise<{ backups: RemoteBackup[] }>;
  local: (b: RemoteBackup) => boolean;
  onFetch: (targetId: string, b: RemoteBackup) => void;
  fetching: string | null;
}) {
  const { t } = useTranslation();
  const [targetId, setTargetId] = useState(targets[0]?.id ?? "");
  const [open, setOpen] = useState(false);
  const q = useQuery({ queryKey: [...queryKey, targetId], queryFn: () => load(targetId), enabled: open && targetId !== "", retry: false });

  if (!targets.length) return null;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {targets.length > 1 && (
          <div className="w-64 max-w-full">
            <Select aria-label={t("Offsite target")} value={targetId} onChange={(e) => setTargetId(e.target.value)}>
              {targets.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.name}
                </option>
              ))}
            </Select>
          </div>
        )}
        <Button size="sm" onClick={() => (open ? void q.refetch() : setOpen(true))} loading={q.isFetching} icon={<RefreshCw className="size-3.5" />}>
          {open ? t("Refresh") : targets.length > 1 ? t("Show copies") : t("Show copies on {{name}}", { name: targets[0]?.name ?? "" })}
        </Button>
      </div>
      {open &&
        (q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <Alert tone="red">{errorText(q.error, t)}</Alert>
        ) : q.data.backups.length === 0 ? (
          <p className="text-sm text-muted">{t("Nothing on this target yet.")}</p>
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {q.data.backups.map((b) => {
              const here = local(b);
              return (
                <li key={b.key} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm">
                  <div>
                    <p className="flex flex-wrap items-center gap-2 font-medium">
                      {formatDateTime(b.createdAt)}
                      <Badge>{b.kind}</Badge>
                      {b.source === "scheduled" && <Badge tone="blue">{t("scheduled")}</Badge>}
                      {b.encrypted && (
                        <span title={t("encrypted")}>
                          <Lock className="size-3.5 text-muted" aria-label={t("encrypted")} />
                        </span>
                      )}
                    </p>
                    <p className="font-mono text-[11px] text-subtle">
                      {b.id} · {formatBytes(b.sizeBytes)}
                    </p>
                  </div>
                  {here ? (
                    <span className="text-xs text-muted">{t("also here")}</span>
                  ) : (
                    <Button size="sm" loading={fetching === b.key} disabled={fetching !== null} onClick={() => onFetch(targetId, b)} icon={<CloudDownload className="size-3.5" />}>
                      {t("Fetch")}
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        ))}
    </div>
  );
}
