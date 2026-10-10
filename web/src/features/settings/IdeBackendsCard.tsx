import { MonitorCog } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSettingLocked, useSettings, useUpdateSettings } from "@/api/hooks";
import { LockedHint } from "@/features/plan/PlanCard";
import { Alert, Badge, Card, CardHeader, Checkbox, ErrorState, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** Opt-in that shares the JetBrains Gateway backends between projects. */
export function IdeBackendsCard() {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const locked = useSettingLocked("sharedIdeBackends");
  const [error, setError] = useState<string | null>(null);

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <MonitorCog className="size-4 text-accent-500" aria-hidden />
            {t("JetBrains backends")}
          </span>
        }
        description={t("JetBrains Gateway downloads an IDE backend of about 1 GB into a project the first time you open it there. Each project keeps its own; shared, a backend is downloaded once for all projects.")}
        actions={s.data && <Badge tone={s.data.sharedIdeBackends ? "green" : "gray"}>{s.data.sharedIdeBackends ? t("on") : t("off")}</Badge>}
      />
      <div className="space-y-3 p-5">
        {s.isPending ? (
          <Spinner />
        ) : s.isError ? (
          <ErrorState message={errorText(s.error, t)} />
        ) : (
          <>
            {error && <Alert tone="red">{error}</Alert>}
            <Checkbox
              label={t("Share the IDE backends between projects")}
              description={t("Only when you trust everyone who works on a project here: every project with Gateway can change the shared backends, so a developer of one project could change the IDE another project runs. Projects with Gateway pick the change up when they are restarted.")}
              checked={s.data.sharedIdeBackends ?? false}
              disabled={update.isPending || locked}
              onChange={(e) => {
                setError(null);
                update.mutate({ sharedIdeBackends: e.target.checked }, { onError: (err) => setError(errorText(err, t, t("Saving failed"))) });
              }}
            />
            {locked && <LockedHint />}
          </>
        )}
      </div>
    </Card>
  );
}
