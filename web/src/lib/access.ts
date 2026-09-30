import type { Project } from "@/api/types";

/** What a principal may do in a project, as the server's auth.Scope: each level includes the ones before it. */
export type Access = "read" | "operate" | "admin";

const levels: Access[] = ["read", "operate", "admin"];

/** Mirrors auth.Scope.Covers: an unknown or empty level covers nothing. */
export function covers(have: string | undefined, need: Access): boolean {
  const rank = levels.indexOf(have as Access);
  return rank >= 0 && rank >= levels.indexOf(need);
}

/**
 * The caller's level in a project and what it allows, for hiding the controls the server
 * would refuse. Each route's level is in internal/api/api.go (rd, op, adm). A project
 * without access comes from a server before roles, when everyone was an admin.
 */
export function projectAccess(project: Pick<Project, "access">): { access: Access; operate: boolean; admin: boolean } {
  const access = project.access ?? "admin";
  return { access, operate: covers(access, "operate"), admin: covers(access, "admin") };
}
