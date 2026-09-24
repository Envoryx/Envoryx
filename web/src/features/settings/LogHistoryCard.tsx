import { useMutation, useQueryClient } from "@tanstack/react-query";
import { History, Save, Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { api } from "@/api/client";
import { keys, useSettings, useUpdateSettings } from "@/api/hooks";
import type { Settings } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Dialog, ErrorState, Field, Input, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatBytes } from "@/lib/format";

/** Keeps container output beyond the containers: on/off, retention and the space it uses. */
export function LogHistoryCard() {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const qc = useQueryClient();
  const info = s.data?.logHistory;
  const [days, setDays] = useState("");
  const [maxMb, setMaxMb] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);

  useEffect(() => {
    if (info) {
      setDays(String(info.retentionDays));
      setMaxMb(String(info.maxMb));
    }
  }, [info?.retentionDays, info?.maxMb]);

  const clear = useMutation({
    mutationFn: api.clearLogHistory,
    onSuccess: (res) => {
      qc.setQueryData<Settings>(keys.settings, (old) => (old ? { ...old, logHistory: res.logHistory } : old));
      setConfirmClear(false);
    },
    onError: (err) => {
      setConfirmClear(false);
      setError(errorText(err, t, t("Deleting failed")));
    },
  });

  const save = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);
    update.mutate(
      { logHistory: { retentionDays: Number(days), maxMb: Number(maxMb) } },
      { onSuccess: () => setSaved(true), onError: (err) => setError(errorText(err, t, t("Saving failed"))) },
    );
  };

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <History className="size-4 text-accent-500" aria-hidden />
            {t("Log history")}
          </span>
        }
        description={t("Envoryx copies the output of the project containers into daily files, so the Logs tab can still search it after a container was restarted or recreated. Finished days are compressed.")}
        actions={info && <Badge tone={info.enabled && info.available ? "green" : "gray"}>{info.enabled && info.available ? t("on") : t("off")}</Badge>}
      />
      <div className="space-y-4 p-5">
        {s.isPending ? (
          <Spinner />
        ) : s.isError ? (
          <ErrorState message={errorText(s.error, t)} />
        ) : !info ? null : (
          <>
            {error && <Alert tone="red">{error}</Alert>}
            {!info.available && <Alert tone="amber">{t("The log history directory could not be opened; see the Envoryx log. The Logs tab reads the containers only.")}</Alert>}
            <Checkbox
              label={t("Keep the output of the project containers")}
              description={t("When off, nothing new is stored and the Logs tab reads what Docker still holds for the current container (10 MB × 3 per container). What is stored stays until retention removes it.")}
              checked={info.enabled}
              disabled={update.isPending || !info.available}
              onChange={(e) => {
                setError(null);
                update.mutate({ logHistory: { enabled: e.target.checked } }, { onError: (err) => setError(errorText(err, t, t("Saving failed"))) });
              }}
            />
            <form onSubmit={save} className="grid gap-4 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
              <Field label={t("Keep for (days)")} htmlFor="lh-days" hint={t("1 to 365")}>
                <Input id="lh-days" type="number" min={1} max={365} value={days} onChange={(e) => setDays(e.target.value)} required />
              </Field>
              <Field label={t("At most (MB)")} htmlFor="lh-max" hint={t("All projects together; the oldest days go first")}>
                <Input id="lh-max" type="number" min={50} value={maxMb} onChange={(e) => setMaxMb(e.target.value)} required />
              </Field>
              <Button type="submit" loading={update.isPending} icon={<Save className="size-4" />} className="sm:mb-5">
                {t("Save")}
              </Button>
            </form>
            {saved && <p className="text-xs text-emerald-600 dark:text-emerald-400">{t("Saved. Retention is applied within the hour.")}</p>}
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-default px-4 py-3 text-sm">
              <div className="space-y-0.5">
                <p className="text-fg">
                  {t("{{size}} in {{files}} files", { size: formatBytes(info.usage.bytes), files: info.usage.files })}
                  {info.usage.oldest && <span className="text-muted"> · {t("since {{date}}", { date: new Date(info.usage.oldest).toLocaleDateString() })}</span>}
                </p>
                <p className="text-xs text-subtle">
                  {t("Reading {{n}} containers now", { n: info.following })}
                  {info.dir && (
                    <>
                      {" · "}
                      <Code>{info.dir}</Code>
                    </>
                  )}
                </p>
              </div>
              <Button variant="ghost" size="sm" onClick={() => setConfirmClear(true)} disabled={info.usage.files === 0} icon={<Trash2 className="size-3.5" />}>
                {t("Delete stored logs")}
              </Button>
            </div>
          </>
        )}
      </div>
      <Dialog
        open={confirmClear}
        onClose={() => setConfirmClear(false)}
        title={t("Delete the log history?")}
        description={t("Every stored line of every project is deleted. What Docker still holds for the running containers stays visible, but is not collected again.")}
        footer={
          <>
            <Button onClick={() => setConfirmClear(false)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={clear.isPending} onClick={() => clear.mutate()} icon={<Trash2 className="size-4" />}>
              {t("Delete")}
            </Button>
          </>
        }
      />
    </Card>
  );
}
