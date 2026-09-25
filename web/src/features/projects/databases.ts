import type { Project } from "@/api/types";

/** The services of a project that are databases: the primary ("database") and the additional ones ("db-<name>"), primary first. */
export function databaseServices(project: Pick<Project, "services">): { name: string; kind: string; variant: string }[] {
  const out = project.services
    .filter((s) => s.enabled && (s.kind === "database" || s.kind.startsWith("db-")))
    .map((s) => ({ name: s.kind === "database" ? "" : s.kind.slice(3), kind: s.kind, variant: s.variant }));
  return out.sort((x, y) => (x.name === "" ? -1 : y.name === "" ? 1 : x.name.localeCompare(y.name)));
}

export const databaseEngineNames: Record<string, string> = { mariadb: "MariaDB", mysql: "MySQL", postgresql: "PostgreSQL", mongodb: "MongoDB" };

/** A name an additional database can take (the server checks the reserved ones). */
export const databaseNamePattern = /^[a-z][a-z0-9]*(-[a-z0-9]+)*$/;

/** The prefix of an additional database's variables: analytics → ANALYTICS. */
export function databaseEnvPrefix(name: string): string {
  return name.toUpperCase().replace(/-/g, "_");
}
