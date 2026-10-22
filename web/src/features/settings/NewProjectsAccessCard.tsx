import { Lock } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { usePlanAllows, useSettingLocked, useSettings, useUpdateSettings } from "@/api/hooks";
import { LockedHint } from "@/features/plan/PlanCard";
import { Alert, Badge, Card, CardHeader, Checkbox, ErrorState, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** Whether new projects start behind the Envoryx sign-in (Domains → Rules of each project). */
export function NewProjectsAccessCard() {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const locked = useSettingLocked("newProjectsPrivate");
  const publicAllowed = usePlanAllows("publicProjects");
  const [error, setError] = useState<string | null>(null);
  const on = !publicAllowed || (s.data?.newProjectsPrivate ?? false);

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Lock className="size-4 text-accent-500" aria-hidden />
            {t("Who sees new projects")}
          </span>
        }
        description={t("A project's addresses are open to everyone who knows them, unless its rules let in only people signed in to Envoryx. On a server on the internet, start new projects that way; each project can still change it under Domains → Rules.")}
        actions={s.data && <Badge tone={on ? "green" : "gray"}>{on ? t("signed-in only") : t("open")}</Badge>}
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
              label={t("New projects only let in people signed in to Envoryx")}
              description={t("Others can be let in with a guest link. Projects that exist keep their rules.")}
              checked={on}
              disabled={update.isPending || locked || !publicAllowed}
              onChange={(e) => {
                setError(null);
                update.mutate({ newProjectsPrivate: e.target.checked }, { onError: (err) => setError(errorText(err, t, t("Saving failed"))) });
              }}
            />
            {!publicAllowed ? <p className="text-xs text-muted">{t("Your hoster's plan keeps every project behind the sign-in.")}</p> : locked && <LockedHint />}
          </>
        )}
      </div>
    </Card>
  );
}
