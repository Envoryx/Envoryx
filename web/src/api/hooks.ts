import { useInfiniteQuery, useIsMutating, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./client";
import { servesOf, type AuditFilter, type CreateProjectRequest, type DuplicateProjectRequest, type RenameProjectRequest, type NodeConfig, type Project, type PythonConfig, type GoConfig, type RubyConfig, type UpdateProjectRequest, type UpdateSettingsRequest } from "./types";
import { projectUrl } from "@/lib/format";

export const keys = {
  me: ["me"] as const,
  setup: ["setup"] as const,
  dashboard: ["dashboard"] as const,
  operations: ["operations"] as const,
  runtimes: ["runtimes"] as const,
  docker: ["docker"] as const,
  settings: ["settings"] as const,
  audit: ["audit"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["projects", id] as const,
  projectPlan: (id: string) => ["projects", id, "plan"] as const,
  projectStats: (id: string) => ["projects", id, "stats"] as const,
  database: (id: string, db = "") => ["projects", id, "database", db] as const,
  databases: (id: string, db = "") => ["projects", id, "database", db, "list"] as const,
  snapshots: (id: string, db = "") => ["projects", id, "database", db, "snapshots"] as const,
  databaseServers: (id: string) => ["projects", id, "database-servers"] as const,
};

const LIVE_INTERVAL = 5000;

/**
 * Running and recently finished project operations. Polls quickly while something runs
 * (or a mutation of ours is in flight), slowly otherwise.
 */
export function useOperations() {
  const mutating = useIsMutating();
  return useQuery({
    queryKey: keys.operations,
    queryFn: async () => (await api.operations()).operations,
    refetchInterval: (q) => ((q.state.data?.some((o) => !o.finishedAt) ?? false) || mutating > 0 ? 1000 : LIVE_INTERVAL),
    staleTime: 500,
  });
}

export function useDashboard() {
  return useQuery({ queryKey: keys.dashboard, queryFn: api.dashboard, refetchInterval: LIVE_INTERVAL });
}

export function useRuntimes() {
  return useQuery({ queryKey: keys.runtimes, queryFn: api.runtimes, staleTime: 5 * 60 * 1000 });
}

export function useDockerOverview() {
  return useQuery({ queryKey: keys.docker, queryFn: api.docker, refetchInterval: LIVE_INTERVAL });
}

export function useSettings() {
  return useQuery({ queryKey: keys.settings, queryFn: api.settings });
}

/** Host used for project links; falls back to the browser address bar while loading. */
export function usePublicHost(): string {
  const q = useQuery({ queryKey: keys.settings, queryFn: api.settings, staleTime: 60 * 1000 });
  return q.data?.publicHost ?? "";
}

/** Set-up checks with fixes; refreshed every minute while the page is open. */
export function useDiagnostics() {
  return useQuery({ queryKey: ["diagnostics"], queryFn: api.diagnostics, refetchInterval: 60 * 1000, staleTime: 20 * 1000 });
}

export function useUpdateSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateSettingsRequest) => api.updateSettings(body),
    onSuccess: (data) => {
      qc.setQueryData(keys.settings, data);
      void qc.invalidateQueries({ queryKey: keys.dashboard });
      void qc.invalidateQueries({ queryKey: keys.projects });
      void qc.invalidateQueries({ queryKey: ["tls"] });
      void qc.invalidateQueries({ queryKey: ["diagnostics"] });
    },
  });
}

/**
 * Returns a function building the URL a project should be opened at: the proxy domain
 * (HTTPS when available) when the proxy ports are published, otherwise the direct port.
 * While a Python, Go or Ruby server or Node dev server serves the project the web container's HTTP
 * port stays unpublished, so that container's host port is the direct address.
 */
