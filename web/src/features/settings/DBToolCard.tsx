import { TableProperties } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { useDBTool, useSetDBTool } from "@/api/hooks";
import { Alert, Badge, Card, CardHeader, Checkbox, ErrorState, Spinner } from "@/components/ui";

/** Opt-in for the shared Adminer container that opens project databases in the browser. */
export function DBToolCard() {
  const { t } = useTranslation();
  const q = useDBTool();
  const set = useSetDBTool();
  const [error, setError] = useState<string | null>(null);

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <TableProperties className="size-4 text-accent-500" aria-hidden />
            {t("Database browser")}
          </span>
        }
        description={t("Open project databases in the browser with Adminer: one small container shared by all projects, started on first use, logged in automatically and reachable only through your Envoryx session.")}
        actions={
          q.data && (
            <Badge tone={q.data.running ? "green" : q.data.enabled ? "gray" : "gray"}>{q.data.running ? t("running") : q.data.enabled ? t("starts on first use") : t("off")}</Badge>
          )
        }
      />
      <div className="space-y-3 p-5">
        {q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorState message={q.error.message} />
        ) : (
          <>
            {error && <Alert tone="red">{error}</Alert>}
            <Checkbox
              label={t("Enable the database browser")}
              description={t("MariaDB, MySQL and PostgreSQL. Turning it off removes the container. Image: {{image}}.", { image: q.data.image })}
              checked={q.data.enabled}
              disabled={set.isPending}
              onChange={(e) => {
                setError(null);
                set.mutate(e.target.checked, { onError: (err) => setError(err instanceof ApiError ? err.message : t("Saving failed")) });
              }}
            />
          </>
        )}
      </div>
    </Card>
  );
}
