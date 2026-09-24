import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Activity, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "@/api/client";
import { keys, useSettings, useUpdateSettings } from "@/api/hooks";
import type { Settings } from "@/api/types";
import { Alert, Button, Card, CardHeader, Dialog, ErrorState, Field, Select, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";

const choices = [7, 30, 90, 365];

/** How long the resource history of the projects is kept, and a way to delete it. */
export function ResourceHistoryCard() {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const qc = useQueryClient();
  const info = s.data?.metrics;
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);

  const clear = useMutation({
    mutationFn: api.clearMetrics,
    onSuccess: (res) => {
      qc.setQueryData<Settings>(keys.settings, (old) => (old ? { ...old, metrics: res.metrics } : old));
      void qc.invalidateQueries({ queryKey: ["projects"] });
      void qc.invalidateQueries({ queryKey: ["metrics-overview"] });
      setConfirmClear(false);
    },
    onError: (err) => {
      setConfirmClear(false);
      setError(errorText(err, t, t("Deleting failed")));
    },
  });

  const label = (d: number) => (d === 365 ? t("1 year") : t("{{n}} days", { n: d }));
  const options = info && !choices.includes(info.retentionDays) ? [...choices, info.retentionDays].sort((a, b) => a - b) : choices;

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Activity className="size-4 text-accent-500" aria-hidden />
            {t("Resource history")}
          </span>
        }
        description={t("Envoryx records CPU, memory, network and disk I/O of every running project container once a minute, and the disk space of each project once an hour. After a day the values are averaged over 5 minutes, after a week over an hour.")}
      />
      <div className="space-y-4 p-5">
        {s.isPending ? (
          <Spinner />
        ) : s.isError ? (
          <ErrorState message={errorText(s.error, t)} />
        ) : !info ? null : (
          <>
            {error && <Alert tone="red">{error}</Alert>}
            <div className="max-w-xs">
              <Field label={t("Keep for")} htmlFor="rh-days" hint={t("Older values are removed within the hour.")}>
                <Select
                  id="rh-days"
                  value={String(info.retentionDays)}
                  disabled={update.isPending}
                  onChange={(e) => {
                    setError(null);
                    setSaved(false);
                    update.mutate({ metricsRetentionDays: Number(e.target.value) }, { onSuccess: () => setSaved(true), onError: (err) => setError(errorText(err, t, t("Saving failed"))) });
                  }}
                >
                  {options.map((d) => (
                    <option key={d} value={d}>
                      {label(d)}
                    </option>
                  ))}
                </Select>
              </Field>
            </div>
            {saved && <p className="text-xs text-emerald-600 dark:text-emerald-400">{t("Saved.")}</p>}
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-default px-4 py-3 text-sm">
              <p className="text-fg">{t("{{samples}} samples and {{sizes}} disk space measurements stored", { samples: info.samples.toLocaleString(), sizes: info.sizes.toLocaleString() })}</p>
              <Button variant="ghost" size="sm" onClick={() => setConfirmClear(true)} disabled={info.samples === 0 && info.sizes === 0} icon={<Trash2 className="size-3.5" />}>
                {t("Delete resource history")}
              </Button>
            </div>
          </>
        )}
      </div>
      <Dialog
        open={confirmClear}
        onClose={() => setConfirmClear(false)}
        title={t("Delete the resource history?")}
        description={t("The recorded values of every project are deleted. Recording goes on from the next minute.")}
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
