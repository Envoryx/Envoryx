import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { ArrowDownToLine, Download, Eraser, History, Pause, Play, Radio, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/api/client";
import type { LogLevel, Project } from "@/api/types";
import { Badge, Button, Card, Input, Select } from "@/components/ui";
import { serviceLabel } from "@/lib/format";
import { downloadClass, LogHistory } from "./LogHistory";
import { type LineFilter, lineMatches, LogLinesTable, type ShownLine } from "./LogLines";

type Line = ShownLine & { id: number };

const MAX_LINES = 5000;

type StreamState = "connecting" | "live" | "closed" | "error";

/**
 * Streams container logs over a WebSocket into a bounded ring buffer. While paused,
 * incoming lines are parked and appended on resume so nothing is lost (up to the buffer
 * limit).
 */
function useLogStream(projectId: string, kind: string | null, paused: boolean) {
  const [lines, setLines] = useState<Line[]>([]);
  const [state, setState] = useState<StreamState>("connecting");
  const [error, setError] = useState<string | null>(null);
  const pending = useRef<Line[]>([]);
  const pausedRef = useRef(paused);
  const seq = useRef(0);
  pausedRef.current = paused;

  const push = useCallback((batch: Line[]) => {
    setLines((prev) => {
      const next = prev.length + batch.length > MAX_LINES ? [...prev.slice(prev.length + batch.length - MAX_LINES), ...batch] : [...prev, ...batch];
      return next;
    });
  }, []);

  useEffect(() => {
    if (!paused && pending.current.length > 0) {
      push(pending.current);
      pending.current = [];
    }
  }, [paused, push]);

  useEffect(() => {
    if (!kind) return;
    setLines([]);
    pending.current = [];
    setError(null);
    setState("connecting");
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${window.location.host}/api/v1/projects/${encodeURIComponent(projectId)}/services/${kind}/logs/ws?tail=500`);
    let batch: Line[] = [];
    let flushTimer: number | null = null;
    const flush = () => {
      flushTimer = null;
      if (batch.length === 0) return;
      const b = batch;
      batch = [];
      if (pausedRef.current) {
        pending.current = [...pending.current, ...b].slice(-MAX_LINES);
      } else {
        push(b);
      }
    };
    ws.onopen = () => setState("live");
    ws.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data as string) as { type: string; time?: string; stream?: "stdout" | "stderr"; text?: string; level?: LogLevel; message?: string };
        if (m.type === "line") {
          batch.push({ id: ++seq.current, time: m.time ?? "", stream: m.stream ?? "stdout", text: m.text ?? "", level: m.level ?? "" });
          if (flushTimer === null) flushTimer = window.setTimeout(flush, 50);
        } else if (m.type === "error") {
          setError(m.message ?? "stream error");
          setState("error");
        }
      } catch {
        /* ignore malformed frames */
      }
    };
    ws.onerror = () => setState("error");
    ws.onclose = (ev) => {
      flush();
      setState((s) => (s === "error" ? s : "closed"));
      if (ev.code === 1008 || ev.code === 1011) setError(ev.reason || "connection closed");
    };
    return () => {
      if (flushTimer !== null) window.clearTimeout(flushTimer);
      ws.close();
    };
  }, [projectId, kind, push]);

  const clear = useCallback(() => {
    setLines([]);
    pending.current = [];
  }, []);

  return { lines, state, error, clear, pendingCount: pending.current.length };
}

export function LogsTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const services = [
    ...project.services.filter((s) => s.enabled).map((s) => ({ kind: s.kind, label: serviceLabel(s.kind, s.version, s.variant) })),
    ...project.status.services.filter((s) => s.kind === "worker" && s.workerId).map((s) => ({ kind: `worker:${s.workerId}`, label: t("Worker {{name}}", { name: s.variant }) })),
  ];
  const [kind, setKind] = useState<string | null>(services[0]?.kind ?? null);
  const [mode, setMode] = useState<"live" | "history">("live");

  if (services.length === 0 || !kind) {
    return <p className="text-sm text-muted">This project has no services.</p>;
  }

  return (
    <Card className="flex h-[75vh] min-h-[28rem] flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-default px-3 py-2">
        <div className="flex flex-wrap items-center gap-1" role="tablist" aria-label={t("Service")}>
          {services.map((s) => (
            <button
              key={s.kind}
              role="tab"
              aria-selected={kind === s.kind}
              onClick={() => setKind(s.kind)}
              className={clsx("rounded-md px-2.5 py-1.5 text-xs font-medium", kind === s.kind ? "bg-accent-500/10 text-accent-600 dark:text-accent-300" : "text-muted hover:bg-muted hover:text-fg")}
            >
              {s.label}
            </button>
          ))}
        </div>
        <div className="ml-auto inline-flex rounded-md border border-default p-0.5" role="radiogroup" aria-label={t("View")}>
          {(
            [
              ["live", t("Live"), Radio],
              ["history", t("History"), History],
            ] as const
          ).map(([m, label, Icon]) => (
            <button
              key={m}
              role="radio"
              aria-checked={mode === m}
              onClick={() => setMode(m)}
              className={clsx("inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-medium", mode === m ? "bg-muted text-fg" : "text-muted hover:text-fg")}
            >
              <Icon className="size-3.5" aria-hidden />
              {label}
            </button>
          ))}
        </div>
      </div>
      {mode === "live" ? <LiveLogs project={project} kind={kind} /> : <LogHistory key={kind} projectId={project.id} kind={kind} />}
    </Card>
  );
}

/** The live stream of one service, with a client-side filter over the buffer. */
function LiveLogs({ project, kind }: { project: Project; kind: string }) {
  const { t } = useTranslation();
  const [paused, setPaused] = useState(false);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<LineFilter>("all");
  const [autoScroll, setAutoScroll] = useState(true);
  const { lines, state, error, clear } = useLogStream(project.id, kind, paused);
  const viewport = useRef<HTMLDivElement>(null);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return lines.filter((l) => lineMatches(l, filter, q));
  }, [lines, query, filter]);

  useEffect(() => {
    if (autoScroll && !paused && viewport.current) {
      viewport.current.scrollTop = viewport.current.scrollHeight;
    }
  }, [filtered, autoScroll, paused]);

  const onScroll = () => {
    const el = viewport.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    setAutoScroll(atBottom);
  };

  return (
    <>
      <div className="flex flex-wrap items-center gap-1.5 border-b border-default px-3 py-2">
        <span className="mr-1 inline-flex items-center gap-1.5 text-xs text-muted">
          <span className={clsx("size-2 rounded-full", state === "live" ? "bg-emerald-500" : state === "connecting" ? "bg-amber-500 animate-pulse" : "bg-zinc-400")} aria-hidden />
          {state === "live" ? (paused ? t("paused") : t("live")) : t(state)}
        </span>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-subtle" aria-hidden />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("Search…")} aria-label={t("Search logs")} className="h-8 w-48 pl-7 text-xs" />
        </div>
        <Select aria-label={t("Show")} className="h-8 w-44! text-xs" value={filter} onChange={(e) => setFilter(e.target.value as LineFilter)}>
          <option value="all">{t("All lines")}</option>
          <option value="warn">{t("Warnings and errors")}</option>
          <option value="error">{t("Errors only")}</option>
          <option value="stderr">{t("stderr only")}</option>
        </Select>
        <div className="ml-auto flex items-center gap-1.5">
          <Button size="sm" onClick={() => setPaused(!paused)} icon={paused ? <Play className="size-3.5" /> : <Pause className="size-3.5" />} aria-pressed={paused}>
            {paused ? t("Resume") : t("Pause")}
          </Button>
          <Button size="sm" onClick={clear} icon={<Eraser className="size-3.5" />}>
            {t("Clear")}
          </Button>
          <a href={api.projects.logDownloadUrl(project.id, kind)} download className={downloadClass} title={t("Everything this service has logged")}>
            <Download className="size-3.5" aria-hidden />
            {t("Download")}
          </a>
        </div>
      </div>
      {error && (
        <p className="border-b border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-600 dark:text-red-400" role="alert">
          {error}
        </p>
      )}
      <div ref={viewport} onScroll={onScroll} className="relative flex-1 overflow-auto bg-[#0f1115] font-mono text-[12px] leading-5 text-zinc-200" aria-live="off">
        {filtered.length === 0 ? (
          <p className="p-4 text-zinc-500">{lines.length === 0 ? t("No output yet.") : t("No lines match the filter.")}</p>
        ) : (
          <LogLinesTable lines={filtered} />
        )}
        {!autoScroll && (
          <button
            onClick={() => {
              setAutoScroll(true);
              viewport.current?.scrollTo({ top: viewport.current.scrollHeight });
            }}
            className="sticky bottom-3 left-full mr-3 inline-flex items-center gap-1 rounded-full bg-accent-600 px-3 py-1 text-xs font-medium text-white shadow"
          >
            <ArrowDownToLine className="size-3.5" /> {t("Follow")}
          </button>
        )}
      </div>
      <div className="flex items-center justify-between border-t border-default px-3 py-1.5 text-[11px] text-subtle">
        <span>
          {filtered.length === lines.length ? t("{{count}} lines", { count: lines.length }) : t("{{shown}} of {{total}} lines", { shown: filtered.length, total: lines.length })} · {t("buffer")} {MAX_LINES}
        </span>
        <Badge>{kind}</Badge>
      </div>
    </>
  );
}
