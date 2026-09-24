import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { useTranslation } from "react-i18next";

/** A series: points are [unix seconds, value]; null is a gap (no sample). */
export interface Series {
  key: string;
  label: string;
  /** Categorical slot 1–8 (fixed per entity, never by rank). */
  slot: number;
  points: [number, number | null][];
}

export const seriesColor = (slot: number) => `var(--series-${((slot - 1) % 8) + 1})`;

const PAD = { top: 8, right: 12, bottom: 22, left: 56 };

/** Clean axis steps: 1, 2, 2.5, 5 × 10^n. */
function niceMax(max: number, ticks: number): { max: number; step: number } {
  if (!(max > 0)) return { max: 1, step: 0.25 };
  const raw = max / ticks;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? 10 * mag;
  return { max: Math.ceil(max / step) * step, step };
}

function timeTicks(from: number, to: number, width: number): number[] {
  const count = Math.max(2, Math.min(6, Math.floor(width / 110)));
  const span = to - from;
  const steps = [300, 900, 1800, 3600, 3 * 3600, 6 * 3600, 12 * 3600, 86400, 2 * 86400, 7 * 86400, 14 * 86400, 30 * 86400];
  const step = steps.find((s) => span / s <= count) ?? 60 * 86400;
  const out: number[] = [];
  // Days start at local midnight; smaller steps at round local times.
  const offset = new Date(from * 1000).getTimezoneOffset() * 60;
  for (let t = Math.ceil((from - offset) / step) * step + offset; t <= to; t += step) out.push(t);
  return out;
}

