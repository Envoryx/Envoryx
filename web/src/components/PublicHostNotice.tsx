import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { useSettings, useUpdateSettings } from "@/api/hooks";
import { Alert, Button } from "@/components/ui";

/**
 * Shown when Envoryx has an IP of its own (Unraid br0, macvlan) and no public host is
 * configured: links to published project ports would point at Envoryx's address, where
 * nothing listens. Offers the Docker host the daemon reports as a one-click fix.
 */
export function PublicHostNotice({ className }: { className?: string }) {
  const { t } = useTranslation();
  const settings = useSettings();
  const update = useUpdateSettings();
  if (!settings.data?.publicHostNeeded) return null;
  const suggestion = settings.data.publicHostSuggestion;
  const candidate = suggestion?.ip || suggestion?.hostname || "";
  return (
    <div className={className}>
      <Alert tone="amber" title={t("Project links need a host")}>
        <p>
          {t("Envoryx runs with its own IP ({{address}}), but projects publish their ports on the Docker host. Links to published ports – project URLs, Mailpit, the object storage console, database ports – point at the wrong address until the Docker host is set.", { address: settings.data.proxy.address })}
        </p>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          {candidate && (
            <Button size="sm" variant="primary" loading={update.isPending} onClick={() => update.mutate({ publicHost: candidate })}>
              {t("Use {{host}}", { host: candidate })}
            </Button>
          )}
          {suggestion?.ip && suggestion.hostname && <span className="text-xs text-muted">{t("({{hostname}} as reported by Docker)", { hostname: suggestion.hostname })}</span>}
          <Link to="/settings?tab=general" className="text-sm underline">
            {t("Set it in Settings")}
          </Link>
        </div>
      </Alert>
    </div>
  );
}
