import { useEffect, useState } from "react";
import { Lock } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link, useLocation } from "react-router-dom";
import { api, ApiError } from "@/api/client";
import { Spinner } from "@/components/ui";
import { LogoFull } from "@/layout/Logo";
import { errorText } from "@/lib/errors";

/**
 * Where the proxy sends a browser that wants an addon's web UI or a dev server without
 * having signed in there yet (/gate?to=<address>). Signed in to Envoryx, the user is
 * passed on with a one-time code, or told why not.
 */
export function GatePage() {
  const { t } = useTranslation();
  const location = useLocation();
  const to = new URLSearchParams(location.search).get("to") ?? "";
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    api.auth.gate(to).then(
      ({ url }) => live && window.location.replace(url),
      (err) => {
        if (!live) return;
        if (err instanceof ApiError && err.code === "not_gated") setError(t("This address isn't behind the Envoryx sign-in."));
        else if (err instanceof ApiError && err.status === 403) setError(t("Your role in this project doesn't let you open this page."));
        else setError(errorText(err, t, t("The page could not be opened.")));
      },
    );
    return () => {
      live = false;
    };
  }, [to, t]);

  if (!error) return <Spinner label={t("Opening the page…")} />;
  return (
    <div className="flex min-h-full items-center justify-center bg-app px-4">
      <div className="w-full max-w-lg">
        <div className="mb-8 flex justify-center">
          <LogoFull className="w-96 max-w-full" />
        </div>
        <div className="space-y-4 rounded-xl border border-default bg-elevated p-6 shadow-sm">
          <h1 className="flex items-center gap-2 text-lg font-semibold text-fg">
            <Lock className="size-5 text-amber-500" aria-hidden />
            {t("The page could not be opened.")}
          </h1>
          <p className="text-sm text-fg">{error}</p>
          <p className="break-all font-mono text-xs text-subtle">{to}</p>
          <Link to="/projects" className="inline-block text-sm text-accent-500 hover:underline">
            {t("Back to projects")}
          </Link>
        </div>
      </div>
    </div>
  );
}
