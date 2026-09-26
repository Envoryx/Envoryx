import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Play, Square, Terminal as TerminalIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { useProjectActions } from "@/api/hooks";
import type { ActionInfo, Project } from "@/api/types";
import { Badge, Button, Card, CardHeader, Code, Dialog, ErrorState, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { useLiveRun } from "./useLiveRun";

/** The live runner with the action it runs. */
function useActionRunner(projectId: string) {
  const live = useLiveRun();
  const [action, setAction] = useState<ActionInfo | null>(null);
  const start = (a: ActionInfo) => {
    setAction(a);
    live.start(`/projects/${encodeURIComponent(projectId)}/actions/${encodeURIComponent(a.id)}/ws`, a.cmd.join(" "));
  };
  const run = live.run && action ? { ...live.run, action } : null;
  return { host: live.host, run, start, cancel: live.cancel };
}

export function ActionsTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const actions = useProjectActions(project.id);
  const { host, run, start, cancel } = useActionRunner(project.id);
  const [confirm, setConfirm] = useState<ActionInfo | null>(null);

  const groups = useMemo(() => {
    const map = new Map<string, ActionInfo[]>();
    for (const a of actions.data ?? []) {
      map.set(a.group, [...(map.get(a.group) ?? []), a]);
    }
    return [...map.entries()];
  }, [actions.data]);

  const launch = (a: ActionInfo) => {
    if (a.destructive) setConfirm(a);
    else start(a);
  };

  return (
    <div className="grid gap-6 lg:grid-cols-[18rem_1fr]">
      <Card className="self-start">
        <CardHeader title={t("Actions")} description={t("Predefined commands, run inside the matching runtime container (PHP, Python, Go, Ruby or Node) as the project owner.")} />
        {actions.isPending ? (
          <Spinner />
        ) : actions.isError ? (
          <ErrorState message={errorText(actions.error, t)} />
        ) : (
          <div className="divide-y divide-[var(--border)]">
            {groups.map(([group, items]) => (
              <div key={group} className="px-3 py-2">
                <p className="px-1.5 pb-1 text-[11px] font-semibold uppercase tracking-wide text-subtle">{group}</p>
                <ul className="space-y-0.5">
                  {items.map((a) => (
                    <li key={a.id}>
                      <button
                        onClick={() => launch(a)}
                        disabled={!a.available || run?.state === "running"}
                        title={a.available ? a.description : a.reason}
                        className={clsx(
                          "flex w-full items-center gap-2 rounded-md px-1.5 py-1.5 text-left text-sm",
                          a.available ? "text-fg hover:bg-muted" : "cursor-not-allowed text-subtle",
                          run?.action.id === a.id && "bg-accent-500/10",
                        )}
                      >
                        <Play className="size-3.5 shrink-0" aria-hidden />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-mono text-xs">{a.label}</span>
                          <span className="block truncate text-[11px] text-subtle">{a.available ? a.description : a.reason}</span>
                        </span>
                        {a.destructive && <AlertTriangle className="size-3.5 shrink-0 text-amber-500" aria-label={t("destructive")} />}
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card className="flex h-[70vh] min-h-[24rem] flex-col overflow-hidden">
        <div className="flex items-center gap-2 border-b border-default px-3 py-2 text-xs">
          <TerminalIcon className="size-4 text-subtle" aria-hidden />
          {run ? (
            <>
              <Code>{run.action.label}</Code>
              <Badge tone={run.state === "running" ? "blue" : run.state === "finished" ? "green" : run.state === "failed" ? "red" : "gray"}>
                {run.state === "running" ? t("running") : run.state === "finished" ? t("finished") : run.exitCode !== null ? t("exit {{code}}", { code: run.exitCode }) : (run.message ?? t("failed"))}
              </Badge>
              {run.state === "running" && (
                <Button size="sm" variant="ghost" className="ml-auto" onClick={cancel} icon={<Square className="size-3.5" />}>
                  {t("Cancel")}
                </Button>
              )}
            </>
          ) : (
            <span className="text-muted">{t("Select an action to run it here.")}</span>
          )}
        </div>
        <div ref={host} className="min-h-0 flex-1 overflow-hidden bg-[#0f1115] p-2" data-testid="action-output" />
      </Card>

      <Dialog
        open={confirm !== null}
        onClose={() => setConfirm(null)}
        title={t("Run {{label}}?", { label: confirm?.label ?? "" })}
        description={confirm?.description}
        footer={
          <>
            <Button onClick={() => setConfirm(null)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              onClick={() => {
                if (confirm) start(confirm);
                setConfirm(null);
              }}
            >
              {t("Run")}
            </Button>
          </>
        }
      >
        <p className="text-sm text-muted">{t("This action modifies or deletes data in the project database.")}</p>
      </Dialog>
    </div>
  );
}
