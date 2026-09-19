import { Plus, Search } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useDashboard, useProjectLinks, useProjects } from "@/api/hooks";
import type { Project } from "@/api/types";
import { Alert, Badge, Button, Card, EmptyState, ErrorState, Input, LinkButton, PageHeader, Spinner, StatusDot } from "@/components/ui";
import { formatBytes, formatPercent, serviceLabel, stateMeta } from "@/lib/format";
import { ProjectActionButtons, useActionError } from "./ProjectActions";

function ProjectRow({ project, usage }: { project: Project; usage?: { cpuPercent: number; memoryBytes: number } | undefined }) {
  const { t } = useTranslation();
  const meta = stateMeta[project.status.state];
  const php = project.services.find((s) => s.kind === "php");
  const web = project.services.find((s) => s.kind === "web");
  const db = project.services.find((s) => s.kind === "database");
  const links = useProjectLinks();
  const { url } = links(project);
  const { error, capture } = useActionError();

  return (
    <li className="px-4 py-3 sm:px-5">
      <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
        <div className="flex min-w-[14rem] flex-1 items-center gap-3">
          <StatusDot tone={meta.tone} pulse={meta.pulse ?? false} />
          <div className="min-w-0">
            <Link to={`/projects/${project.id}`} className="block truncate text-sm font-semibold text-fg hover:underline">
              {project.name}
            </Link>
            <p className="truncate font-mono text-[11px] text-subtle">{url || t("no port")}</p>
          </div>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {php ? <Badge tone="blue">{serviceLabel("php", php.version)}</Badge> : <Badge>{t("No PHP")}</Badge>}
          {db && <Badge tone="amber">{serviceLabel("database", db.version, db.variant)}</Badge>}
          {project.services.filter((s) => s.enabled && (s.kind === "redis" || s.kind === "mailpit" || s.kind === "storage" || s.kind === "node")).map((s) => (
            <Badge key={s.kind}>{serviceLabel(s.kind, s.version, s.variant)}</Badge>
          ))}
          {web && <Badge>{serviceLabel("web", web.version, web.variant)}</Badge>}
        </div>
        <div className="hidden w-40 text-xs text-muted md:block">
          <span className="font-medium text-fg">{t(meta.label)}</span>
          <span className="block">
            {t("{{running}}/{{total}} containers", { running: project.status.services.filter((s) => s.running).length, total: project.status.services.length })}
          </span>
        </div>
        <div className="hidden w-32 text-xs tabular-nums text-muted lg:block">
          {usage ? (
            <>
              <span className="block">CPU {formatPercent(usage.cpuPercent)}</span>
              <span className="block">RAM {formatBytes(usage.memoryBytes)}</span>
            </>
          ) : (
            <span className="block text-subtle">—</span>
          )}
        </div>
        <ProjectActionButtons project={project} onError={capture} />
      </div>
      {(error || project.status.warnings.length > 0) && (
        <div className="mt-2 space-y-1 pl-6">
          {error && (
            <p className="text-xs text-red-500" role="alert">
              {error}
            </p>
          )}
          {project.status.warnings.map((w, i) => (
            <p key={i} className="text-xs text-amber-600 dark:text-amber-400">
              {w}
            </p>
          ))}
        </div>
      )}
    </li>
  );
}

export function ProjectsPage() {
  const { t } = useTranslation();
  const q = useProjects();
  const dash = useDashboard();
  const [filter, setFilter] = useState("");

  const projects = useMemo(() => {
    const list = q.data ?? [];
    const f = filter.trim().toLowerCase();
    if (!f) return list;
    return list.filter((p) => p.name.toLowerCase().includes(f) || p.slug.includes(f) || p.path.includes(f));
  }, [q.data, filter]);

  return (
    <div>
      <PageHeader
        title={t("Projects")}
        description={t("Each project runs in its own isolated set of containers.")}
        actions={
          <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
            {t("New project")}
          </LinkButton>
        }
      />
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <ErrorState message={q.error.message} action={<Button onClick={() => void q.refetch()}>{t("Retry")}</Button>} />
      ) : q.data.length === 0 ? (
        <EmptyState
          title={t("No projects yet")}
          message={t("Create a project to get an isolated PHP + web server environment with its own Docker network.")}
          action={
            <LinkButton to="/projects/new" variant="primary" icon={<Plus className="size-4" />}>
              {t("Create your first project")}
            </LinkButton>
          }
        />
      ) : (
        <Card>
          <div className="flex items-center gap-2 border-b border-default px-4 py-3">
            <Search className="size-4 text-subtle" aria-hidden />
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t("Filter projects…")}
              aria-label={t("Filter projects")}
              className="h-8 border-0 bg-transparent px-1 focus:ring-0"
            />
            <span className="text-xs text-subtle">
              {projects.length}/{q.data.length}
            </span>
          </div>
          {projects.length === 0 ? (
            <p className="px-5 py-8 text-center text-sm text-muted">{t("No project matches “{{filter}}”.", { filter })}</p>
          ) : (
            <ul className="divide-y divide-[var(--border)]">
              {projects.map((p) => (
                <ProjectRow key={p.id} project={p} usage={dash.data?.stats?.perProject[p.id]} />
              ))}
            </ul>
          )}
        </Card>
      )}
      {q.data && q.data.some((p) => p.status.warnings.some((w) => w.includes("Docker engine unavailable"))) && (
        <div className="mt-4">
          <Alert tone="red">{t("Docker is unreachable, the displayed states may be outdated.")}</Alert>
        </div>
      )}
    </div>
  );
}