export function useProjectLinks(): (project: Pick<Project, "httpPort" | "hostnames" | "serves" | "services">) => { url: string; direct: string } {
  const q = useQuery({ queryKey: keys.settings, queryFn: api.settings, staleTime: 60 * 1000 });
  const publicHost = q.data?.publicHost ?? "";
  const proxy = q.data?.proxy;
  return (project) => {
    const serves = project.serves ?? servesOf(project);
    const app = serves === "node" || serves === "python" || serves === "go" || serves === "ruby" ? ((project.services.find((s) => s.kind === serves && s.enabled)?.config ?? {}) as NodeConfig | PythonConfig | GoConfig | RubyConfig) : undefined;
    const direct = projectUrl(app ? (app.hostPort ?? 0) : project.httpPort, publicHost);
    const host = project.hostnames?.[0];
    if (!proxy?.enabled || !host) return { url: direct, direct };
    if (proxy.tls && proxy.httpsPort > 0) {
      return { url: `https://${host}${proxy.httpsPort === 443 ? "" : `:${proxy.httpsPort}`}`, direct };
    }
    if (proxy.httpPort > 0) {
      return { url: `http://${host}${proxy.httpPort === 80 ? "" : `:${proxy.httpPort}`}`, direct };
    }
    return { url: direct, direct };
  };
}

/** URL of a project's Node dev server through the proxy, or its direct port. */
export function useDevServerLink(): (project: Pick<Project, "devHostname" | "services">) => string {
  const q = useQuery({ queryKey: keys.settings, queryFn: api.settings, staleTime: 60 * 1000 });
  const publicHost = q.data?.publicHost ?? "";
  const proxy = q.data?.proxy;
  return (project) => {
    const svc = project.services.find((s) => s.kind === "node" && s.enabled);
    const cfg = (svc?.config ?? {}) as NodeConfig;
    if (!cfg.devServer) return "";
    if (proxy?.enabled && project.devHostname) {
      if (proxy.tls && proxy.httpsPort > 0) return `https://${project.devHostname}${proxy.httpsPort === 443 ? "" : `:${proxy.httpsPort}`}`;
      if (proxy.httpPort > 0) return `http://${project.devHostname}${proxy.httpPort === 80 ? "" : `:${proxy.httpPort}`}`;
    }
    return cfg.hostPort ? projectUrl(cfg.hostPort, publicHost) : "";
  };
}

export function useTLSInfo() {
  return useQuery({ queryKey: ["tls"], queryFn: api.tls.info });
}

export function useProjectDomains(id: string) {
  return useQuery({ queryKey: [...keys.project(id), "domains"], queryFn: () => api.projects.domains.list(id) });
}

/** The audit log for a filter, a page at a time (fetchNextPage loads older entries). */
export function useAuditLog(filter: AuditFilter) {
  return useInfiniteQuery({
    queryKey: [...keys.audit, filter],
    queryFn: ({ pageParam }) => api.audit.list(filter, pageParam),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
  });
}

export function useAuditUsers() {
  return useQuery({ queryKey: [...keys.audit, "users"], queryFn: async () => (await api.audit.users()).users, staleTime: 60 * 1000 });
}

export function useAuditSettings() {
  const qc = useQueryClient();
  const query = useQuery({ queryKey: [...keys.audit, "settings"], queryFn: async () => (await api.audit.settings()).settings });
  const save = useMutation({
    mutationFn: async (days: number) => (await api.audit.setSettings(days)).settings,
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.audit }),
  });
  return { query, save };
}

export function useProjects() {
  return useQuery({
    queryKey: keys.projects,
    queryFn: async () => (await api.projects.list()).projects,
    refetchInterval: LIVE_INTERVAL,
  });
}

export function useProject(id: string) {
  return useQuery({
    queryKey: keys.project(id),
    queryFn: async () => (await api.projects.get(id)).project,
    refetchInterval: LIVE_INTERVAL,
  });
}

export function useProjectPlan(id: string) {
  return useQuery({ queryKey: keys.projectPlan(id), queryFn: async () => (await api.projects.plan(id)).plan });
}

export function useProjectStats(id: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.projectStats(id),
    queryFn: () => api.projects.stats(id),
    refetchInterval: LIVE_INTERVAL,
    enabled,
  });
}

function useProjectInvalidation() {
  const qc = useQueryClient();
  return (project?: Project) => {
    void qc.invalidateQueries({ queryKey: keys.projects });
    void qc.invalidateQueries({ queryKey: keys.dashboard });
    void qc.invalidateQueries({ queryKey: keys.docker });
    void qc.invalidateQueries({ queryKey: keys.operations });
    if (project) {
      qc.setQueryData(keys.project(project.id), project);
    }
  };
}

