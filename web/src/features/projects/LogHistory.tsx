import { useQuery } from "@tanstack/react-query";
import { clsx } from "clsx";
import { Download, RefreshCw, Search, ZoomOut } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "@/api/client";
import type { LogBucket, LogFilter, LogLevel, LogSummary } from "@/api/types";
import { Button, ErrorState, Input, Select, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatDateTime } from "@/lib/format";
import { LogLinesTable } from "./LogLines";

const PRESETS = ["15m", "1h", "6h", "24h", "7d", "30d", "all"] as const;
type Preset = (typeof PRESETS)[number];
type Range = { preset: Preset } | { from: string; to: string };

/** A download link that looks like a small secondary button. */
export const downloadClass = "inline-flex h-8 items-center justify-center gap-1.5 whitespace-nowrap rounded-md border border-default bg-elevated px-2.5 text-xs font-medium text-fg hover:bg-muted";

/** How many lines the history shows; the download has all of them. */
export const HISTORY_LINES = 2000;

function toFilter(range: Range, q: string, level: LogLevel): LogFilter {
  const f: LogFilter = { q: q.trim(), level };
  if ("preset" in range) {
    if (range.preset !== "all") f.since = range.preset;
  } else {
    f.since = range.from;
    f.until = range.to;
  }
  return f;
}

