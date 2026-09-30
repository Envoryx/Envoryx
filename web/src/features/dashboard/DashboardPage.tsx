import { Link } from "react-router-dom";
import { ActivityNotice } from "@/components/ActivityNotice";
import { NoticeRow, Notices } from "@/components/NoticeRow";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/features/auth/AuthContext";
import { ResourceOverviewCard } from "./ResourceOverviewCard";
import { Plus, ArrowRight } from "lucide-react";
import { useDashboard, useDiagnostics } from "@/api/hooks";
import { Button, Card, CardHeader, EmptyState, ErrorState, LinkButton, PageHeader, Spinner, StatusDot } from "@/components/ui";
import { formatBytes, formatPercent } from "@/lib/format";
import type { Dashboard } from "@/api/types";
import type { ReactNode } from "react";
import { ProjectRow } from "@/features/projects/ProjectsPage";
import { diagnosticsSummary } from "@/features/settings/DiagnosticsTab";
import { errorText, translateMessage } from "@/lib/errors";

/** Everything that wants attention, in one box: set-up check, update, inconsistencies, what Envoryx did alone. */
function DashboardNotices({ d, admin }: { d: Dashboard; admin: boolean }) {
  const { t } = useTranslation();
  const diagnostics = useDiagnostics(admin);
  const summary = diagnostics.data?.summary;
  const findings = diagnostics.data?.checks.filter((c) => c.status === "error" || c.status === "warning").slice(0, 3) ?? [];
  return (
    <Notices>
      {admin && summary && summary.warning + summary.error > 0 && (
        <NoticeRow tone={summary.error > 0 ? "red" : "amber"} title={t("Set-up check: {{summary}}", { summary: diagnosticsSummary(t, summary) })}>
          {findings.map((c) => t(c.title)).join(" · ")}{" "}
          <Link to="/settings" className="underline">
            {t("Open diagnostics")}
          </Link>
        </NoticeRow>
      )}
      {d.update?.available && (
        <NoticeRow tone="blue" title={t("Envoryx {{version}} is available", { version: d.update.latest })}>
          {t("You are running {{current}}. Update the container to get the new version.", { current: d.update.current })}{" "}
          {d.update.url && (
            <a href={d.update.url} target="_blank" rel="noopener noreferrer" className="underline">
              {t("Release notes")}
            </a>
          )}
        </NoticeRow>
      )}
      {/* Admins find this in the set-up check above. */}
      {!admin && d.hostPath.error && !Object.keys(d.hostPath.overrides).length && (
        <NoticeRow tone="red" title={t("Host paths could not be detected")}>
          {t("Envoryx needs to know the host paths behind /projects and /config to mount project files into containers. Set ENVORYX_PROJECTS_HOST_PATH and ENVORYX_CONFIG_HOST_PATH.")} ({translateMessage(d.hostPath.error, t)})
        </NoticeRow>
      )}
      {d.issues.length > 0 && (
        <NoticeRow tone="amber" title={t("{{count}} inconsistencies detected", { count: d.issues.length })}>
          <ul className="space-y-0.5">
            {d.issues.map((i, idx) => (
              <li key={idx}>
                <Link to={`/projects/${i.projectId}`} className="font-medium text-fg underline-offset-2 hover:underline">
                  {i.projectName}
                </Link>
                : {translateMessage(i.message, t)}
              </li>
            ))}
          </ul>
        </NoticeRow>
      )}
      <ActivityNotice activity={d.activity} />
    </Notices>
  );
}

function SystemRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-muted">{label}</dt>
      <dd className="text-right tabular-nums">{children}</dd>
    </div>
  );
}

