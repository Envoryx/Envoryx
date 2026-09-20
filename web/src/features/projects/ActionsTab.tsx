import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Play, Square, Terminal as TerminalIcon } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { useProjectActions } from "@/api/hooks";
import type { ActionInfo, Project } from "@/api/types";
import { Badge, Button, Card, CardHeader, Code, Dialog, ErrorState, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";

type RunState = "idle" | "running" | "finished" | "failed";

interface Run {
  action: ActionInfo;
  state: RunState;
  exitCode: number | null;
  startedAt: number;
  message?: string;
}

function useActionRunner(projectId: string) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const [run, setRun] = useState<Run | null>(null);

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const t = new Terminal({
      disableStdin: true,
      convertEol: true,
      fontFamily: "ui-monospace, 'JetBrains Mono', 'SF Mono', Menlo, Consolas, monospace",
      fontSize: 12.5,
      theme: { background: "#0f1115", foreground: "#e6e8ee" },
      scrollback: 10000,
    });
    const f = new FitAddon();
    t.loadAddon(f);
    t.open(el);
    f.fit();
    term.current = t;
    fit.current = f;
    const obs = new ResizeObserver(() => {
      const dims = f.proposeDimensions();
      if (dims && (dims.cols !== t.cols || dims.rows !== t.rows)) f.fit();
    });
    obs.observe(el);
    return () => {
      obs.disconnect();
      socket.current?.close();
      t.dispose();
      term.current = null;
    };
  }, []);

  const start = (action: ActionInfo) => {
    socket.current?.close();
    const t = term.current;
    if (!t) return;
    t.reset();
    t.writeln(`\x1b[90m$ ${action.cmd.join(" ")}\x1b[0m`);
    setRun({ action, state: "running", exitCode: null, startedAt: Date.now() });
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${window.location.host}/api/v1/projects/${encodeURIComponent(projectId)}/actions/${encodeURIComponent(action.id)}/ws?cols=${t.cols}&rows=${t.rows}`);
    ws.binaryType = "arraybuffer";
    socket.current = ws;
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        t.write(new Uint8Array(ev.data));
        return;
      }
      try {
        const m = JSON.parse(ev.data as string) as { type: string; code?: number; message?: string };
        if (m.type === "exit") {
          const code = m.code ?? -1;
          t.writeln(code === 0 ? "\r\n\x1b[32m✔ finished (exit 0)\x1b[0m" : `\r\n\x1b[31m✘ exited with code ${code}\x1b[0m`);
          setRun((r) => (r ? { ...r, state: code === 0 ? "finished" : "failed", exitCode: code } : r));
        } else if (m.type === "error") {
          setRun((r) => (r ? { ...r, state: "failed", message: m.message ?? "error" } : r));
        }
      } catch {
        /* ignore */
      }
    };
    ws.onerror = () => setRun((r) => (r && r.state === "running" ? { ...r, state: "failed", message: "connection failed" } : r));
    ws.onclose = (ev) => {
      setRun((r) => {
        if (!r || r.state !== "running") return r;
        return { ...r, state: "failed", message: ev.reason || "connection closed" };
      });
    };
  };

  const cancel = () => {
    socket.current?.send(JSON.stringify({ type: "cancel" }));
  };

  return { host, run, start, cancel };
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
        <CardHeader title={t("Actions")} description={t("Predefined commands, run inside the project container as the project owner.")} />
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
