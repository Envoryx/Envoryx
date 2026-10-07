import type { Settings } from "@/api/types";

/**
 * The Docker host's path of a directory Envoryx uses (projects, config): an override,
 * the detected bind mount, or on bare metal the directory itself. undefined while unknown.
 */
export function hostPathOf(s: Pick<Settings, "hostPath"> | undefined, dir: string | undefined): string | undefined {
  if (!s?.hostPath || !dir) return undefined;
  return s.hostPath.overrides[dir] ?? s.hostPath.detected[dir] ?? (s.hostPath.bareMetal ? dir : undefined);
}