/** Value of a datetime-local input for an ISO time (local time, minute precision). */
function localInput(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/**
 * Searches a service's past output: time range, text, level, error frequency over time
 * and the most frequent problems. Everything is filtered on the server.
 */
export function LogHistory({ projectId, kind }: { projectId: string; kind: string }) {
  const { t } = useTranslation();
  const [range, setRange] = useState<Range>({ preset: "24h" });
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [level, setLevel] = useState<LogLevel>("");
  const filter = useMemo(() => toFilter(range, q, level), [range, q, level]);

  const lines = useQuery({
    queryKey: ["projects", projectId, "logs", kind, filter],
    queryFn: () => api.projects.logs(projectId, kind, filter, HISTORY_LINES),
    staleTime: Infinity,
  });
  const stats = useQuery({
    queryKey: ["projects", projectId, "logs", kind, "stats", filter],
    queryFn: () => api.projects.logStats(projectId, kind, filter),
    staleTime: Infinity,
  });
  const refresh = () => void Promise.all([lines.refetch(), stats.refetch()]);

  const zoom = (b: LogBucket, seconds: number) => {
    const from = new Date(b.start);
    setRange({ from: from.toISOString(), to: new Date(from.getTime() + seconds * 1000 - 1).toISOString() });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <form
        className="flex flex-wrap items-center gap-1.5 border-b border-default px-3 py-2"
        onSubmit={(e) => {
          e.preventDefault();
          setQ(search);
        }}
      >
        <Select
          aria-label={t("Time range")}
          className="h-8 w-44! text-xs"
          value={"preset" in range ? range.preset : "custom"}
          onChange={(e) => {
            const v = e.target.value;
            if (v === "custom") {
              const to = new Date();
              setRange({ from: new Date(to.getTime() - 3600_000).toISOString(), to: to.toISOString() });
            } else {
              setRange({ preset: v as Preset });
            }
          }}
        >
          <option value="15m">{t("Last 15 minutes")}</option>
          <option value="1h">{t("Last hour")}</option>
          <option value="6h">{t("Last 6 hours")}</option>
          <option value="24h">{t("Last 24 hours")}</option>
          <option value="7d">{t("Last 7 days")}</option>
          <option value="30d">{t("Last 30 days")}</option>
          <option value="all">{t("Everything")}</option>
          <option value="custom">{t("Custom range")}</option>
        </Select>
        {!("preset" in range) && (
          <>
            <Input
              type="datetime-local"
              aria-label={t("From")}
              className="h-8 w-44! text-xs"
              value={localInput(range.from)}
              onChange={(e) => e.target.value && setRange({ ...range, from: new Date(e.target.value).toISOString() })}
            />
            <span className="text-xs text-subtle">–</span>
            <Input
              type="datetime-local"
              aria-label={t("To")}
              className="h-8 w-44! text-xs"
              value={localInput(range.to)}
              onChange={(e) => e.target.value && setRange({ ...range, to: new Date(e.target.value).toISOString() })}
            />
          </>
        )}
        <Select aria-label={t("Level")} className="h-8 w-44! text-xs" value={level} onChange={(e) => setLevel(e.target.value as LogLevel)}>
          <option value="">{t("All lines")}</option>
          <option value="warn">{t("Warnings and errors")}</option>
          <option value="error">{t("Errors only")}</option>
        </Select>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-subtle" aria-hidden />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} onBlur={() => setQ(search)} placeholder={t("Search… (Enter)")} aria-label={t("Search history")} className="h-8 w-48! pl-7 text-xs" />
        </div>
        <div className="ml-auto flex items-center gap-1.5">
          <Button type="button" size="sm" onClick={refresh} icon={<RefreshCw className={clsx("size-3.5", (lines.isFetching || stats.isFetching) && "animate-spin")} />}>
            {t("Refresh")}
          </Button>
          <a href={api.projects.logDownloadUrl(projectId, kind, filter)} download className={downloadClass}>
            <Download className="size-3.5" aria-hidden />
            {t("Download")}
          </a>
        </div>
      </form>

      <div className="border-b border-default px-3 py-2">
        {stats.isPending ? (
          <div className="flex h-28 items-center justify-center">
            <Spinner />
          </div>
        ) : stats.isError ? (
          <ErrorState message={errorText(stats.error, t)} />
        ) : (
          <>
            <LogHistogram summary={stats.data} onZoom={zoom} />
            {!("preset" in range) && (
              <button type="button" onClick={() => setRange({ preset: "24h" })} className="mt-1 inline-flex items-center gap-1 text-[11px] text-accent-600 hover:underline dark:text-accent-300">
                <ZoomOut className="size-3" aria-hidden /> {t("Back to the last 24 hours")}
              </button>
            )}
            <TopMessages
              summary={stats.data}
              onSearch={(text) => {
                setSearch(text);
                setQ(text);
              }}
            />
          </>
        )}
      </div>

      <div className="relative min-h-0 flex-1 overflow-auto bg-[#0f1115] font-mono text-[12px] leading-5 text-zinc-200">
        {lines.isPending ? (
          <p className="p-4 text-zinc-500">{t("Loading…")}</p>
        ) : lines.isError ? (
          <p className="p-4 text-red-400" role="alert">
            {errorText(lines.error, t)}
          </p>
        ) : lines.data.lines.length === 0 ? (
          <p className="p-4 text-zinc-500">{t("No lines in this range.")}</p>
        ) : (
          <LogLinesTable withDate lines={lines.data.lines.map((l, i) => ({ ...l, id: i }))} />
        )}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-default px-3 py-1.5 text-[11px] text-subtle">
        <span>
          {lines.data &&
            (lines.data.truncated
              ? t("Last {{shown}} of {{total}} matching lines – the download has all of them", { shown: lines.data.lines.length, total: lines.data.matched })
              : t("{{count}} lines", { count: lines.data.matched }))}
        </span>
        {lines.data && (
          <span title={lines.data.source === "history" ? t("Kept across container restarts and recreations") : t("Only what Docker still holds for the current container – the log history is off or has nothing yet")}>
            {lines.data.source === "history"
              ? lines.data.oldest
                ? t("Log history since {{date}}", { date: formatDateTime(lines.data.oldest) })
                : t("Log history")
              : t("Current container only")}
          </span>
        )}
      </div>
    </div>
  );
}

function bucketLabel(b: LogBucket, seconds: number): string {
  const from = new Date(b.start);
  const date: Intl.DateTimeFormatOptions = { day: "2-digit", month: "2-digit" };
  if (seconds >= 86400) return from.toLocaleDateString(undefined, date);
  const time: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", hour12: false };
  const to = new Date(from.getTime() + seconds * 1000);
  return `${from.toLocaleString(undefined, { ...date, ...time })} – ${to.toLocaleTimeString(undefined, time)}`;
}

/**
 * Lines per time slot, stacked: errors at the baseline, then warnings, then the rest.
 * A bar zooms into its slot.
 */
export function LogHistogram({ summary, onZoom }: { summary: LogSummary; onZoom: (b: LogBucket, seconds: number) => void }) {
  const { t } = useTranslation();
  const [hover, setHover] = useState<number | null>(null);
  const max = Math.max(1, ...summary.buckets.map((b) => b.total));
  const h = hover !== null ? summary.buckets[hover] : undefined;
  const pct = (n: number) => `${(n / max) * 100}%`;

  return (
    <figure className="space-y-1.5">
      <figcaption className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
        <span className="font-medium text-fg">{t("Error frequency")}</span>
        <Legend swatch="bg-red-500" label={t("Errors")} value={summary.errors} />
        <Legend swatch="bg-amber-400" label={t("Warnings")} value={summary.warnings} />
        <Legend swatch="bg-zinc-300 dark:bg-zinc-600" label={t("Other lines")} value={summary.total - summary.errors - summary.warnings} />
        <span className="ml-auto text-subtle" aria-live="polite">
          {h ? `${bucketLabel(h, summary.bucketSeconds)}: ${t("Errors: {{errors}} · warnings: {{warnings}} · lines: {{total}}", { errors: h.errors, warnings: h.warnings, total: h.total })}` : t("Click a bar to zoom in")}
        </span>
      </figcaption>
      <div className="flex gap-2">
        <div className="flex h-24 flex-col justify-between text-right text-[10px] tabular-nums text-subtle" aria-hidden>
          <span>{max}</span>
          <span>0</span>
        </div>
        <div className="relative flex-1">
          <div className="absolute inset-x-0 top-0 border-t border-dashed border-default" aria-hidden />
          <div
            className="flex h-24 items-end gap-[2px] border-b border-default"
            role="group"
            aria-label={t("Errors: {{errors}} · warnings: {{warnings}} · lines: {{total}}", { errors: summary.errors, warnings: summary.warnings, total: summary.total })}
            onMouseLeave={() => setHover(null)}
          >
            {summary.buckets.map((b, i) => (
              <button
                key={b.start}
                type="button"
                className={clsx("flex h-full min-w-0 flex-1 flex-col-reverse gap-px rounded-t-[3px] focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent-500", hover === i && "bg-muted")}
                onMouseEnter={() => setHover(i)}
                onFocus={() => setHover(i)}
                onClick={() => onZoom(b, summary.bucketSeconds)}
                disabled={b.total === 0}
                aria-label={`${bucketLabel(b, summary.bucketSeconds)}: ${t("Errors: {{errors}} · warnings: {{warnings}} · lines: {{total}}", { errors: b.errors, warnings: b.warnings, total: b.total })}`}
              >
                {b.errors > 0 && <span className="w-full shrink-0 rounded-b-none bg-red-500" style={{ height: pct(b.errors) }} />}
                {b.warnings > 0 && <span className="w-full shrink-0 bg-amber-400" style={{ height: pct(b.warnings) }} />}
                {b.total - b.errors - b.warnings > 0 && <span className="w-full shrink-0 rounded-t-[3px] bg-zinc-300 dark:bg-zinc-600" style={{ height: pct(b.total - b.errors - b.warnings) }} />}
              </button>
            ))}
          </div>
          <div className="mt-0.5 flex justify-between text-[10px] text-subtle" aria-hidden>
            <span>{formatDateTime(summary.from)}</span>
            <span>{formatDateTime(summary.to)}</span>
          </div>
        </div>
      </div>
    </figure>
  );
}

function Legend({ swatch, label, value }: { swatch: string; label: string; value: number }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-muted">
      <span className={clsx("size-2.5 rounded-sm", swatch)} aria-hidden />
      {label}
      <span className="font-medium tabular-nums text-fg">{value}</span>
    </span>
  );
}

