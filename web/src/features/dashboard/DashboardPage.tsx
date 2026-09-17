import { Link } from "react-router-dom";
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
      <span className="text-xs text-muted">{meta.label}</span>
    </Link>
  );
}

export function DashboardPage() {
  const q = useDashboard();

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={q.error.message} action={<Button onClick={() => void q.refetch()}>Retry</Button>} />;
  const d = q.data;

  return (
    <div>
      <PageHeader
        title="Dashboard"
        description="Overview of your development environments."
        actions={
          <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
            New project
          </LinkButton>
        }
      />

      {d.hostPath.error && !Object.keys(d.hostPath.overrides).length && (
        <div className="mb-6">
          <Alert tone="red" title="Host paths could not be detected">
            Staqio needs to know the host paths behind <code>/projects</code> and <code>/config</code> to mount project files
            into containers. Set <code>STAQIO_PROJECTS_HOST_PATH</code> and <code>STAQIO_CONFIG_HOST_PATH</code>. ({d.hostPath.error})
          </Alert>
        </div>
      )}

      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        <Stat label="Projects" value={d.projects.total} />
        <Stat label="Running" value={d.projects.running} />
        <Stat label="Stopped" value={d.projects.stopped} sub={d.projects.attention ? `${d.projects.attention} need attention` : undefined} />
        <Stat label="Containers" value={d.stats?.containers ?? 0} sub={`${d.stats?.running ?? 0} running`} />
        <Stat label="CPU" value={formatPercent(d.stats?.cpuPercent ?? 0)} sub={d.docker.ncpu ? `${d.docker.ncpu} cores` : undefined} />
        <Stat label="Memory" value={formatBytes(d.stats?.memoryBytes ?? 0)} sub={d.docker.memTotal ? `of ${formatBytes(d.docker.memTotal)}` : undefined} />
      </div>

      {d.issues.length > 0 && (
        <div className="mt-6">
          <Alert tone="amber" title={`${d.issues.length} inconsistenc${d.issues.length === 1 ? "y" : "ies"} detected`}>
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
            title="Recent projects"
            actions={
              <Link to="/projects" className="inline-flex items-center gap-1 text-xs font-medium text-accent-600 hover:underline dark:text-accent-300">
                All projects <ArrowRight className="size-3" />
              </Link>
            }
          />
          <div className="p-2">
            {d.recent.length === 0 ? (
              <EmptyState
                title="No projects yet"
                message="Create your first development environment with PHP and a web server in under a minute."
                action={
                  <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
                    New project
                  </LinkButton>
                }
              />
            ) : (
              d.recent.map((p) => <RecentProject key={p.id} project={p} />)
            )}
          </div>
        </Card>

        <Card>
          <CardHeader title="Docker engine" />
          <dl className="space-y-3 px-5 py-4 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-muted">Status</dt>
              <dd className="flex items-center gap-2 font-medium">
                <StatusDot tone={d.docker.connected ? "green" : "red"} />
                {d.docker.connected ? "Connected" : "Unreachable"}
              </dd>
            </div>
            {d.docker.connected ? (
              <>
                <div className="flex justify-between gap-4">
                  <dt className="text-muted">Version</dt>
                  <dd className="font-mono text-xs">{d.docker.serverVersion} (API {d.docker.apiVersion})</dd>
                </div>
                <div className="flex justify-between gap-4">
                  <dt className="text-muted">Host</dt>
                  <dd className="truncate text-xs">
                    {d.docker.os} · {d.docker.architecture}
                  </dd>
                </div>
                <div className="flex justify-between gap-4">
                  <dt className="text-muted">All containers</dt>
                  <dd className="tabular-nums">
                    {d.docker.running} / {d.docker.containers} running
                  </dd>
                </div>
                {d.orphans > 0 && (
                  <div className="flex justify-between gap-4">
                    <dt className="text-muted">Orphaned resources</dt>
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
    </div>
  );
}
