import type { TFunction } from "i18next";
import type { Operation, OperationAction } from "@/api/types";

/** "Starting Acme Shop" – what an operation is doing, for trays and headers. */
export function operationTitle(op: Operation, t: TFunction): string {
  const name = op.projectName || op.projectSlug;
  const titles: Record<OperationAction, string> = {
    create: t("Creating {{name}}", { name }),
    start: t("Starting {{name}}", { name }),
    stop: t("Stopping {{name}}", { name }),
    restart: t("Restarting {{name}}", { name }),
    update: t("Applying settings to {{name}}", { name }),
    delete: t("Deleting {{name}}", { name }),
    image: t("Changing the image of {{name}}", { name }),
    backup: t("Backing up {{name}}", { name }),
    restore: t("Restoring {{name}}", { name }),
  };
  return titles[op.action] ?? `${op.action} ${name}`;
}

/** "Acme Shop started" – the outcome of a finished operation. */
export function operationDone(op: Operation, t: TFunction): string {
  const name = op.projectName || op.projectSlug;
  const titles: Record<OperationAction, string> = {
    create: t("{{name}} created", { name }),
    start: t("{{name}} started", { name }),
    stop: t("{{name}} stopped", { name }),
    restart: t("{{name}} restarted", { name }),
    update: t("Settings applied to {{name}}", { name }),
    delete: t("{{name}} deleted", { name }),
    image: t("Image of {{name}} changed", { name }),
    backup: t("Backup of {{name}} finished", { name }),
    restore: t("{{name}} restored", { name }),
  };
  return titles[op.action] ?? `${op.action} ${name}`;
}

/** Short verb for badges next to a project: "Starting…". */
export function operationVerb(action: OperationAction, t: TFunction): string {
  const verbs: Record<OperationAction, string> = {
    create: t("Creating…"),
    start: t("Starting…"),
    stop: t("Stopping…"),
    restart: t("Restarting…"),
    update: t("Applying settings…"),
    delete: t("Deleting…"),
    image: t("Changing image…"),
    backup: t("Backing up…"),
    restore: t("Restoring…"),
  };
  return verbs[action] ?? action;
}

/** The current step, translated; empty when the operation reports none. */
export function operationStep(op: Operation, t: TFunction): string {
  return op.step ? t(op.step, op.stepArgs ?? {}) : "";
}

/** Seconds since the operation started, for an elapsed-time hint. */
export function elapsedSeconds(op: Operation, now = Date.now()): number {
  return Math.max(0, Math.round((now - Date.parse(op.startedAt)) / 1000));
}
