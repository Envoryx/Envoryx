import { Lock } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { Suspension } from "@/api/types";
import { usePlan } from "@/api/hooks";
import { Button } from "@/components/ui";
import { useAuth } from "@/features/auth/AuthContext";
import { LogoFull } from "@/layout/Logo";
import { formatDateTime } from "@/lib/format";

/**
 * What a suspended instance shows instead of everything else: the hoster's message. The
 * plan is asked again every half minute, so the page goes away by itself once the
 * hoster lifts the suspension.
 */
export function SuspendedPage({ suspension }: { suspension: Suspension }) {
  const { t } = useTranslation();
  const { logout } = useAuth();
  usePlan(30 * 1000);
  return (
    <div className="flex min-h-full items-center justify-center bg-app px-4">
      <div className="w-full max-w-lg">
        <div className="mb-8 flex justify-center">
          <LogoFull className="w-96 max-w-full" />
        </div>
        <div className="space-y-4 rounded-xl border border-default bg-elevated p-6 shadow-sm">
          <h1 className="flex items-center gap-2 text-lg font-semibold text-fg">
            <Lock className="size-5 text-amber-500" aria-hidden />
            {t("This instance is suspended")}
          </h1>
          {suspension.message ? (
            <p className="whitespace-pre-wrap break-words rounded-md border border-default bg-app p-4 text-sm text-fg">{suspension.message}</p>
          ) : (
            <p className="text-sm text-fg">{t("Your hoster has suspended this instance. Please contact them.")}</p>
          )}
          <p className="text-sm text-muted">
            {t("Your projects are stopped and your data is kept. Once the suspension is lifted, you can start the projects again.")}
          </p>
          <div className="flex items-center justify-between gap-3 pt-2">
            <span className="text-xs text-subtle">{t("Suspended since {{time}}", { time: formatDateTime(suspension.since) })}</span>
            <Button onClick={() => void logout()}>{t("Sign out")}</Button>
          </div>
        </div>
      </div>
    </div>
  );
}