/** Docker, CPU, memory and disk at a glance; the Docker page has the details (admins only). */
function SystemCard({ d, admin }: { d: Dashboard; admin: boolean }) {
  const { t } = useTranslation();
  return (
    <Card>
      <CardHeader
        title={t("System")}
        actions={
          admin ? (
            <Link to="/docker" className="inline-flex items-center gap-1 text-xs font-medium text-accent-600 hover:underline dark:text-accent-300">
              Docker <ArrowRight className="size-3" />
            </Link>
          ) : undefined
        }
      />
      <dl className="space-y-3 px-5 py-4 text-sm">
        <SystemRow label={t("Docker engine")}>
          <span className="inline-flex items-center gap-2 font-medium">
            <StatusDot tone={d.docker.connected ? "green" : "red"} />
            {d.docker.connected ? d.docker.serverVersion : t("Unreachable")}
          </span>
        </SystemRow>
        {!d.docker.connected && <p className="text-xs text-red-500">{translateMessage(d.docker.error, t)}</p>}
        {d.docker.connected && (
          <>
            <SystemRow label={t("CPU")}>
              {formatPercent(d.stats?.cpuPercent ?? 0)}
              {d.docker.ncpu ? <span className="text-muted"> · {t("{{count}} cores", { count: d.docker.ncpu })}</span> : null}
            </SystemRow>
            <SystemRow label={t("Memory")}>
              {formatBytes(d.stats?.memoryBytes ?? 0)}
              {d.docker.memTotal ? <span className="text-muted"> {t("of {{total}}", { total: formatBytes(d.docker.memTotal) })}</span> : null}
            </SystemRow>
            <SystemRow label={t("All containers")}>{t("{{running}} / {{total}} running", { running: d.docker.running, total: d.docker.containers })}</SystemRow>
            {admin && d.orphans > 0 && (
              <SystemRow label={t("Orphaned resources")}>
                <Link to="/docker" className="text-amber-600 underline-offset-2 hover:underline dark:text-amber-400">
                  {d.orphans}
                </Link>
              </SystemRow>
            )}
          </>
        )}
      </dl>
      {d.storage && d.storage.length > 0 && (
        <div className="border-t border-default px-5 py-4">
          <p className="text-xs font-medium text-subtle" title={t("Free space on the filesystems behind /config, /projects and /backups. A backup is refused when it would fill the disk.")}>
            {t("Disk space")}
          </p>
          <ul className="mt-2 space-y-3">
            {d.storage.map((s) => {
              const used = s.totalBytes > 0 ? Math.min(100, Math.round(((s.totalBytes - s.freeBytes) / s.totalBytes) * 100)) : 0;
              return (
                <li key={s.path} className="text-xs">
                  <div className="flex items-center justify-between gap-3">
                    <span className="truncate font-mono">{s.path}</span>
                    <span className={s.low ? "shrink-0 font-medium text-red-500" : "shrink-0 text-muted"}>{t("{{free}} free", { free: formatBytes(s.freeBytes) })}</span>
                  </div>
                  <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={used} aria-valuemin={0} aria-valuemax={100} aria-label={s.path}>
                    <div className={s.low ? "h-full bg-red-500" : "h-full bg-accent-500"} style={{ width: `${used}%` }} />
                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </Card>
  );
}

export function DashboardPage() {
  const { t } = useTranslation();
  // Only an admin of the whole instance creates projects.
  const admin = useAuth().admin;
  const q = useDashboard();

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} action={<Button onClick={() => void q.refetch()}>{t("Retry")}</Button>} />;
  const d = q.data;

  return (
    <div>
      <PageHeader
        title={t("Dashboard")}
        description={t("Overview of your development environments.")}
        actions={
          admin ? (
            <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
              {t("New project")}
            </LinkButton>
          ) : undefined
        }
      />

      <DashboardNotices d={d} admin={admin} />

      <div className="grid grid-cols-[minmax(0,1fr)] gap-6 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader
            title={t("Projects")}
            description={
              d.projects.total > 0
                ? [t("{{running}} of {{total}} running", { running: d.projects.running, total: d.projects.total }), d.projects.attention ? t("{{count}} need attention", { count: d.projects.attention }) : ""].filter(Boolean).join(" · ")
                : undefined
            }
            actions={
              <Link to="/projects" className="inline-flex items-center gap-1 text-xs font-medium text-accent-600 hover:underline dark:text-accent-300">
                {t("All projects")} <ArrowRight className="size-3" />
              </Link>
            }
          />
          {d.recent.length === 0 ? (
            <div className="p-2">
              <EmptyState
                title={t("No projects yet")}
                message={t("Create your first development environment - PHP, Python, Go, Ruby, Java, .NET or Node.js with a web server - in under a minute.")}
                action={
                  admin ? (
                    <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
                      {t("New project")}
                    </LinkButton>
                  ) : undefined
                }
              />
            </div>
          ) : (
            <ul className="@container divide-y divide-[var(--border)]">
              {d.recent.map((p) => (
                <ProjectRow key={p.id} project={p} usage={d.stats?.perProject[p.id]} />
              ))}
            </ul>
          )}
        </Card>
        <SystemCard d={d} admin={admin} />
      </div>

      <ResourceOverviewCard />
    </div>
  );
}
