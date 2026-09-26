import { Plus, Search } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useDashboard, useProjectLinks, useProjects } from "@/api/hooks";
import { appKindOf, type Project } from "@/api/types";
import { Alert, Badge, Button, Card, EmptyState, ErrorState, Input, LinkButton, PageHeader, Spinner, StatusDot } from "@/components/ui";
import { formatBytes, formatPercent, serviceLabel, stateMeta } from "@/lib/format";
import { ProjectActionButtons, useActionError } from "./ProjectActions";
import { OperationHint } from "@/components/OperationsTray";
import { errorText, translateMessage } from "@/lib/errors";

/** Service badges in a fixed order – runtime, web server, database, extras – so rows read alike. */
function ServiceBadges({ project }: { project: Project }) {
  const { t } = useTranslation();
  // The application runtime leads: PHP when present, else Python, else Go, else Node; a static site has none.
  const app = project.appService ?? appKindOf(project);
  const runtime = app ? project.services.find((s) => s.kind === app && s.enabled) : undefined;
  const web = project.services.find((s) => s.kind === "web");
  const db = project.services.find((s) => s.kind === "database");
  const extras = project.services.filter((s) => s.enabled && s.kind !== app && (s.kind === "node" || s.kind === "python" || s.kind === "go" || s.kind === "ruby" || s.kind === "redis" || s.kind === "memcached" || s.kind === "mailpit" || s.kind === "rabbitmq" || s.kind === "meilisearch" || s.kind === "typesense" || s.kind === "opensearch" || s.kind === "ollama" || s.kind === "storage"));
  return (
    <div className="flex flex-wrap gap-1.5">
      {runtime ? <Badge tone="blue">{serviceLabel(runtime.kind, runtime.version)}</Badge> : <Badge>{t("Static")}</Badge>}
      {web && <Badge>{serviceLabel("web", web.version, web.variant)}</Badge>}
      {db && <Badge tone="amber">{serviceLabel("database", db.version, db.variant)}</Badge>}
      {extras.map((s) => (
        <Badge key={s.kind}>{serviceLabel(s.kind, s.version, s.variant)}</Badge>
      ))}
    </div>
  );
}

// One grid shared by every row, so name, stack, state, resources and actions line up
// down the list no matter how many badges a project has. Narrow screens use two rows:
// name and actions, then the stack and the state; resources are a large-screen extra.
const rowGrid = "grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-2 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1.7fr)_6.5rem_6rem_auto] lg:items-center lg:gap-x-5";

/** The address without its scheme – the list is tight and every project is https anyway. */
function shortUrl(url: string): string {
  return url.replace(/^https?:\/\//, "");
}

function ProjectRow({ project, usage }: { project: Project; usage?: { cpuPercent: number; memoryBytes: number } | undefined }) {
  const { t } = useTranslation();
  const meta = stateMeta[project.status.state];
  const links = useProjectLinks();
  const { url } = links(project);
  const { error, capture } = useActionError();
  const running = project.status.services.filter((s) => s.running).length;
  const total = project.status.services.filter((s) => s.state !== "external").length;

  return (
    <li className="px-4 py-3 sm:px-5">
      <div className={rowGrid}>
        <div className="flex min-w-0 items-center gap-3">
          <StatusDot tone={meta.tone} pulse={meta.pulse ?? false} />
          <div className="min-w-0">
            <Link to={`/projects/${project.id}`} className="block truncate text-sm font-semibold text-fg hover:underline">
              {project.name}
            </Link>
            <p className="truncate font-mono text-[11px] text-subtle" title={url}>
              {url ? shortUrl(url) : t("no port")}
            </p>
            {project.status.operation && <OperationHint op={project.status.operation} className="max-w-full" />}
          </div>
        </div>
        <div className="col-start-1 row-start-2 min-w-0 pl-[22px] lg:col-start-2 lg:row-start-1 lg:pl-0">
          <ServiceBadges project={project} />
        </div>
        <div className="col-start-2 row-start-2 self-center text-right text-xs text-muted lg:col-start-3 lg:row-start-1 lg:text-left">
          <span className="block font-medium text-fg">{t(meta.label)}</span>
          <span className="block">{t("{{running}}/{{total}} containers", { running, total })}</span>
        </div>
        <div className="hidden text-xs tabular-nums text-muted lg:block">
          {usage ? (
            <>
              <span className="block">CPU {formatPercent(usage.cpuPercent)}</span>
              <span className="block">RAM {formatBytes(usage.memoryBytes)}</span>
            </>
          ) : (
            <span className="block text-subtle">—</span>
          )}
        </div>
        <div className="col-start-2 row-start-1 justify-self-end lg:col-start-5 lg:row-start-1">
          <ProjectActionButtons project={project} onError={capture} />
        </div>
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
              {translateMessage(w, t)}
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
        <ErrorState message={errorText(q.error, t)} action={<Button onClick={() => void q.refetch()}>{t("Retry")}</Button>} />
      ) : q.data.length === 0 ? (
        <EmptyState
          title={t("No projects yet")}
          message={t("Create a project to get an isolated PHP, Python, Go, Ruby or Node.js environment with its own web server and Docker network.")}
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
