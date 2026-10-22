import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSettings, useUpdateSettings } from "@/api/hooks";
import { Alert, Badge, Card, CardHeader, Checkbox, ErrorState, Field, Select, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatDateTime } from "@/lib/format";

/** The daily check for newer builds of the projects' images and the optional restart that applies them. */
export function ImageUpdatesCard() {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const [error, setError] = useState<string | null>(null);
  const info = s.data?.imageUpdates;

  const save = (next: { autoRestart: boolean; hour: number }) => {
    setError(null);
    update.mutate({ imageUpdates: next }, { onError: (err) => setError(errorText(err, t, t("Saving failed"))) });
  };

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <RefreshCw className="size-4 text-accent-500" aria-hidden />
            {t("Image updates")}
          </span>
        }
        description={t("Once a day Envoryx asks the registries whether the images your projects use have a newer build, a PHP security fix for example. A project shows it and you get a notification; the next restart applies it. Nothing changes until then, unless you let Envoryx restart the projects itself.")}
        actions={info && <Badge tone={info.autoRestart ? "green" : "gray"}>{info.autoRestart ? t("on") : t("off")}</Badge>}
      />
      <div className="space-y-3 p-5">
        {s.isPending ? (
          <Spinner />
        ) : s.isError ? (
          <ErrorState message={errorText(s.error, t)} />
        ) : info ? (
          <>
            {error && <Alert tone="red">{error}</Alert>}
            <Checkbox
              label={t("Restart the running projects with a newer image daily")}
              description={t("At the hour below, each running project whose image has a newer build is restarted, one after the other, which interrupts it for a few seconds. Stopped projects and projects rolled back to their previous image stay as they are.")}
              checked={info.autoRestart}
              disabled={update.isPending}
              onChange={(e) => save({ autoRestart: e.target.checked, hour: info.hour })}
            />
            {info.autoRestart && (
              <Field label={t("Time")} htmlFor="image-restart-hour" hint={t("Server local time")}>
                <Select id="image-restart-hour" value={info.hour} disabled={update.isPending} onChange={(e) => save({ autoRestart: true, hour: Number(e.target.value) })}>
                  {Array.from({ length: 24 }, (_, h) => (
                    <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>
                  ))}
                </Select>
              </Field>
            )}
            <p className="text-xs text-subtle">
              {info.check.checkedAt
                ? t("Last checked {{time}}, images with a newer build: {{n}}", { time: formatDateTime(info.check.checkedAt), n: info.check.newer.length })
                : t("Not checked yet: the first check runs a few minutes after Envoryx starts.")}
            </p>
            {info.check.error && <Alert tone="amber">{info.check.error}</Alert>}
          </>
        ) : null}
      </div>
    </Card>
  );
}