/** The most frequent errors and warnings; a click searches for the message. */
function TopMessages({ summary, onSearch }: { summary: LogSummary; onSearch: (text: string) => void }) {
  const { t } = useTranslation();
  if (summary.top.length === 0) return null;
  return (
    <details className="mt-2 text-xs">
      <summary className="cursor-pointer text-muted hover:text-fg">{t("Most frequent problems ({{n}})", { n: summary.top.length })}</summary>
      <ul className="mt-1.5 max-h-48 space-y-1 overflow-auto">
        {summary.top.map((m) => (
          <li key={m.pattern} className="flex items-start gap-2">
            <span className={clsx("mt-0.5 shrink-0 rounded px-1.5 font-medium tabular-nums", m.level === "error" ? "bg-red-500/15 text-red-700 dark:text-red-300" : "bg-amber-500/15 text-amber-700 dark:text-amber-300")}>
              {m.count}×
            </span>
            <button type="button" className="min-w-0 flex-1 truncate text-left font-mono text-fg hover:underline" title={m.example} onClick={() => onSearch(searchText(m.example))}>
              {m.pattern}
            </button>
            <span className="shrink-0 text-subtle">{t("last {{time}}", { time: formatDateTime(m.last) })}</span>
          </li>
        ))}
      </ul>
    </details>
  );
}

/** A search for a grouped message: its longest stretch without masked parts. */
export function searchText(example: string): string {
  const parts = example.split(/\d[\d:.,-]*|[0-9a-f]{12,}/i).map((s) => s.trim());
  return parts.reduce((a, b) => (b.length > a.length ? b : a), "").slice(0, 80);
}
