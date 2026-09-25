import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

export type RunState = "idle" | "running" | "finished" | "failed";

export interface LiveRun {
  state: RunState;
  exitCode: number | null;
  startedAt: number;
  message?: string;
}

/**
 * An output-only terminal fed by one of the API's run WebSockets (actions, tests): binary
 * frames are the process output, text frames JSON control messages. onMessage sees every
 * control message; "exit" also ends the run.
 */
export function useLiveRun(onMessage?: (m: Record<string, unknown>) => void) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const handler = useRef(onMessage);
  handler.current = onMessage;
  const [run, setRun] = useState<LiveRun | null>(null);

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

  /** Opens the socket at path (below /api/v1, cols and rows are added) after echoing the command. */
  const start = (path: string, command: string) => {
    socket.current?.close();
    const t = term.current;
    if (!t) return;
    t.reset();
    t.writeln(`\x1b[90m$ ${command}\x1b[0m`);
    setRun({ state: "running", exitCode: null, startedAt: Date.now() });
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const sep = path.includes("?") ? "&" : "?";
    const ws = new WebSocket(`${proto}://${window.location.host}/api/v1${path}${sep}cols=${t.cols}&rows=${t.rows}`);
    ws.binaryType = "arraybuffer";
    socket.current = ws;
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        t.write(new Uint8Array(ev.data));
        return;
      }
      let m: Record<string, unknown>;
      try {
        m = JSON.parse(ev.data as string) as Record<string, unknown>;
      } catch {
        return;
      }
      if (m.type === "exit") {
        const code = typeof m.code === "number" ? m.code : -1;
        t.writeln(code === 0 ? "\r\n\x1b[32m✔ finished (exit 0)\x1b[0m" : `\r\n\x1b[31m✘ exited with code ${code}\x1b[0m`);
        setRun((r) => (r ? { ...r, state: code === 0 ? "finished" : "failed", exitCode: code } : r));
      } else if (m.type === "error") {
        setRun((r) => (r ? { ...r, state: "failed", message: typeof m.message === "string" ? m.message : "error" } : r));
      }
      handler.current?.(m);
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
