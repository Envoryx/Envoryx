import { formatBytes } from "@/lib/format";
import type { TFunction } from "i18next";
import type { AuditEntry } from "@/api/types";

/** English labels for the backend's audit actions; translated on the client like other keys. */
export const auditActionLabels: Record<string, string> = {
  "auth.login": "Signed in",
  "auth.login_failed": "Sign-in failed",
  "auth.logout": "Signed out",
  "auth.setup": "Initial setup",
  "auth.password_changed": "Password changed",
  "auth.password_reset": "Password reset (rescue command)",
  "auth.sessions_revoked": "All sessions revoked",
  "auth.tokens_revoked": "API tokens revoked",
  "auth.accounts_reset": "Accounts reset (rescue command)",
  "project.created": "Project created",
  "project.updated": "Project updated",
  "project.started": "Project started",
  "project.stopped": "Project stopped",
  "project.restarted": "Project restarted",
  "project.duplicated": "Project duplicated",
  "project.renamed": "Project renamed",
  "project.deleted": "Project deleted",
  "project.failed": "Project creation failed",
  "project.dbtool_opened": "Database browser opened",
  "project.image_rolled_back": "Image rolled back",
  "project.image_latest": "Image set to latest",
  "settings.changed": "Settings changed",
  "docker.images_pruned": "Unused images removed",
  "docker.orphans_removed": "Orphaned resources removed",
  "logs.history_cleared": "Log history deleted",
  "cache.cleared": "Package cache cleared",
  "backup.created": "Backup created",
  "backup.restored": "Backup restored",
  "backup.deleted": "Backup deleted",
  "instance.backup_created": "Instance backup created",
  "instance.backup_uploaded": "Instance backup uploaded",
  "instance.backup_deleted": "Instance backup deleted",
  "instance.restore_scheduled": "Instance restore scheduled",
  "instance.restored": "Instance restored",
  "database.credentials_viewed": "Database credentials viewed",
  "database.password_rotated": "Database password rotated",
  "database.created": "Database created",
  "database.dropped": "Database dropped",
  "database.cloned": "Database cloned",
  "terminal.opened": "Terminal opened",
  "project.exec": "Command run",
  "action.run": "Action run",
  "test.run": "Tests run",
  "cron.run": "Cron job run",
  "action.finished": "Action finished",
  "git.deploy_key_generated": "Deploy key generated",
  "git.clone": "Repository cloned",
  "git.pull": "Repository pulled",
  "git.checkout": "Branch checked out",
  "token.created": "API token created",
  "token.revoked": "API token revoked",
};

export function auditActionLabel(action: string, t: TFunction): string {
  const label = auditActionLabels[action];
  return label ? t(label) : action;
}

/** Who did it: the user, "cli" for rescue commands, Envoryx itself for automatic actions. */
export function auditActor(e: AuditEntry, t: TFunction): string {
  if (e.username) return e.username;
  return t("Envoryx (automatic)");
}

/** A short, readable summary of an entry's details; falls back to the target. */
export function auditDetails(e: AuditEntry, t: TFunction): string {
  const d = e.details ?? {};
  const list = (v: unknown) => (Array.isArray(v) ? v.map(String).join(", ") : "");
  switch (e.action) {
    case "docker.orphans_removed":
      return list(d["removed"]);
    case "docker.images_pruned":
      return t("Images: {{count}}", { count: Number(d["removed"] ?? 0) });
    case "settings.changed":
      return Object.keys(d).join(", ");
    case "cache.cleared":
      return [typeof d["tool"] === "string" ? d["tool"] : "", t("{{size}} freed", { size: formatBytes(Number(d["freed"] ?? 0)) })].filter(Boolean).join(" · ");
    case "project.updated": {
      const changes = d["changes"];
      const keys = changes && typeof changes === "object" ? Object.keys(changes as object).join(", ") : "";
      return typeof d["name"] === "string" ? (keys ? `${d["name"]} · ${keys}` : String(d["name"])) : keys;
    }
    default:
      if (typeof d["name"] === "string") return String(d["name"]);
      return e.targetType ? `${e.targetType} ${e.targetId.slice(0, 8)}` : "—";
  }
}
