import { Power } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useSettings, useUpdateSettings } from "@/api/hooks";
import { Alert, Badge, Card, CardHeader, Checkbox, ErrorState, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** Opt-in that ties the project containers to the Envoryx container's own lifecycle. */
export function LifecycleCard() {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const [error, setError] = useState<string | null>(null);

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Power className="size-4 text-accent-500" aria-hidden />
            {t("Projects and the Envoryx container")}
          </span>
        }
        description={t("By default the project containers are independent of Envoryx: stopping or updating Envoryx leaves them running. Tie them together for maintenance windows, so stopping the Envoryx container takes the whole stack down.")}
        actions={s.data && <Badge tone={s.data.projectsFollowEnvoryx ? "green" : "gray"}>{s.data.projectsFollowEnvoryx ? t("on") : t("off")}</Badge>}
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
              label={t("Stop projects with Envoryx and start them again when it comes back")}
              description={t("When the Envoryx container is stopped, every running project is stopped as well (a restart from within Envoryx does not). On the next start, the projects that were running come back automatically – also after a reboot of the host. Give the Envoryx container a stop timeout that covers all projects.")}
              checked={s.data.projectsFollowEnvoryx ?? false}
              disabled={update.isPending}
              onChange={(e) => {
                setError(null);
                update.mutate({ projectsFollowEnvoryx: e.target.checked }, { onError: (err) => setError(errorText(err, t, t("Saving failed"))) });
              }}
            />
          </>
        )}
      </div>
    </Card>
  );
}
