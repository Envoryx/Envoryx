import { Link } from "react-router-dom";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { MetricRange, ProjectUsageSummary } from "@/api/types";
import { Card, CardHeader, ErrorState, Spinner } from "@/components/ui";
import { RangePicker } from "@/features/projects/ResourcesTab";
import { formatBytes } from "@/lib/format";
import { errorText } from "@/lib/errors";

const cores = (v: number) => (v / 100 >= 10 ? (v / 100).toFixed(0) : (v / 100).toFixed(2));

/** A meter against the busiest project: one hue, the track a lighter wash of it. */
function Meter({ value, max, label }: { value: number; max: number; label: string }) {
  const pct = max > 0 ? Math.max(2, (value / max) * 100) : 0;
  return (
    <div className="space-y-1">
      <div className="text-xs tabular-nums text-fg">{label}</div>
      <div className="h-1.5 w-full rounded-full bg-[color-mix(in_oklab,var(--series-1)_15%,transparent)]" aria-hidden>
        <div className="h-full rounded-full bg-[var(--series-1)]" style={{ width: `${pct}%` }} />
      </div>
    </div>
  );
}

/** The project's CPU over the range – a single series, so no legend. */
function Sparkline({ points, from, to }: { points: [number, number, number][]; from: number; to: number }) {
  const w = 96;
  const h = 24;
  const max = Math.max(1, ...points.map((p) => p[1]));
  const d = points.map((p, i) => `${i === 0 ? "M" : "L"}${(((p[0] - from) / Math.max(1, to - from)) * w).toFixed(1)},${(h - 2 - (p[1] / max) * (h - 4)).toFixed(1)}`).join("");
  return (
    <svg width={w} height={h} aria-hidden className="block">
      <path d={d} fill="none" stroke="var(--series-1)" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

/**
 * Which project uses how much: CPU and memory on average and at the peak over the range,
 * the disk space it takes, busiest first.
 */
export function ResourceOverviewCard() {
  const { t } = useTranslation();
  const [range, setRange] = useState<MetricRange>("24h");
  const q = useQuery({
    queryKey: ["metrics-overview", range],
    queryFn: async () => (await api.metricsOverview(range)).overview,
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  });
  const projects: ProjectUsageSummary[] = (q.data?.projects ?? []).filter((p) => p.series.length > 0 || Object.keys(p.disk).length > 0);
  const maxCPU = Math.max(0, ...projects.map((p) => p.cpuAvg));
  const maxMem = Math.max(0, ...projects.map((p) => p.memAvg));
  const disk = (p: ProjectUsageSummary) => (p.disk.volume ?? 0) + (p.disk.files ?? 0) + (p.disk.backups ?? 0);

  return (
    <Card className="mt-6">
      <CardHeader
        title={t("Resource usage by project")}
        description={t("Average and peak of each project's containers together over the range, busiest first. The project's Resources tab shows the history per container.")}
        actions={<RangePicker value={range} onChange={setRange} ranges={["24h", "7d", "30d", "90d"]} wrap={false} />}
      />
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <ErrorState message={errorText(q.error, t)} />
      ) : projects.length === 0 ? (
        <p className="px-5 py-8 text-center text-sm text-muted">{t("Nothing recorded yet. Envoryx samples running containers once a minute and measures the disk space once an hour.")}</p>
      ) : (
        <div className={q.isFetching && q.isPlaceholderData ? "overflow-x-auto opacity-60" : "overflow-x-auto"}>
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-subtle">
              <tr className="border-b border-default">
                <th className="px-5 py-2 font-medium">{t("Project")}</th>
                <th className="w-44 px-3 py-2 font-medium">{t("CPU (cores, average)")}</th>
                <th className="px-3 py-2 text-right font-medium">{t("Peak")}</th>
                <th className="w-44 px-3 py-2 font-medium">{t("Memory (average)")}</th>
                <th className="px-3 py-2 text-right font-medium">{t("Peak")}</th>
                <th className="px-3 py-2 text-right font-medium">{t("Disk space")}</th>
                <th className="px-5 py-2 font-medium">{t("CPU over time")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[var(--border)]">
              {projects.map((p) => (
                <tr key={p.id}>
                  <td className="px-5 py-2">
                    <Link to={`/projects/${encodeURIComponent(p.id)}?tab=Resources`} className="font-medium text-fg hover:underline">
                      {p.name}
                    </Link>
                  </td>
                  <td className="px-3 py-2">
                    <Meter value={p.cpuAvg} max={maxCPU} label={cores(p.cpuAvg)} />
                  </td>
                  <td className="px-3 py-2 text-right text-xs tabular-nums text-muted">{cores(p.cpuMax)}</td>
                  <td className="px-3 py-2">
                    <Meter value={p.memAvg} max={maxMem} label={formatBytes(p.memAvg)} />
                  </td>
                  <td className="px-3 py-2 text-right text-xs tabular-nums text-muted">{formatBytes(p.memMax)}</td>
                  <td className="px-3 py-2 text-right text-xs tabular-nums text-muted" title={t("Volumes {{volumes}} · files {{files}} · backups {{backups}}", { volumes: formatBytes(p.disk.volume ?? 0), files: formatBytes(p.disk.files ?? 0), backups: formatBytes(p.disk.backups ?? 0) })}>
                    {formatBytes(disk(p))}
                  </td>
                  <td className="px-5 py-2">{p.series.length > 1 && q.data && <Sparkline points={p.series} from={q.data.from} to={q.data.to} />}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
