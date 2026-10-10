import type { ReactNode } from "react";
import { Lock, Package } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { FleetInfo, PlanFeature } from "@/api/types";
import { usePlan, usePlanAllows } from "@/api/hooks";
import { Alert, Badge, Card, CardHeader, StatusDot } from "@/components/ui";
import { translateMessage } from "@/lib/errors";
import { formatBytes, formatDateTime } from "@/lib/format";

/** Display names of the features a plan can switch off (translated where shown). */
export const planFeatureLabels: Record<PlanFeature, string> = {
  addons: "Addons",
  customImages: "Custom images",
  branchEnvironments: "Branch environments",
  externalServices: "External services",
  offsite: "Offsite targets",
  ideGateway: "IDE gateway",
};

const runtimeLabels: Record<string, string> = { php: "PHP", node: "Node.js", python: "Python", go: "Go", ruby: "Ruby", java: "Java", dotnet: ".NET" };

/**
 * The hoster's plan of a managed instance: what it includes and how much of it is used.
 * Nothing shows on an instance without a plan.
 */
export function PlanCard() {
  const { t } = useTranslation();
  const q = usePlan();
  const plan = q.data?.plan;
  const fleet = q.data?.fleet;
  if (!plan) return fleet ? <Card><div className="p-5"><FleetLine fleet={fleet} /></div></Card> : null;
  const usage = q.data?.usage;
  const diskLimit = (plan.limits.diskGb ?? 0) * 2 ** 30;
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Package className="size-4 text-accent-500" aria-hidden />
            {t("Plan")}
            {plan.name && <Badge tone="blue">{plan.name}</Badge>}
          </span>
        }
        description={t("Your hoster sets what this instance includes. To get more, ask them for another plan.")}
      />
      <div className="space-y-5 p-5">
        {fleet && <FleetLine fleet={fleet} />}
        <div className="grid gap-5 sm:grid-cols-3">
          <Quota label={t("Projects")} used={usage?.projects ?? 0} limit={plan.limits.projects ?? 0} format={String} />
          <Quota label={t("Users")} used={usage?.users ?? 0} limit={plan.limits.users ?? 0} format={String} />
          <Quota
            label={t("Disk space")}
            used={usage?.diskBytes ?? 0}
            limit={diskLimit}
            format={formatBytes}
            note={usage?.diskMeasuredAt ? t("measured {{time}}", { time: formatDateTime(usage.diskMeasuredAt) }) : diskLimit > 0 ? t("not measured yet") : undefined}
          />
        </div>
        <dl className="grid gap-x-8 gap-y-3 text-sm sm:grid-cols-[12rem_1fr]">
          <dt className="text-muted">{t("Runtimes")}</dt>
          <dd className="text-fg">{plan.runtimes.length === 0 ? t("All runtimes") : plan.runtimes.map((r) => runtimeLabels[r] ?? r).join(", ")}</dd>
          <dt className="text-muted">{t("Not included")}</dt>
          <dd className="flex flex-wrap gap-1.5">
            {plan.disabled.length === 0 ? <span className="text-fg">{t("Nothing")}</span> : plan.disabled.map((f) => <Badge key={f}>{t(planFeatureLabels[f])}</Badge>)}
          </dd>
          {plan.lockedSettings.length > 0 && (
            <>
              <dt className="text-muted">{t("Set by your hoster")}</dt>
              <dd className="flex flex-wrap gap-1.5">
                {plan.lockedSettings.map((k) => (
                  <Badge key={k}>
                    <Lock className="mr-1 inline size-3" aria-hidden />
                    {t(settingLabels[k] ?? k)}
                  </Badge>
                ))}
              </dd>
            </>
          )}
        </dl>
      </div>
    </Card>
  );
}

/** Which fleet manager runs the instance and whether it is in touch. */
function FleetLine({ fleet }: { fleet: FleetInfo }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-2">
      <p className="flex flex-wrap items-center gap-2 text-sm">
        <StatusDot tone={fleet.connected ? "green" : "amber"} />
        <span className="text-fg">{fleet.name ? t("Managed by your hoster as {{name}}", { name: fleet.name }) : t("Managed by your hoster")}</span>
        <span className="font-mono text-xs text-subtle">{fleet.url}</span>
        <Badge tone={fleet.connected ? "green" : "amber"}>{fleet.connected ? t("connected") : t("not connected")}</Badge>
        {!fleet.connected && fleet.lastContact && <span className="text-xs text-muted">{t("last contact {{time}}", { time: formatDateTime(fleet.lastContact) })}</span>}
      </p>
      {fleet.error && <Alert tone="amber">{translateMessage(fleet.error, t)}</Alert>}
    </div>
  );
}

/** Names of the settings a plan can fix, as the settings pages call them. */
const settingLabels: Record<string, string> = {
  publicHost: "Public host",
  baseDomain: "Base domain",
  forceHttps: "Force HTTPS",
  projectsFollowEnvoryx: "Projects follow Envoryx",
  sharedIdeBackends: "Shared IDE backends",
  sharedPackageCache: "Shared package cache",
  xdebugClientHost: "Xdebug client host",
  folderViewFolder: "FolderView3 folder",
  metricsRetentionDays: "Resource history",
};

function Quota({ label, used, limit, format, note }: { label: string; used: number; limit: number; format: (n: number) => string; note?: string | undefined }) {
  const { t } = useTranslation();
  const pct = limit > 0 ? Math.min(100, (used / limit) * 100) : 0;
  const full = limit > 0 && used >= limit;
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-2 text-sm">
        <span className="text-muted">{label}</span>
        <span className={`tabular-nums ${full ? "text-red-600 dark:text-red-400" : "text-fg"}`}>
          {limit > 0 ? t("{{used}} of {{limit}}", { used: format(used), limit: format(limit) }) : t("{{used}}, no limit", { used: format(used) })}
        </span>
      </div>
      {limit > 0 && (
        <div className="h-1.5 w-full rounded-full bg-[color-mix(in_oklab,var(--series-1)_15%,transparent)]" aria-hidden>
          <div className={`h-full rounded-full ${full ? "bg-red-500" : "bg-[var(--series-1)]"}`} style={{ width: `${Math.max(2, pct)}%` }} />
        </div>
      )}
      {note && <p className="text-xs text-subtle">{note}</p>}
    </div>
  );
}

/** Shown in place of a feature the plan leaves out. */
export function NotInPlan({ feature }: { feature: PlanFeature }) {
  const { t } = useTranslation();
  return (
    <Alert tone="gray" title={t("Not included in your plan")}>
      {t("This instance's plan leaves out {{feature}}. Your hoster can include it.", { feature: t(planFeatureLabels[feature]) })}
    </Alert>
  );
}

/** Marks a setting the hoster's plan fixes. */
export function LockedHint() {
  const { t } = useTranslation();
  return (
    <p className="mt-1 flex items-center gap-1 text-xs text-muted">
      <Lock className="size-3" aria-hidden />
      {t("Set by your hoster")}
    </p>
  );
}

/** Renders its children only when the plan includes the feature. */
export function IfPlanAllows({ feature, children }: { feature: PlanFeature; children: ReactNode }) {
  return usePlanAllows(feature) ? <>{children}</> : null;
}
