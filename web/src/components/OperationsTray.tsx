import { clsx } from "clsx";
import { CheckCircle2, Loader2, X, XCircle } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { keys, useOperations } from "@/api/hooks";
import type { Operation } from "@/api/types";
import { elapsedSeconds, operationDone, operationStep, operationTitle, operationVerb } from "@/lib/operations";
import { translateMessage } from "@/lib/errors";

const SUCCESS_VISIBLE_MS = 8000;

/**
 * Bottom-right stack of what Envoryx is doing right now: every running project operation
 * with its current step, and the outcome once it finished. Successes fade after a few
 * seconds, failures stay until dismissed. Operations that were already over when the page
 * loaded are not announced again.
 */
export function OperationsTray() {
  const { t } = useTranslation();
  const ops = useOperations();
  const [dismissed, setDismissed] = useState<Set<string>>(() => new Set());
  const seeded = useRef(false);
  const [, tick] = useState(0);
  const qc = useQueryClient();
  const wasRunning = useRef<Set<string>>(new Set());

  // An operation that just finished changed the project – refresh lists and details right
  // away instead of waiting for their own poll. The mutation that started it may belong to
  // a page the user has left, or to another browser altogether.
  useEffect(() => {
    if (!ops.data) return;
    const running = new Set(ops.data.filter((o) => !o.finishedAt).map((o) => o.id));
    const finished = [...wasRunning.current].some((id) => !running.has(id));
    wasRunning.current = running;
    if (finished) {
      void qc.invalidateQueries({ queryKey: keys.projects });
      void qc.invalidateQueries({ queryKey: keys.dashboard });
      void qc.invalidateQueries({ queryKey: keys.docker });
    }
  }, [ops.data, qc]);

  // Elapsed-time counters need a re-render every second while something runs.
  const running = ops.data?.some((o) => !o.finishedAt) ?? false;
  useEffect(() => {
    if (!running) return;
    const id = window.setInterval(() => tick((n) => n + 1), 1000);
    return () => window.clearInterval(id);
  }, [running]);

  useEffect(() => {
    if (!ops.data) return;
    if (!seeded.current) {
      seeded.current = true;
      const old = ops.data.filter((o) => o.finishedAt).map((o) => o.id);
      if (old.length > 0) setDismissed((d) => new Set([...d, ...old]));
      return;
    }
    // Successes disappear on their own; failures wait for the user.
    const timers = ops.data
      .filter((o) => o.finishedAt && !o.error && !dismissed.has(o.id))
      .map((o) => {
        const left = SUCCESS_VISIBLE_MS - (Date.now() - Date.parse(o.finishedAt!));
        return window.setTimeout(() => setDismissed((d) => new Set([...d, o.id])), Math.max(0, left));
      });
    return () => timers.forEach((id) => window.clearTimeout(id));
  }, [ops.data, dismissed]);

  const visible = (ops.data ?? []).filter((o) => !dismissed.has(o.id));
  if (visible.length === 0) return null;

  return (
    <div className="pointer-events-none fixed inset-x-4 bottom-4 z-30 flex flex-col items-end gap-2 sm:inset-x-auto sm:right-4 sm:w-96" role="status" aria-live="polite">
      {visible.map((op) => (
        <OperationCard key={op.id} op={op} onDismiss={() => setDismissed((d) => new Set([...d, op.id]))} t={t} />
      ))}
    </div>
  );
}

function OperationCard({ op, onDismiss, t }: { op: Operation; onDismiss: () => void; t: ReturnType<typeof useTranslation>["t"] }) {
  const failed = !!op.error;
  const finished = !!op.finishedAt;
  return (
    <div
      className={clsx(
        "pointer-events-auto w-full rounded-lg border bg-elevated p-3 text-sm shadow-lg",
        failed ? "border-red-500/40" : finished ? "border-emerald-500/40" : "border-default",
      )}
      data-testid="operation"
    >
      <div className="flex items-start gap-2.5">
        {failed ? (
          <XCircle className="mt-0.5 size-4 shrink-0 text-red-500" aria-hidden />
        ) : finished ? (
          <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-500" aria-hidden />
        ) : (
          <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin text-accent-500" aria-hidden />
        )}
        <div className="min-w-0 flex-1">
          <p className="font-medium text-fg">{failed ? t("{{title}} failed", { title: operationTitle(op, t) }) : finished ? operationDone(op, t) : operationTitle(op, t)}</p>
          {failed ? (
            <p className="mt-0.5 break-words text-xs text-red-600 dark:text-red-400">{translateMessage(op.error, t)}</p>
          ) : finished ? null : (
            <p className="mt-0.5 break-words text-xs text-muted">
              {operationStep(op, t) || t("Working…")}
              <span className="ml-1 tabular-nums text-subtle">· {t("{{seconds}} s", { seconds: elapsedSeconds(op) })}</span>
            </p>
          )}
        </div>
        {finished && (
          <button onClick={onDismiss} className="-m-1 rounded p-1 text-muted hover:bg-muted hover:text-fg" aria-label={t("Dismiss")}>
            <X className="size-4" aria-hidden />
          </button>
        )}
      </div>
    </div>
  );
}

/** Inline "Starting… · Pulling the image …" line for a project that has an operation running. */
export function OperationHint({ op, className }: { op: Operation | undefined; className?: string }) {
  const { t } = useTranslation();
  if (!op) return null;
  const step = operationStep(op, t);
  return (
    <span className={clsx("inline-flex min-w-0 items-center gap-1.5 text-xs text-accent-600 dark:text-accent-300", className)} data-testid="operation-hint">
      <Loader2 className="size-3 shrink-0 animate-spin" aria-hidden />
      <span className="truncate">
        {operationVerb(op.action, t)}
        {step && <span className="text-muted"> · {step}</span>}
      </span>
    </span>
  );
}
