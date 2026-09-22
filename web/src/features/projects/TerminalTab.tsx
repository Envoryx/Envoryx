import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { RotateCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";
import type { Project } from "@/api/types";
import { Button, Card } from "@/components/ui";
import { serviceLabel } from "@/lib/format";

type SessionState = "connecting" | "open" | "closed" | "error";

const theme = {
  background: "#0f1115",
  foreground: "#e6e8ee",
  cursor: "#a5b4fc",
  selectionBackground: "#4f46e544",
  black: "#1d2129",
  brightBlack: "#6b7280",
};

/**
 * Browser terminal attached to a PTY exec session in a project container. Keystrokes go
 * out as binary frames, output comes back as binary frames, resizes as JSON text frames.
 */
function useTerminalSession(projectId: string, kind: string, generation: number) {
  const host = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<SessionState>("connecting");
  const [message, setMessage] = useState<string | null>(null);

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    setState("connecting");
    setMessage(null);

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, 'JetBrains Mono', 'SF Mono', Menlo, Consolas, monospace",
      fontSize: 13,
      lineHeight: 1.2,
      theme,
      scrollback: 5000,
      allowProposedApi: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon());
    term.open(el);
    fit.fit();

    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const url = `${proto}://${window.location.host}/api/v1/projects/${encodeURIComponent(projectId)}/services/${kind}/terminal/ws?cols=${term.cols}&rows=${term.rows}`;
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    const encoder = new TextEncoder();

    ws.onopen = () => {
      setState("open");
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(ev.data));
        return;
      }
      try {
        const m = JSON.parse(ev.data as string) as { type: string; message?: string };
        if (m.type === "error") setMessage(m.message ?? "session error");
        if (m.type === "exit") setState("closed");
      } catch {
        /* ignore */
      }
    };
    ws.onerror = () => setState("error");
    ws.onclose = (ev) => {
      setState((s) => (s === "error" ? s : "closed"));
      if (ev.code !== 1000 && ev.reason) setMessage(ev.reason);
      term.write("\r\n\x1b[90m[session closed]\x1b[0m\r\n");
    };

    const onData = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(data));
    });
    const onBinary = term.onBinary((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(Uint8Array.from(data, (c) => c.charCodeAt(0)));
    });
    const onResize = term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows }));
    });
    const observer = new ResizeObserver(() => {
      const dims = fit.proposeDimensions();
      if (dims && (dims.cols !== term.cols || dims.rows !== term.rows)) fit.fit();
    });
    observer.observe(el);

    return () => {
      observer.disconnect();
      onData.dispose();
      onBinary.dispose();
      onResize.dispose();
      ws.close();
      term.dispose();
    };
  }, [projectId, kind, generation]);

  return { host, state, message };
}

export function TerminalTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const services = project.services.filter((s) => s.enabled);
  // Open on the application container (PHP, else Node); the web container is the last resort.
  const [kind, setKind] = useState<string>(project.appService ?? services[0]?.kind ?? "web");
  const [generation, setGeneration] = useState(0);
  const running = project.status.services.find((s) => s.kind === kind)?.running ?? false;
  const { host, state, message } = useTerminalSession(project.id, kind, generation);

  return (
    <Card className="flex h-[70vh] min-h-[24rem] flex-col overflow-hidden">
      <div className="flex flex-wrap items-center gap-2 border-b border-default px-3 py-2">
        <div className="flex items-center gap-1" role="tablist" aria-label={t("Container")}>
          {services.map((s) => (
            <button
              key={s.kind}
              role="tab"
              aria-selected={kind === s.kind}
              onClick={() => setKind(s.kind)}
              className={clsx("rounded-md px-2.5 py-1.5 text-xs font-medium", kind === s.kind ? "bg-accent-500/10 text-accent-600 dark:text-accent-300" : "text-muted hover:bg-muted hover:text-fg")}
            >
              {serviceLabel(s.kind, s.version, s.variant)}
            </button>
          ))}
        </div>
        <span className="ml-1 inline-flex items-center gap-1.5 text-xs text-muted">
          <span className={clsx("size-2 rounded-full", state === "open" ? "bg-emerald-500" : state === "connecting" ? "bg-amber-500 animate-pulse" : "bg-zinc-400")} aria-hidden />
          {state === "open" ? t("connected") : t(state)}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <span className="text-[11px] text-subtle">
            {kind === "php" || kind === "node" ? t("runs as the project owner in /var/www/html") : t("runs as root")}
          </span>
          <Button size="sm" onClick={() => setGeneration((g) => g + 1)} icon={<RotateCw className="size-3.5" />} disabled={!running}>
            {t("New session")}
          </Button>
        </div>
      </div>
      {message && (
        <p className="border-b border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-600 dark:text-red-400" role="alert">
          {message}
        </p>
      )}
      {!running ? (
        <p className="p-4 text-sm text-muted">{t("The {{service}} container is not running. Start the project to open a shell.", { service: serviceLabel(kind) })}</p>
      ) : (
        <div ref={host} className="min-h-0 flex-1 overflow-hidden bg-[#0f1115] p-2" data-testid="terminal" />
      )}
    </Card>
  );
}