export type ProjectAction = "start" | "stop" | "restart";

export function useProjectAction() {
  const invalidate = useProjectInvalidation();
  return useMutation({
    mutationFn: async ({ id, action }: { id: string; action: ProjectAction }) =>
      (await api.projects[action](id)).project,
    onSuccess: (project) => invalidate(project),
    onError: () => invalidate(),
  });
}

export function useImageChoice(id: string) {
  const invalidate = useProjectInvalidation();
  return useMutation({
    mutationFn: async ({ image, use }: { image: string; use: "previous" | "latest" }) => (await api.projects.useImage(id, image, use)).project,
    onSuccess: (project) => invalidate(project),
    onError: () => invalidate(),
  });
}

export function useCreateProject() {
  const invalidate = useProjectInvalidation();
  return useMutation({
    mutationFn: async (body: CreateProjectRequest) => (await api.projects.create(body)).project,
    onSuccess: (project) => invalidate(project),
  });
}

/** Copies a project; the answer is the new project, which the caller navigates to. */
export function useDuplicateProject() {
  const invalidate = useProjectInvalidation();
  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: DuplicateProjectRequest }) => (await api.projects.duplicate(id, body)).project,
    onSuccess: (project) => invalidate(project),
  });
}

/** Renames a project; the answer says what moved with it. */
export function useRenameProject(id: string) {
  const invalidate = useProjectInvalidation();
  return useMutation({
    mutationFn: (body: RenameProjectRequest) => api.projects.rename(id, body),
    onSuccess: (res) => invalidate(res.project),
  });
}

export function useUpdateProject(id: string) {
  const invalidate = useProjectInvalidation();
  return useMutation({
    mutationFn: async (body: UpdateProjectRequest) => (await api.projects.update(id, body)).project,
    onSuccess: (project) => invalidate(project),
  });
}

export function useExtraServices(id: string) {
  return useQuery({ queryKey: ["projects", id, "extras"], queryFn: async () => (await api.projects.extras(id)).services, refetchInterval: LIVE_INTERVAL });
}

/** The shared Ollama store's models and the project's downloads; polled every second while one runs. */
export function useOllamaModels(id: string, enabled = true) {
  return useQuery({
    queryKey: ["projects", id, "ollama"],
    queryFn: () => api.ollama.models(id),
    enabled,
    refetchInterval: (q) => (q.state.data?.pulls.some((p) => !p.done) ? 1000 : LIVE_INTERVAL),
  });
}

/** Downloading, cancelling and deleting Ollama models; every change refreshes the list. */
export function useOllamaMutations(id: string) {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: ["projects", id, "ollama"] });
  const pull = useMutation({ mutationFn: async (model: string) => (await api.ollama.pull(id, model)).pull, onSuccess: refresh });
  const cancel = useMutation({ mutationFn: (model: string) => api.ollama.cancel(id, model), onSuccess: refresh });
  const remove = useMutation({ mutationFn: (model: string) => api.ollama.remove(id, model), onSuccess: refresh });
  return { pull, cancel, remove };
}

/** The project's object storage; null when it has none (404). */
export function useStorage(id: string) {
  return useQuery({
    queryKey: ["projects", id, "storage"],
    queryFn: async () => {
      try {
        return (await api.storage.info(id)).storage;
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }
    },
    refetchInterval: LIVE_INTERVAL,
  });
}

export function useProjectManifest(id: string) {
  return useQuery({ queryKey: ["projects", id, "manifest"], queryFn: () => api.manifest.get(id), retry: false });
}

export function useGitStatus(id: string, enabled = true) {
  return useQuery({ queryKey: ["projects", id, "git"], queryFn: async () => (await api.git.status(id)).git, enabled, retry: false });
}

export function useDeployKey() {
  return useQuery({ queryKey: ["deploy-key"], queryFn: async () => (await api.git.deployKey()).publicKey, staleTime: 60 * 60 * 1000 });
}

export function useProjectActions(id: string) {
  return useQuery({
    queryKey: ["projects", id, "actions"],
    queryFn: async () => (await api.projects.actions(id)).actions,
    refetchInterval: LIVE_INTERVAL,
  });
}

