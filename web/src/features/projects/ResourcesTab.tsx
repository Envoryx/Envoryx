import { Table2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { MetricPoint, MetricRange, Project, ProjectMetrics } from "@/api/types";
import { Button, Card, CardHeader, ErrorState, Spinner } from "@/components/ui";
import { LineChart, SeriesTable, type Series } from "@/components/charts/LineChart";
import { formatBytes } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { LimitsCard } from "./LimitsCard";

export const metricRanges: MetricRange[] = ["1h", "6h", "24h", "7d", "30d", "90d", "365d"];

export function RangePicker({ value, onChange, ranges = metricRanges, wrap = true }: { value: MetricRange; onChange: (r: MetricRange) => void; ranges?: MetricRange[]; wrap?: boolean }) {
  const { t } = useTranslation();
  const labels: Record<MetricRange, string> = { "1h": t("1 hour"), "6h": t("6 hours"), "24h": t("24 hours"), "7d": t("7 days"), "30d": t("30 days"), "90d": t("90 days"), "365d": t("1 year") };
  return (
    <div className={`inline-flex shrink-0 ${wrap ? "flex-wrap" : ""} gap-1 rounded-md border border-default bg-elevated p-0.5`} role="radiogroup" aria-label={t("Time range")}>
      {ranges.map((r) => (
        <button
          key={r}
          type="button"
          role="radio"
          aria-checked={value === r}
          onClick={() => onChange(r)}
          className={value === r ? "whitespace-nowrap rounded px-2.5 py-1 text-xs font-medium bg-muted text-fg" : "whitespace-nowrap rounded px-2.5 py-1 text-xs text-muted hover:text-fg"}
        >
          {labels[r]}
        </button>
      ))}
    </div>
  );
}

const cores = (v: number) => (v >= 10 ? v.toFixed(0) : v >= 1 ? v.toFixed(1) : v.toFixed(2));
const rateFmt = (v: number) => `${formatBytes(v)}/s`;

/** At most eight series: the busiest seven keep their own line, the rest become "Other". */
function capSeries(all: Series[], other: string): Series[] {
  if (all.length <= 8) return all;
  const weight = (s: Series) => s.points.reduce((a, p) => a + (p[1] ?? 0), 0);
  const ranked = [...all].sort((a, b) => weight(b) - weight(a));
  const keep = new Set(ranked.slice(0, 7).map((s) => s.key));
  const kept = all.filter((s) => keep.has(s.key));
  const sum = new Map<number, number>();
  for (const s of all) {
    if (keep.has(s.key)) continue;
    for (const [x, y] of s.points) sum.set(x, (sum.get(x) ?? 0) + (y ?? 0));
  }
  return [...kept, { key: "__other", label: other, slot: 8, points: [...sum.entries()].sort((a, b) => a[0] - b[0]) }];
}

function perContainer(m: ProjectMetrics, pick: (p: MetricPoint) => number, other: string): Series[] {
  // Slots follow the container, in name order – a range with fewer containers keeps
  // everyone's color.
  const s = m.containers.map((c, i) => ({ key: c.name, label: c.name, slot: i + 1, points: c.points.map((p) => [p[0], pick(p)] as [number, number]) }));
  return capSeries(s, other);
}

function totals(m: ProjectMetrics, picks: { key: string; label: string; pick: (p: MetricPoint) => number }[]): Series[] {
  return picks.map((p, i) => {
    const sum = new Map<number, number>();
    for (const c of m.containers) for (const pt of c.points) sum.set(pt[0], (sum.get(pt[0]) ?? 0) + p.pick(pt));
    return { key: p.key, label: p.label, slot: i + 1, points: [...sum.entries()].sort((a, b) => a[0] - b[0]) };
  });
}

function ChartCard({ title, description, children, table }: { title: string; description?: string; children: ReactNode; table: ReactNode }) {
  const { t } = useTranslation();
  const [asTable, setAsTable] = useState(false);
  return (
    <Card>
      <CardHeader
        title={title}
        description={description}
        actions={
          <Button size="sm" variant="ghost" onClick={() => setAsTable((v) => !v)} aria-pressed={asTable} icon={<Table2 className="size-3.5" />}>
            {asTable ? t("Chart") : t("Table")}
          </Button>
        }
      />
      <div className="p-4">{asTable ? table : children}</div>
    </Card>
  );
}

/**
 * The project's resource history: CPU and memory per container, network and disk I/O of
 * the project, and the disk space of its volumes, directory and backups.
 */
export function ResourcesTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const [range, setRange] = useState<MetricRange>("24h");
  const q = useQuery({
    queryKey: ["projects", project.id, "metrics", range],
    queryFn: async () => (await api.projects.metrics(project.id, range)).metrics,
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  });
  const other = t("Other");
  const charts = useMemo(() => {
    const m = q.data;
    if (!m) return null;
    const sizeLabel: Record<string, string> = { volume: t("Volumes"), files: t("Project files"), backups: t("Backups") };
    const sizeSeries: Series[] = (["volume", "files", "backups"] as const)
      .map((kind, i) => {
        const sum = new Map<number, number>();
        for (const s of m.sizes.filter((s) => s.kind === kind)) for (const [x, y] of s.points) sum.set(x, (sum.get(x) ?? 0) + y);
        return { key: kind, label: sizeLabel[kind]!, slot: i + 1, points: [...sum.entries()].sort((a, b) => a[0] - b[0]) as [number, number | null][] };
      })
      .filter((s) => s.points.length > 0);
    return {
      cpu: perContainer(m, (p) => p[1] / 100, other),
      mem: perContainer(m, (p) => p[3], other),
      net: totals(m, [
        { key: "rx", label: t("received"), pick: (p) => p[5] },
        { key: "tx", label: t("sent"), pick: (p) => p[6] },
      ]),
      io: totals(m, [
        { key: "read", label: t("read"), pick: (p) => p[7] },
        { key: "write", label: t("written"), pick: (p) => p[8] },
      ]),
      size: sizeSeries,
    };
  }, [q.data, t, other]);

  const m = q.data;
  const empty = m && m.containers.length === 0 && m.sizes.length === 0;
  const now = Math.floor(Date.now() / 1000);
  // Sizes are hourly: over a short range the latest measurement starts the line.
  const sizeFrom = m ? Math.min(m.from, ...m.sizes.flatMap((s) => s.points.map((p) => p[0]))) : 0;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <RangePicker value={range} onChange={setRange} />
        {m && <p className="text-xs text-subtle">{m.res === 60 ? t("One value per minute") : m.res === 300 ? t("Averages over 5 minutes") : t("Averages over an hour")}</p>}
      </div>
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <ErrorState message={errorText(q.error, t)} />
      ) : empty ? (
        <Card className="px-5 py-8 text-center text-sm text-muted">{t("Nothing recorded yet. Envoryx samples running containers once a minute and measures the disk space once an hour.")}</Card>
      ) : (
        charts &&
        m && (
          <div className={q.isFetching && q.isPlaceholderData ? "grid gap-6 opacity-60 xl:grid-cols-2" : "grid gap-6 xl:grid-cols-2"}>
            <ChartCard title={t("CPU")} description={t("Cores used per container (1 = one core fully busy)")} table={<SeriesTable series={charts.cpu} format={cores} />}>
              <LineChart series={charts.cpu} from={m.from} to={now} step={m.res} format={cores} label={t("CPU")} />
            </ChartCard>
            <ChartCard title={t("Memory")} description={t("Memory used per container")} table={<SeriesTable series={charts.mem} format={formatBytes} />}>
              <LineChart series={charts.mem} from={m.from} to={now} step={m.res} format={formatBytes} label={t("Memory")} />
            </ChartCard>
            <ChartCard title={t("Network")} description={t("All containers of the project together, per second")} table={<SeriesTable series={charts.net} format={rateFmt} />}>
              <LineChart series={charts.net} from={m.from} to={now} step={m.res} format={rateFmt} label={t("Network")} />
            </ChartCard>
            <ChartCard title={t("Disk I/O")} description={t("All containers of the project together, per second")} table={<SeriesTable series={charts.io} format={rateFmt} />}>
              <LineChart series={charts.io} from={m.from} to={now} step={m.res} format={rateFmt} label={t("Disk I/O")} />
            </ChartCard>
            <div className="xl:col-span-2">
              <ChartCard title={t("Disk space")} description={t("Volumes (database, caches, search, storage), the project directory and its backups – measured hourly")} table={<SeriesTable series={charts.size} format={formatBytes} />}>
                <LineChart series={charts.size} from={sizeFrom} to={now} step={3600} format={formatBytes} label={t("Disk space")} />
              </ChartCard>
            </div>
          </div>
        )
      )}
      <LimitsCard project={project} />
    </div>
  );
}
