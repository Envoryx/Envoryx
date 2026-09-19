import { Link } from "react-router-dom";
import { DiagnosticsBanner } from "@/components/DiagnosticsBanner";
import { useTranslation } from "react-i18next";
import { Plus, ArrowRight } from "lucide-react";
import { useDashboard } from "@/api/hooks";
import { Alert, Badge, Button, Card, CardHeader, EmptyState, ErrorState, LinkButton, PageHeader, Spinner, StatusDot } from "@/components/ui";
import { formatBytes, formatPercent, serviceLabel, stateMeta } from "@/lib/format";
import type { Project } from "@/api/types";

function Stat({ label, value, sub }: { label: string; value: string | number; sub?: string | undefined }) {
  return (
    <Card className="px-5 py-4">
      <p className="text-xs font-medium uppercase tracking-wide text-subtle">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums text-fg">{value}</p>
      {sub && <p className="text-xs text-muted">{sub}</p>}
    </Card>
  );
}

function RecentProject({ project }: { project: Project }) {
  const { t } = useTranslation();
  const meta = stateMeta[project.status.state];
  return (
    <Link
      to={`/projects/${project.id}`}
      className="flex items-center justify-between gap-4 rounded-lg px-3 py-2.5 transition-colors hover:bg-muted"
    >
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <StatusDot tone={meta.tone} pulse={meta.pulse ?? false} />
          <span className="truncate text-sm font-medium text-fg">{project.name}</span>
        </div>
        <div className="mt-1 flex flex-wrap gap-1.5 pl-4.5">
          {project.services
            .filter((s) => s.enabled)
            .map((s) => (
              <Badge key={s.kind}>{serviceLabel(s.kind, s.version, s.variant)}</Badge>
            ))}
        </div>
      </div>
      <span className="text-xs text-muted">{t(meta.label)}</span>
    </Link>
  );
}

