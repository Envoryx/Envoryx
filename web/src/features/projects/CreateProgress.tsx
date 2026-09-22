import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOperations } from "@/api/hooks";
import { elapsedSeconds, operationStep } from "@/lib/operations";
import type { OperationAction } from "@/api/types";

/**
 * Live view of a create or duplicate operation while the dialog waits for it: the
 * server-side step (image pull with percentage, template scaffolding, file copy,
 * container start) and the time spent.
 */
export function CreateProgress({ slug, action = "create", title, hint }: { slug: string; action?: OperationAction; title?: string; hint?: string }) {
  const { t } = useTranslation();
  const ops = useOperations();
  const op = ops.data?.find((o) => o.action === action && o.projectSlug === slug && !o.finishedAt);
  const step = op ? operationStep(op, t) : "";
  // The elapsed time must move even while the server reports the same step.
  const [, tick] = useState(0);
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 1000);
    return () => window.clearInterval(id);
  }, []);
  return (
    <div className="flex items-start gap-3 rounded-lg border border-accent-500/30 bg-accent-500/5 px-4 py-3 text-sm" role="status" data-testid="create-progress">
      <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin text-accent-500" aria-hidden />
      <div className="min-w-0 flex-1">
        <p className="font-medium text-fg">{title ?? t("Creating the project…")}</p>
        <p className="mt-0.5 break-words text-xs text-muted">
          {step || t("Waiting for the server…")}
          {op && <span className="ml-1 tabular-nums text-subtle">· {t("{{seconds}} s", { seconds: elapsedSeconds(op) })}</span>}
        </p>
        <p className="mt-1 text-xs text-subtle">{hint ?? t("You can leave this page – the project keeps being created and appears in the list when done.")}</p>
      </div>
    </div>
  );
}
