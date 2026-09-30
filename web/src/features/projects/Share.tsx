import { Globe, Square } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Project, ProjectShare } from "@/api/types";
import { Alert, Badge, Button, Dialog, Field, Select } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { projectAccess } from "@/lib/access";
import { formatDateTime } from "@/lib/format";
import { CopyButton } from "./DatabaseTab";

const durations = [15, 60, 240, 1440] as const;

/** The project's share state; the badge and the dialog read the same cached query. */
export function useShare(project: Project) {
  return useQuery({ queryKey: ["projects", project.id, "share"], queryFn: async () => (await api.share.get(project.id)).share, refetchInterval: (query) => (query.state.data?.state === "starting" ? 2000 : 30000) });
}

/** Shown in the project header only while the project is shared; opens the share dialog. */
export function SharedBadge({ project, onOpen }: { project: Project; onOpen: () => void }) {
  const { t } = useTranslation();
  if (!useShare(project).data?.active) return null;
  return (
    <Button size="md" variant="secondary" onClick={onOpen} icon={<Globe className="size-4 text-emerald-500" />} title={t("Share on a public address")}>
      {t("Shared")}
    </Button>
  );
}

/** Puts the project on a temporary public address (Cloudflare quick tunnel) and shows it. */
export function ShareDialog({ project, open, onClose }: { project: Project; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const key = ["projects", project.id, "share"];
  const running = project.status.state === "running";
  // Ending a share needs operate, starting one admin.
  const canStart = projectAccess(project).admin;
  const q = useShare(project);
  const [minutes, setMinutes] = useState<number>(60);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (open) setError(null);
  }, [open]);
  const start = useMutation({
    mutationFn: async () => (await api.share.start(project.id, minutes)).share,
    onSuccess: (share) => qc.setQueryData(key, share),
    onError: (err) => setError(errorText(err, t, t("Sharing failed"))),
  });
  const stop = useMutation({
    mutationFn: () => api.share.stop(project.id),
    onSuccess: () => qc.setQueryData<ProjectShare>(key, { active: false }),
    onError: (err) => setError(errorText(err, t, t("Ending the share failed"))),
  });
  const share = q.data;
  const shared = !!share?.active;
  const label = (m: number) => (m < 60 ? t("{{n}} minutes", { n: m }) : m === 1440 ? t("24 hours") : t("{{n}} hours", { n: m / 60 }));

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("Share {{name}}", { name: project.name })}
      description={t("A temporary public https address through a Cloudflare quick tunnel - no account and no port forwarding. The address is random and changes with every share.")}
      footer={
        shared ? (
          <>
            <Button onClick={onClose}>{t("Close")}</Button>
            <Button variant="danger" icon={<Square className="size-4" />} loading={stop.isPending} onClick={() => stop.mutate()}>
              {t("End the share")}
            </Button>
          </>
        ) : (
          <>
            <Button onClick={onClose}>{t("Cancel")}</Button>
            {canStart && (
              <Button variant="primary" icon={<Globe className="size-4" />} loading={start.isPending} disabled={!running} onClick={() => { setError(null); start.mutate(); }}>
                {t("Share publicly")}
              </Button>
            )}
          </>
        )
      }
    >
      <div className="space-y-4">
        {error && <Alert tone="red">{error}</Alert>}
        {shared && share ? (
          <>
            {share.url ? (
              <div className="flex items-center gap-2 rounded-md border border-default px-3 py-2">
                <a href={share.url} target="_blank" rel="noopener noreferrer" className="min-w-0 flex-1 truncate font-mono text-sm text-accent-600 hover:underline dark:text-accent-300">
                  {share.url}
                </a>
                <CopyButton value={share.url} label={t("public address")} />
              </div>
            ) : (
              <p className="text-sm text-muted">
                <Badge tone={share.state === "stopped" ? "red" : "blue"}>{share.state === "stopped" ? t("tunnel stopped") : t("starting")}</Badge> {share.message}
              </p>
            )}
            {share.expiresAt && <p className="text-sm text-muted">{t("Public until {{time}}; it also ends when the project stops.", { time: formatDateTime(share.expiresAt) })}</p>}
          </>
        ) : (
          canStart && (
          <Field label={t("Share for")} htmlFor="share-duration">
            <Select id="share-duration" value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
              {durations.map((m) => (
                <option key={m} value={m}>
                  {label(m)}
                </option>
              ))}
            </Select>
          </Field>
          )
        )}
        <Alert tone="amber">{t("Anyone who has the address reaches the project from the internet, without signing in. Share work in progress, not data you have to protect.")}</Alert>
      </div>
    </Dialog>
  );
}
