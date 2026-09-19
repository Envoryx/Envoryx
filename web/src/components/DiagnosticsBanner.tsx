import { AlertTriangle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { useDiagnostics } from "@/api/hooks";
import { Alert } from "@/components/ui";
import { diagnosticsSummary } from "@/features/settings/DiagnosticsTab";

/** Dashboard banner: one line when the diagnostics found something to fix. */
export function DiagnosticsBanner() {
  const { t } = useTranslation();
  const q = useDiagnostics();
  if (!q.data || (q.data.summary.warning === 0 && q.data.summary.error === 0)) return null;
  const first = q.data.checks.filter((c) => c.status === "error" || c.status === "warning").slice(0, 3);
  return (
    <div className="mb-6">
      <Alert tone={q.data.summary.error > 0 ? "red" : "amber"} title={t("Set-up check: {{summary}}", { summary: diagnosticsSummary(t, q.data.summary) })}>
        <ul className="mt-1 list-disc pl-4 text-sm">
          {first.map((c) => (
            <li key={c.id}>{t(c.title)}</li>
          ))}
        </ul>
        <Link to="/settings" className="mt-2 inline-flex items-center gap-1 text-sm underline">
          <AlertTriangle className="size-3.5" aria-hidden />
          {t("Open diagnostics")}
        </Link>
      </Alert>
    </div>
  );
}
