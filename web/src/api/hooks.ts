import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./client";
import type { CreateProjectRequest, NodeConfig, Project, UpdateProjectRequest, UpdateSettingsRequest } from "./types";
import { projectUrl } from "@/lib/format";

export const keys = {
  me: ["me"] as const,
  setup: ["setup"] as const,
  dashboard: ["dashboard"] as const,
  runtimes: ["runtimes"] as const,
  docker: ["docker"] as const,
  settings: ["settings"] as const,
  audit: ["audit"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["projects", id] as const,
  projectPlan: (id: string) => ["projects", id, "plan"] as const,
  projectStats: (id: string) => ["projects", id, "stats"] as const,
  database: (id: string) => ["projects", id, "database"] as const,
  databases: (id: string) => ["projects", id, "database", "list"] as const,
};

const LIVE_INTERVAL = 5000;

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

export function useUpdateSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateSettingsRequest) => api.updateSettings(body),
    onSuccess: (data) => {
      qc.setQueryData(keys.settings, data);
      void qc.invalidateQueries({ queryKey: keys.dashboard });
      void qc.invalidateQueries({ queryKey: keys.projects });
      void qc.invalidateQueries({ queryKey: ["tls"] });
    },
  });
}

/**
 * Returns a function building the URL a project should be opened at: the proxy domain
 * (HTTPS when available) when the proxy ports are published, otherwise the direct port.
 */
export function useProjectLinks(): (project: Pick<Project, "httpPort" | "hostnames">) => { url: string; direct: string } {
  const q = useQuery({ queryKey: keys.settings, queryFn: api.settings, staleTime: 60 * 1000 });
  const publicHost = q.data?.publicHost ?? "";
  const proxy = q.data?.proxy;
  return (project) => {
    const direct = projectUrl(project.httpPort, publicHost);
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

export function useAudit(limit = 100) {
  return useQuery({ queryKey: [...keys.audit, limit], queryFn: () => api.audit(limit) });
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
    mutationFn: () => api.dbtool.open(id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["dbtool"] }),
  });
}

export function useDatabaseInfo(id: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.database(id),
    queryFn: async () => (await api.database.info(id)).database,
    refetchInterval: LIVE_INTERVAL,
    enabled,
  });
}

export function useDatabaseList(id: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.databases(id),
    queryFn: async () => (await api.database.list(id)).databases,
    enabled,
    retry: false,
  });
}

export function useDatabaseMutations(id: string) {
  const qc = useQueryClient();
  const invalidate = useProjectInvalidation();
  const refresh = (project?: Project) => {
    invalidate(project);
    void qc.invalidateQueries({ queryKey: keys.database(id) });
    void qc.invalidateQueries({ queryKey: keys.databases(id) });
  };
  const rotate = useMutation({ mutationFn: async () => (await api.database.rotate(id)).project, onSuccess: refresh });
  const expose = useMutation({ mutationFn: async (exposed: boolean) => (await api.database.expose(id, exposed)).project, onSuccess: refresh });
  const create = useMutation({ mutationFn: (name: string) => api.database.create(id, name), onSuccess: () => refresh() });
  const drop = useMutation({ mutationFn: (name: string) => api.database.drop(id, name), onSuccess: () => refresh() });
  return { rotate, expose, create, drop };
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