export function formatTick(ts: number, span: number): string {
  const d = new Date(ts * 1000);
  if (span > 2 * 86400) return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function formatWhen(ts: number): string {
  return new Date(ts * 1000).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
}

/**
 * A line chart for a time range: one y-axis starting at zero, 2px lines, a crosshair
 * that snaps to the nearest sample and a tooltip listing every series there. Gaps (no
 * sample, or longer than two buckets) break the line instead of bridging it.
 */
export function LineChart({
  series,
  from,
  to,
  step,
  format,
  height = 180,
  label,
}: {
  series: Series[];
  from: number;
  to: number;
  /** Bucket size in seconds: a longer stretch without samples is a gap. */
  step: number;
  format: (v: number) => string;
  height?: number;
  label: string;
}) {
  const { t } = useTranslation();
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(600);
  const [hover, setHover] = useState<number | null>(null); // index into xs
  useEffect(() => {
    const el = box.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.max(240, Math.floor(e!.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const xs = useMemo(() => [...new Set(series.flatMap((s) => s.points.map((p) => p[0])))].sort((a, b) => a - b), [series]);
  const values = useMemo(() => series.map((s) => new Map(s.points.map(([x, y]) => [x, y]))), [series]);
  const maxY = Math.max(0, ...series.flatMap((s) => s.points.map((p) => p[1] ?? 0)));
  const { max, step: yStep } = niceMax(maxY, 4);
  const plotW = width - PAD.left - PAD.right;
  const plotH = height - PAD.top - PAD.bottom;
  const x = (ts: number) => PAD.left + ((ts - from) / Math.max(1, to - from)) * plotW;
  const y = (v: number) => PAD.top + plotH - (v / max) * plotH;
  const yTicks: number[] = [];
  for (let v = 0; v <= max + yStep / 2; v += yStep) yTicks.push(v);

  // Runs of samples become lines; a sample with no neighbour (a single hourly size, a
  // container that ran for a minute) has no line to be part of and is drawn as a dot.
  const runs = series.map((s) => {
    const out: [number, number][][] = [];
    let prev: number | null = null;
    for (const [ts, v] of s.points) {
      if (v == null) {
        prev = null;
        continue;
      }
      if (prev == null || ts - prev > 2.5 * step) out.push([]);
      out[out.length - 1]!.push([x(ts), y(v)]);
      prev = ts;
    }
    return out;
  });
  const paths = runs.map((rs) => rs.filter((r) => r.length > 1).map((r) => r.map(([px, py], i) => `${i ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)}`).join("")).join(""));
  const dots = runs.map((rs) => rs.filter((r) => r.length === 1).map((r) => r[0]!));

  const nearest = (clientX: number) => {
    const rect = box.current?.getBoundingClientRect();
    if (!rect || xs.length === 0) return null;
    const ts = from + ((clientX - rect.left - PAD.left) / plotW) * (to - from);
    let best = 0;
    for (let i = 1; i < xs.length; i++) if (Math.abs(xs[i]! - ts) < Math.abs(xs[best]! - ts)) best = i;
    return best;
  };
  const onMove = (e: PointerEvent) => setHover(nearest(e.clientX));
  const onKey = (e: KeyboardEvent) => {
    if (xs.length === 0) return;
    if (e.key === "ArrowLeft") setHover((h) => Math.max(0, (h ?? xs.length) - 1));
    else if (e.key === "ArrowRight") setHover((h) => Math.min(xs.length - 1, (h ?? -1) + 1));
    else if (e.key === "Escape") setHover(null);
    else return;
    e.preventDefault();
  };

  const hx = hover != null ? xs[hover] : undefined;
  const tipLeft = hx != null ? x(hx) : 0;
  const span = to - from;

  return (
    <div className="space-y-2">
      {series.length > 1 && (
        <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted" aria-label={t("Legend")}>
          {series.map((s) => (
            <li key={s.key} className="flex items-center gap-1.5">
              <span className="inline-block h-0.5 w-3 rounded-full" style={{ background: seriesColor(s.slot) }} aria-hidden />
              {s.label}
            </li>
          ))}
        </ul>
      )}
      <div ref={box} className="relative" onPointerMove={onMove} onPointerLeave={() => setHover(null)}>
        <svg
          width={width}
          height={height}
          role="img"
          aria-label={label}
          tabIndex={0}
          onKeyDown={onKey}
          onBlur={() => setHover(null)}
          className="block overflow-visible outline-none focus-visible:ring-2 focus-visible:ring-accent-500/40"
        >
          {yTicks.map((v) => (
            <g key={v}>
              <line x1={PAD.left} x2={width - PAD.right} y1={y(v)} y2={y(v)} stroke={v === 0 ? "var(--chart-axis)" : "var(--chart-grid)"} strokeWidth={1} />
              <text x={PAD.left - 6} y={y(v)} dy="0.32em" textAnchor="end" className="fill-[var(--fg-subtle)] text-[10px] tabular-nums">
                {format(v)}
              </text>
            </g>
          ))}
          {timeTicks(from, to, width).map((ts) => (
            <text key={ts} x={x(ts)} y={height - 6} textAnchor="middle" className="fill-[var(--fg-subtle)] text-[10px] tabular-nums">
              {formatTick(ts, span)}
            </text>
          ))}
          {paths.map((d, i) => (
            <path key={series[i]!.key} d={d} fill="none" stroke={seriesColor(series[i]!.slot)} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
          ))}
          {dots.map((ds, i) => ds.map(([cx, cy]) => <circle key={`${series[i]!.key}-${cx}`} cx={cx} cy={cy} r={3} fill={seriesColor(series[i]!.slot)} />))}
          {hx != null && (
            <g>
              <line x1={x(hx)} x2={x(hx)} y1={PAD.top} y2={PAD.top + plotH} stroke="var(--chart-axis)" strokeWidth={1} />
              {series.map((s, i) => {
                const v = values[i]!.get(hx);
                return v == null ? null : <circle key={s.key} cx={x(hx)} cy={y(v)} r={4} fill={seriesColor(s.slot)} stroke="var(--bg-elevated)" strokeWidth={2} />;
              })}
            </g>
          )}
        </svg>
        {hx != null && (
          <div
            role="status"
            className="pointer-events-none absolute top-0 z-10 min-w-40 rounded-md border border-default bg-elevated px-3 py-2 text-xs shadow-lg"
            style={tipLeft > width / 2 ? { right: width - tipLeft + 12 } : { left: tipLeft + 12 }}
          >
            <p className="mb-1 text-subtle">{formatWhen(hx)}</p>
            <ul className="space-y-0.5">
              {series.map((s, i) => {
                const v = values[i]!.get(hx);
                return (
                  <li key={s.key} className="flex items-center gap-2">
                    <span className="inline-block h-0.5 w-3 shrink-0 rounded-full" style={{ background: seriesColor(s.slot) }} aria-hidden />
                    <span className="font-semibold text-fg tabular-nums">{v == null ? "–" : format(v)}</span>
                    <span className="text-muted">{s.label}</span>
                  </li>
                );
              })}
            </ul>
          </div>
        )}
      </div>
    </div>
  );
}

/** The same data as a table, newest first – the way to read values without hovering. */
export function SeriesTable({ series, format }: { series: Series[]; format: (v: number) => string }) {
  const { t } = useTranslation();
  const xs = [...new Set(series.flatMap((s) => s.points.map((p) => p[0])))].sort((a, b) => b - a);
  const values = series.map((s) => new Map(s.points.map(([x, y]) => [x, y])));
  return (
    <div className="max-h-72 overflow-auto rounded-md border border-default">
      <table className="w-full text-xs">
        <thead className="sticky top-0 bg-elevated text-left text-subtle">
          <tr>
            <th className="px-3 py-1.5 font-medium">{t("Time")}</th>
            {series.map((s) => (
              <th key={s.key} className="px-3 py-1.5 text-right font-medium">
                {s.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-[var(--border)]">
          {xs.map((ts) => (
            <tr key={ts}>
              <td className="whitespace-nowrap px-3 py-1 text-muted">{formatWhen(ts)}</td>
              {values.map((m, i) => {
                const v = m.get(ts);
                return (
                  <td key={series[i]!.key} className="px-3 py-1 text-right tabular-nums">
                    {v == null ? "–" : format(v)}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