export function useDBTool() {
  return useQuery({ queryKey: ["dbtool"], queryFn: () => api.dbtool.status(), refetchInterval: LIVE_INTERVAL });
}

export function useSetDBTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) => api.dbtool.set(enabled),
    onSuccess: (status) => qc.setQueryData(["dbtool"], status),
  });
}

export function useOpenDBTool(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (db: string = "") => api.dbtool.open(id, db),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["dbtool"] }),
  });
}

/** Every database of a project, the primary first. */
export function useDatabases(id: string, enabled = true) {
  return useQuery({
    queryKey: keys.databaseServers(id),
    queryFn: async () => (await api.databases(id)).databases,
    refetchInterval: LIVE_INTERVAL,
    enabled,
  });
}

export function useDatabaseInfo(id: string, enabled: boolean, db = "") {
  return useQuery({
    queryKey: keys.database(id, db),
    queryFn: async () => (await api.database.info(id, db)).database,
    refetchInterval: LIVE_INTERVAL,
    enabled,
  });
}

export function useDatabaseList(id: string, enabled: boolean, db = "") {
  return useQuery({
    queryKey: keys.databases(id, db),
    queryFn: async () => (await api.database.list(id, db)).databases,
    enabled,
    retry: false,
  });
}

export function useDatabaseMutations(id: string, db = "") {
  const qc = useQueryClient();
  const invalidate = useProjectInvalidation();
  const refresh = (project?: Project) => {
    invalidate(project);
    void qc.invalidateQueries({ queryKey: keys.database(id, db) });
    void qc.invalidateQueries({ queryKey: keys.databaseServers(id) });
  };
  const rotate = useMutation({ mutationFn: async () => (await api.database.rotate(id, db)).project, onSuccess: refresh });
  const expose = useMutation({ mutationFn: async (exposed: boolean) => (await api.database.expose(id, exposed, db)).project, onSuccess: refresh });
  const create = useMutation({ mutationFn: (name: string) => api.database.create(id, name, db), onSuccess: () => refresh() });
  const drop = useMutation({ mutationFn: (name: string) => api.database.drop(id, name, db), onSuccess: () => refresh() });
  return { rotate, expose, create, drop };
}

/** Snapshots of one database of a project, newest first. */
export function useSnapshots(id: string, enabled: boolean, db = "") {
  return useQuery({
    queryKey: keys.snapshots(id, db),
    queryFn: async () => (await api.database.snapshots(id, db)).snapshots,
    enabled,
  });
}

export function useSnapshotMutations(id: string, db = "") {
  const qc = useQueryClient();
  const invalidate = useProjectInvalidation();
  const refresh = () => {
    invalidate();
    // Every database's snapshot list: a database-only backup is listed with each of them.
    void qc.invalidateQueries({ queryKey: ["projects", id, "database"] });
    void qc.invalidateQueries({ queryKey: ["projects", id, "backups"] });
  };
  const create = useMutation({ mutationFn: async (note: string) => (await api.database.snapshot(id, note, db)).snapshot, onSuccess: refresh });
  const restore = useMutation({
    mutationFn: async ({ snapshotId, confirm }: { snapshotId: string; confirm: string }) => (await api.database.restoreSnapshot(id, snapshotId, confirm, db)).snapshot,
    onSuccess: refresh,
  });
  const remove = useMutation({ mutationFn: (snapshotId: string) => api.backups.remove(id, snapshotId), onSuccess: refresh });
  const clone = useMutation({
    mutationFn: async (body: { source: string; sourceDb?: string; snapshot: boolean; confirm: string }) => (await api.database.clone(id, body, db)).clone,
    onSuccess: refresh,
  });
  return { create, restore, remove, clone };
}

export function useDeleteProject() {
  const qc = useQueryClient();
  const invalidate = useProjectInvalidation();
  return useMutation({
    mutationFn: ({ id, confirm, deleteFiles }: { id: string; confirm: string; deleteFiles: boolean }) =>
      api.projects.remove(id, confirm, deleteFiles),
    onSuccess: (_data, vars) => {
      qc.removeQueries({ queryKey: keys.project(vars.id) });
      invalidate();
    },
  });
}