export function DashboardPage() {
  const { t } = useTranslation();
  const q = useDashboard();

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={q.error.message} action={<Button onClick={() => void q.refetch()}>{t("Retry")}</Button>} />;
  const d = q.data;

  return (
    <div>
      <PageHeader
        title={t("Dashboard")}
        description={t("Overview of your development environments.")}
        actions={
          <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
            {t("New project")}
          </LinkButton>
        }
      />

      <DiagnosticsBanner />
      {d.update?.available && (
        <div className="mb-6">
          <Alert tone="blue" title={t("Envoryx {{version}} is available", { version: d.update.latest })}>
            {t("You are running {{current}}. Update the container to get the new version.", { current: d.update.current })}{" "}
            {d.update.url && (
              <a href={d.update.url} target="_blank" rel="noopener noreferrer" className="underline">
                {t("Release notes")}
              </a>
            )}
          </Alert>
        </div>
      )}

      {d.hostPath.error && !Object.keys(d.hostPath.overrides).length && (
        <div className="mb-6">
          <Alert tone="red" title={t("Host paths could not be detected")}>
            {t("Envoryx needs to know the host paths behind /projects and /config to mount project files into containers. Set ENVORYX_PROJECTS_HOST_PATH and ENVORYX_CONFIG_HOST_PATH.")} ({d.hostPath.error})
          </Alert>
        </div>
      )}

      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        <Stat label={t("Projects")} value={d.projects.total} />
        <Stat label={t("Running")} value={d.projects.running} />
        <Stat label={t("Stopped")} value={d.projects.stopped} sub={d.projects.attention ? t("{{count}} need attention", { count: d.projects.attention }) : undefined} />
        <Stat label={t("Containers")} value={d.stats?.containers ?? 0} sub={t("{{count}} running", { count: d.stats?.running ?? 0 })} />
        <Stat label={t("CPU")} value={formatPercent(d.stats?.cpuPercent ?? 0)} sub={d.docker.ncpu ? t("{{count}} cores", { count: d.docker.ncpu }) : undefined} />
        <Stat label={t("Memory")} value={formatBytes(d.stats?.memoryBytes ?? 0)} sub={d.docker.memTotal ? t("of {{total}}", { total: formatBytes(d.docker.memTotal) }) : undefined} />
      </div>

      {d.issues.length > 0 && (
        <div className="mt-6">
          <Alert tone="amber" title={t("{{count}} inconsistencies detected", { count: d.issues.length })}>
            <ul className="list-disc space-y-0.5 pl-4">
              {d.issues.map((i, idx) => (
                <li key={idx}>
                  <Link to={`/projects/${i.projectId}`} className="font-medium underline-offset-2 hover:underline">
                    {i.projectName}
                  </Link>
                  : {i.message}
                </li>
              ))}
            </ul>
          </Alert>
        </div>
      )}

      <div className="mt-6 grid gap-6 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader
            title={t("Recent projects")}
            actions={
              <Link to="/projects" className="inline-flex items-center gap-1 text-xs font-medium text-accent-600 hover:underline dark:text-accent-300">
                {t("All projects")} <ArrowRight className="size-3" />
              </Link>
            }
          />
          <div className="p-2">
            {d.recent.length === 0 ? (
              <EmptyState
                title={t("No projects yet")}
                message={t("Create your first development environment with PHP and a web server in under a minute.")}
                action={
                  <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
                    {t("New project")}
                  </LinkButton>
                }
              />
            ) : (
              d.recent.map((p) => <RecentProject key={p.id} project={p} />)
            )}
          </div>
        </Card>

        <Card>
          <CardHeader title={t("Docker engine")} />
          <dl className="space-y-3 px-5 py-4 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-muted">{t("Status")}</dt>
              <dd className="flex items-center gap-2 font-medium">
                <StatusDot tone={d.docker.connected ? "green" : "red"} />
                {d.docker.connected ? t("Connected") : t("Unreachable")}
              </dd>
            </div>
            {d.docker.connected ? (
              <>
                <div className="flex justify-between gap-4">
                  <dt className="text-muted">{t("Version")}</dt>
                  <dd className="font-mono text-xs">{d.docker.serverVersion} (API {d.docker.apiVersion})</dd>
                </div>
                <div className="flex justify-between gap-4">
                  <dt className="text-muted">{t("Host")}</dt>
                  <dd className="truncate text-xs">
                    {d.docker.os} · {d.docker.architecture}
                  </dd>
                </div>
                <div className="flex justify-between gap-4">
                  <dt className="text-muted">{t("All containers")}</dt>
                  <dd className="tabular-nums">
                    {t("{{running}} / {{total}} running", { running: d.docker.running, total: d.docker.containers })}
                  </dd>
                </div>
                {d.orphans > 0 && (
                  <div className="flex justify-between gap-4">
                    <dt className="text-muted">{t("Orphaned resources")}</dt>
                    <dd>
                      <Link to="/docker" className="text-amber-600 underline-offset-2 hover:underline dark:text-amber-400">
                        {d.orphans}
                      </Link>
                    </dd>
                  </div>
                )}
              </>
            ) : (
              <p className="text-xs text-red-500">{d.docker.error}</p>
            )}
          </dl>
        </Card>
      </div>

      {d.storage && d.storage.length > 0 && (
        <Card className="mt-6">
          <CardHeader title={t("Disk space")} description={t("Free space on the filesystems behind /config, /projects and /backups. A backup is refused when it would fill the disk.")} />
          <ul className="divide-y divide-[var(--border)]">
            {d.storage.map((s) => {
              const used = s.totalBytes > 0 ? Math.min(100, Math.round(((s.totalBytes - s.freeBytes) / s.totalBytes) * 100)) : 0;
              return (
                <li key={s.path} className="px-5 py-3 text-sm">
                  <div className="flex items-center justify-between gap-4">
                    <span className="flex items-center gap-2 font-mono text-xs">
                      <StatusDot tone={s.low ? "red" : "green"} />
                      {s.path}
                    </span>
                    <span className={s.low ? "font-medium text-red-500" : "text-muted"}>
                      {t("{{free}} free of {{total}}", { free: formatBytes(s.freeBytes), total: formatBytes(s.totalBytes) })}
                    </span>
                  </div>
                  <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={used} aria-valuemin={0} aria-valuemax={100} aria-label={s.path}>
                    <div className={s.low ? "h-full bg-red-500" : "h-full bg-accent-500"} style={{ width: `${used}%` }} />
                  </div>
                </li>
              );
            })}
          </ul>
        </Card>
      )}
    </div>
  );
}
