import { clsx } from "clsx";
import { AlertCircle, AlertTriangle, Bot, Info, X } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

type Tone = "red" | "amber" | "blue" | "accent";

const icons: Record<Tone, typeof Info> = { red: AlertCircle, amber: AlertTriangle, blue: Info, accent: Bot };
const iconTone: Record<Tone, string> = {
  red: "text-red-500",
  amber: "text-amber-500",
  blue: "text-accent-500",
  accent: "text-accent-500",
};

/**
 * One entry of the dashboard's notices. The rows share one box (see Notices), so a page
 * with several things to say shows one calm list instead of a stack of coloured alerts.
 */
export function NoticeRow({ tone, title, children, onDismiss }: { tone: Tone; title: ReactNode; children?: ReactNode; onDismiss?: () => void }) {
  const { t } = useTranslation();
  const Icon = icons[tone];
  return (
    <div className="flex items-start gap-3 px-4 py-3 text-sm" role={tone === "red" || tone === "amber" ? "alert" : "status"}>
      <Icon className={clsx("mt-0.5 size-4 shrink-0", iconTone[tone])} aria-hidden />
      <div className="min-w-0 flex-1">
        <p className="font-medium text-fg">{title}</p>
        {children && <div className="mt-0.5 text-muted">{children}</div>}
      </div>
      {onDismiss && (
        <button type="button" onClick={onDismiss} className="rounded p-0.5 text-muted hover:text-fg" aria-label={t("Dismiss")}>
          <X className="size-4" aria-hidden />
        </button>
      )}
    </div>
  );
}

/** The box around the notices; it disappears when none of them has anything to say. */
export function Notices({ children }: { children: ReactNode }) {
  return <div className="mb-6 divide-y divide-[var(--border)] rounded-xl border border-default bg-elevated empty:hidden">{children}</div>;
}
