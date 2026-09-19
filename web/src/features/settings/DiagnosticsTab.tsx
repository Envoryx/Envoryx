import { AlertTriangle, CheckCircle2, ExternalLink, Info, RefreshCw, XCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Link, useNavigate } from "react-router-dom";
import { useDiagnostics, useUpdateSettings } from "@/api/hooks";
import type { DiagnosticCheck } from "@/api/types";
import { Button, Card, CardHeader, ErrorState, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

const categoryTitle: Record<DiagnosticCheck["category"], string> = {
  runtime: "Docker & storage",
  network: "Network & links",
  security: "Security",
  maintenance: "Maintenance",
};

const DOCS = "https://github.com/envoryx/envoryx/blob/main/DEPLOYMENT.md";

function StatusIcon({ status }: { status: DiagnosticCheck["status"] }) {
  switch (status) {
    case "ok":
      return <CheckCircle2 className="size-5 shrink-0 text-emerald-600 dark:text-emerald-400" aria-label="ok" />;
    case "info":
      return <Info className="size-5 shrink-0 text-sky-600 dark:text-sky-400" aria-label="info" />;
    case "warning":
      return <AlertTriangle className="size-5 shrink-0 text-amber-600 dark:text-amber-400" aria-label="warning" />;
    case "error":
      return <XCircle className="size-5 shrink-0 text-red-600 dark:text-red-400" aria-label="error" />;
  }
}

/** Summary line for the tab label and the dashboard: "all good" or the counts that matter. */
export function diagnosticsSummary(t: TFunction, s: { warning: number; error: number }): string {
  if (s.error === 0 && s.warning === 0) return t("Everything looks good");
  const parts: string[] = [];
  if (s.error > 0) parts.push(t("{{count}} errors", { count: s.error }));
  if (s.warning > 0) parts.push(t("{{count}} warnings", { count: s.warning }));
  return parts.join(", ");
}

function CheckRow({ check, onAction, busy }: { check: DiagnosticCheck; onAction: (c: DiagnosticCheck) => void; busy: boolean }) {
  const { t } = useTranslation();
  const needsAttention = check.status === "warning" || check.status === "error";
  return (
    <li className="flex gap-3 px-5 py-3">
      <StatusIcon status={check.status} />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{t(check.title)}</p>
        {check.detail && <p className="mt-0.5 break-words text-xs text-muted">{check.detail}</p>}
        {check.hint && (needsAttention || check.status === "info") && <p className={`mt-1 text-xs ${needsAttention ? "text-fg" : "text-subtle"}`}>{t(check.hint)}</p>}
        {(check.action || check.docs) && (
          <div className="mt-2 flex flex-wrap items-center gap-3">
            {check.action && (
              <Button size="sm" variant={needsAttention ? "primary" : "ghost"} loading={busy} onClick={() => onAction(check)}>
                {check.action.kind === "setPublicHost" ? t("Use {{host}}", { host: check.action.value }) : t(check.action.label ?? "")}
              </Button>
            )}
            {check.docs && (
              <a href={`${DOCS}#${check.docs}`} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-xs text-accent-600 hover:underline dark:text-accent-300">
                <ExternalLink className="size-3" aria-hidden />
                {t("Documentation")}
              </a>
            )}
          </div>
        )}
      </div>
    </li>
  );
}

export function DiagnosticsTab({ onSwitchTab }: { onSwitchTab: (tab: string) => void }) {
  const { t } = useTranslation();
  const q = useDiagnostics();
  const update = useUpdateSettings();
  const navigate = useNavigate();

  const act = (c: DiagnosticCheck) => {
    if (!c.action) return;
    switch (c.action.kind) {
      case "setPublicHost":
        update.mutate({ publicHost: c.action.value });
        break;
      case "settingsTab":
        onSwitchTab(c.action.value);
        break;
      case "link":
        void navigate(c.action.value);
        break;
    }
  };

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={q.error.message} />;
  const d = q.data;
  const groups = (["runtime", "network", "security", "maintenance"] as const).map((cat) => ({ cat, checks: d.checks.filter((c) => c.category === cat) })).filter((g) => g.checks.length > 0);
  const allGood = d.summary.error === 0 && d.summary.warning === 0;

  return (
    <div className="space-y-6">
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-4 px-5 py-4">
          <div className="flex items-center gap-3">
            {allGood ? <CheckCircle2 className="size-8 text-emerald-600 dark:text-emerald-400" aria-hidden /> : <AlertTriangle className="size-8 text-amber-600 dark:text-amber-400" aria-hidden />}
            <div>
              <p className="text-base font-semibold">{diagnosticsSummary(t, d.summary)}</p>
              <p className="text-xs text-muted">
                {t("{{count}} checks", { count: d.checks.length })} · {t("checked {{date}}", { date: formatDateTime(d.at) })}
                {d.summary.info > 0 && <> · {t("{{count}} notes", { count: d.summary.info })}</>}
              </p>
            </div>
          </div>
          <Button onClick={() => void q.refetch()} loading={q.isFetching} icon={<RefreshCw className="size-4" />}>
            {t("Check again")}
          </Button>
        </div>
        {!allGood && (
          <p className="border-t border-default px-5 py-3 text-xs text-muted">
            {t("Each finding says what is wrong and how to fix it. Fixes with a button apply immediately; the others point to the setting or the documentation.")}
          </p>
        )}
      </Card>
      {groups.map((g) => (
        <Card key={g.cat}>
          <CardHeader title={t(categoryTitle[g.cat])} />
          <ul className="divide-y divide-[var(--border)]">
            {g.checks.map((c) => (
              <CheckRow key={c.id} check={c} onAction={act} busy={update.isPending} />
            ))}
          </ul>
        </Card>
      ))}
      <p className="text-xs text-subtle">
        {t("Something not covered here?")}{" "}
        <Link to="/docker" className="underline">
          {t("Docker page")}
        </Link>{" "}
        {t("shows containers and orphaned resources; the")}{" "}
        <a href={DOCS} target="_blank" rel="noopener noreferrer" className="underline">
          {t("deployment guide")}
        </a>{" "}
        {t("covers the rest.")}
      </p>
    </div>
  );
}
