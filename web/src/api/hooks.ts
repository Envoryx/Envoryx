import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { CreateProjectRequest, Project, UpdateProjectRequest } from "./types";

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
    mutationFn: (body: { publicHost?: string }) => api.updateSettings(body),
    onSuccess: (data) => {
      qc.setQueryData(keys.settings, data);
      void qc.invalidateQueries({ queryKey: keys.dashboard });
    },
  });
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
