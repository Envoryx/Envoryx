import { AlertTriangle, CheckCircle2, ExternalLink, Info, RefreshCw, XCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Link, useNavigate } from "react-router-dom";
import { useEffect, useState } from "react";
import { useDiagnostics, useSettings, useUpdateSettings } from "@/api/hooks";
import type { DiagnosticCheck, Settings } from "@/api/types";
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

/** The probe URL the browser fetches: same scheme as the UI (no mixed content), proxy ports. */
export function probeUrl(s: Settings): string | null {
  if (!s.proxy?.enabled || !s.baseDomain) return null;
  const https = window.location.protocol === "https:";
  if (https && !s.proxy.tls) return null;
  const port = https ? s.proxy.httpsPort : s.proxy.httpPort;
  const direct = !!s.proxy.address;
  const suffix = direct || !port || port === (https ? 443 : 80) ? "" : `:${port}`;
  return `${https ? "https" : "http"}://envoryx-diagnostics-probe.${s.baseDomain}${suffix}/`;
}

type ProbeState = { status: "checking" | "ok" | "warning" | "skipped"; detail?: string };

/** Fetches the proxy probe from this browser: proves wildcard DNS + proxy reachability (+ CA trust over HTTPS). */
function useBrowserProbe(settings: Settings | undefined, runId: number): ProbeState {
  const [state, setState] = useState<ProbeState>({ status: "checking" });
  useEffect(() => {
    if (!settings) return;
    const url = probeUrl(settings);
    if (!url) {
      setState({ status: "skipped" });
      return;
    }
    let cancelled = false;
    setState({ status: "checking" });
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 5000);
    fetch(url, { signal: ctrl.signal, cache: "no-store", mode: "cors" })
      .then(async (res) => {
        const body = (await res.json().catch(() => null)) as { envoryx?: string } | null;
        if (cancelled) return;
        setState(body?.envoryx === "probe" ? { status: "ok", detail: url } : { status: "warning", detail: url });
      })
      .catch(() => !cancelled && setState({ status: "warning", detail: url }))
      .finally(() => clearTimeout(timer));
    return () => {
      cancelled = true;
      ctrl.abort();
    };
  }, [settings, runId]);
  return state;
}

function BrowserProbeRow({ probe, onSwitchTab }: { probe: ProbeState; onSwitchTab: (tab: string) => void }) {
  const { t } = useTranslation();
  const https = window.location.protocol === "https:";
  if (probe.status === "skipped") return null;
  const status: DiagnosticCheck["status"] = probe.status === "checking" ? "info" : probe.status;
  return (
    <li className="flex gap-3 px-5 py-3">
      {probe.status === "checking" ? <RefreshCw className="size-5 shrink-0 animate-spin text-muted" aria-label="checking" /> : <StatusIcon status={status} />}
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{t("Project domains from this browser")}</p>
        {probe.status === "checking" && <p className="mt-0.5 text-xs text-muted">{t("Contacting {{url}} …", { url: probe.detail ?? "" })}</p>}
        {probe.status === "ok" && <p className="mt-0.5 break-words text-xs text-muted">{t("Wildcard DNS and the proxy work from this device{{tls}}.", { tls: https ? t(", and the CA is trusted") : "" })}</p>}
        {probe.status === "warning" && (
          <>
            <p className="mt-0.5 break-words text-xs text-muted">{t("{{url}} could not be reached from this browser.", { url: probe.detail ?? "" })}</p>
            <p className="mt-1 text-xs text-fg">
              {https
                ? t("Either this device's DNS does not resolve names under the base domain, or this browser does not trust the Envoryx CA yet. Install the CA (Domains & HTTPS) and add a wildcard DNS rewrite on the DNS server your devices use (AdGuard Home, Pi-hole, router).")
                : t("This device's DNS does not resolve names under the base domain. Add a wildcard DNS rewrite on the DNS server your devices use (AdGuard Home, Pi-hole, router), or hosts-file entries per project.")}
            </p>
            <div className="mt-2 flex flex-wrap items-center gap-3">
              <Button size="sm" variant="primary" onClick={() => onSwitchTab("domains")}>
                {t("Domains & HTTPS")}
              </Button>
              <a href={`${DOCS}#names`} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-xs text-accent-600 hover:underline dark:text-accent-300">
                <ExternalLink className="size-3" aria-hidden />
                {t("Documentation")}
              </a>
            </div>
          </>
        )}
      </div>
    </li>
  );
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
  const settings = useSettings();
  const update = useUpdateSettings();
  const navigate = useNavigate();
  const [probeRun, setProbeRun] = useState(0);
  const probe = useBrowserProbe(settings.data, probeRun);

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
  // The browser probe counts like a server-side warning.
  const summary = { ...d.summary, warning: d.summary.warning + (probe.status === "warning" ? 1 : 0) };
  const allGood = summary.error === 0 && summary.warning === 0;

  return (
    <div className="space-y-6">
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-4 px-5 py-4">
          <div className="flex items-center gap-3">
            {allGood ? <CheckCircle2 className="size-8 text-emerald-600 dark:text-emerald-400" aria-hidden /> : <AlertTriangle className="size-8 text-amber-600 dark:text-amber-400" aria-hidden />}
            <div>
              <p className="text-base font-semibold">{diagnosticsSummary(t, summary)}</p>
              <p className="text-xs text-muted">
                {t("{{count}} checks", { count: d.checks.length })} · {t("checked {{date}}", { date: formatDateTime(d.at) })}
                {d.summary.info > 0 && <> · {t("{{count}} notes", { count: d.summary.info })}</>}
              </p>
            </div>
          </div>
          <Button
            onClick={() => {
              setProbeRun((n) => n + 1);
              void q.refetch();
            }}
            loading={q.isFetching}
            icon={<RefreshCw className="size-4" />}
          >
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
            {g.cat === "network" && <BrowserProbeRow probe={probe} onSwitchTab={onSwitchTab} />}
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
