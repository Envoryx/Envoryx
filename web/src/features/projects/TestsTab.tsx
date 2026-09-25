import { clsx } from "clsx";
import { useTranslation } from "react-i18next";
import { CheckCircle2, ChevronDown, ChevronRight, FlaskConical, Play, Square, XCircle } from "lucide-react";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Project, TestRun, TestSuite } from "@/api/types";
import { Badge, Button, Card, CardHeader, Code, ErrorState, Input, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatDateTime } from "@/lib/format";
import { useLiveRun } from "./useLiveRun";

function duration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  return s < 60 ? `${s.toFixed(1)} s` : `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`;
}

/** The outcome in one badge: passed/failed counts when there was a report, the status otherwise. */
function RunBadge({ run }: { run: TestRun }) {
  const { t } = useTranslation();
  const r = run.result;
  const tone = run.status === "passed" ? "green" : run.status === "failed" ? "red" : "gray";
  if (run.status === "cancelled") return <Badge tone={tone}>{t("cancelled")}</Badge>;
  if (!r.report) return <Badge tone={tone}>{run.status === "passed" ? t("passed") : t("failed (exit {{code}})", { code: run.exitCode })}</Badge>;
  const bad = r.failures + r.errors;
  return <Badge tone={tone}>{bad > 0 ? t("{{failed}} of {{total}} failed", { failed: bad, total: r.tests }) : t("{{number}} passed", { number: r.tests - r.skipped })}</Badge>;
}

/** The failed tests of a run, each opening to its message and details. */
function Failures({ run }: { run: TestRun }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState<number | null>(run.result.failed.length === 1 ? 0 : null);
  if (run.result.failed.length === 0) return null;
  return (
    <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
      {run.result.failed.map((f, i) => (
        <li key={i} className="text-sm">
          <button type="button" className="flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-muted" aria-expanded={open === i} onClick={() => setOpen(open === i ? null : i)}>
            {open === i ? <ChevronDown className="mt-0.5 size-4 shrink-0" /> : <ChevronRight className="mt-0.5 size-4 shrink-0" />}
            <XCircle className="mt-0.5 size-4 shrink-0 text-red-500" aria-hidden />
            <span className="min-w-0 flex-1">
              <span className="block font-mono text-xs font-medium">{f.name}</span>
              <span className="block truncate text-xs text-muted">{[f.class, f.file && `${f.file}${f.line ? `:${f.line}` : ""}`].filter(Boolean).join(" · ")}</span>
              <span className="block text-xs text-red-700 dark:text-red-300">{f.message}</span>
            </span>
            {f.kind === "error" && <Badge tone="amber">{t("error")}</Badge>}
          </button>
          {open === i && f.details && <pre className="mx-3 mb-3 max-h-72 overflow-auto rounded-md bg-muted p-2 font-mono text-[11px] whitespace-pre-wrap">{f.details}</pre>}
        </li>
      ))}
      {run.result.more ? <li className="px-3 py-2 text-xs text-muted">{t("… and {{number}} more", { number: run.result.more })}</li> : null}
    </ul>
  );
}

/** One run in full: counts, failed tests, and the end of its output. */
function RunDetails({ run, suites }: { run: TestRun; suites: TestSuite[] }) {
  const { t } = useTranslation();
  const r = run.result;
  const label = suites.find((s) => s.id === run.suite)?.label ?? run.suite;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        {run.status === "passed" ? <CheckCircle2 className="size-4 text-emerald-500" aria-hidden /> : <XCircle className="size-4 text-red-500" aria-hidden />}
        <span className="font-medium">{label}</span>
        {run.filter && <Code>{run.filter}</Code>}
        <RunBadge run={run} />
        <span className="text-xs text-muted">
          {formatDateTime(run.startedAt)} · {duration(run.durationMs)}
        </span>
      </div>
      {r.report && (
        <p className="text-xs text-muted">
          {t("{{tests}} tests · {{failures}} failures · {{errors}} errors · {{skipped}} skipped", { tests: r.tests, failures: r.failures, errors: r.errors, skipped: r.skipped })}
        </p>
      )}
      <Failures run={run} />
      {!r.report && run.status === "failed" && r.output && <pre className="max-h-72 overflow-auto rounded-md bg-muted p-2 font-mono text-[11px] whitespace-pre-wrap">{r.output}</pre>}
    </div>
  );
}

export function TestsTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const key = ["projects", project.id, "tests"];
  const q = useQuery({ queryKey: key, queryFn: () => api.tests.list(project.id) });
  const [filters, setFilters] = useState<Record<string, string>>({});
  const [current, setCurrent] = useState<{ suite: TestSuite; result?: TestRun } | null>(null);
  const [selected, setSelected] = useState<TestRun | null>(null);
  const live = useLiveRun((m) => {
    if (m.type === "result" && m.run) {
      const run = m.run as TestRun;
      setCurrent((c) => (c ? { ...c, result: run } : c));
      setSelected(null);
      void qc.invalidateQueries({ queryKey: key });
    }
  });
  const running = live.run?.state === "running";

  const start = (suite: TestSuite) => {
    const filter = (filters[suite.id] ?? "").trim();
    setCurrent({ suite });
    setSelected(null);
    const query = filter ? `?filter=${encodeURIComponent(filter)}` : "";
    live.start(`/projects/${encodeURIComponent(project.id)}/tests/${encodeURIComponent(suite.id)}/ws${query}`, [...suite.cmd, ...(filter ? [filter] : [])].join(" "));
  };
  const openRun = async (run: TestRun) => {
    try {
      setSelected((await api.tests.run(project.id, run.id)).run);
    } catch {
      setSelected(run);
    }
  };
  const shown = selected ?? current?.result ?? null;

  return (
    <div className="grid gap-6 lg:grid-cols-[22rem_1fr]">
      <div className="space-y-6 self-start">
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <FlaskConical className="size-4 text-accent-500" aria-hidden /> {t("Test suites")}
              </span>
            }
            description={t("Found in the project: PHPUnit and Pest, the test scripts of package.json, Playwright, Cypress, pytest and Django. They run in the runtime container as the project owner.")}
          />
          {q.isPending ? (
            <Spinner />
          ) : q.isError ? (
            <ErrorState message={errorText(q.error, t)} />
          ) : q.data.suites.length === 0 ? (
            <p className="p-5 text-sm text-muted">{t("No test suite found. Envoryx looks for vendor/bin/phpunit or pest, test scripts in package.json, a Playwright or Cypress configuration, pytest and manage.py.")}</p>
          ) : (
            <ul className="divide-y divide-[var(--border)]">
              {q.data.suites.map((s) => (
                <li key={s.id} className="space-y-2 px-4 py-3">
                  <div className="flex items-center justify-between gap-2">
                    <span className="min-w-0">
                      <span className="block text-sm font-medium">{s.label}</span>
                      <span className="block truncate font-mono text-[11px] text-subtle">{s.cmd.join(" ")}</span>
                    </span>
                    <Button size="sm" variant="primary" icon={<Play className="size-3.5" />} disabled={!s.available || running} title={s.available ? undefined : s.reason} onClick={() => start(s)}>
                      {t("Run")}
                    </Button>
                  </div>
                  {!s.available && s.reason && <p className="text-xs text-amber-700 dark:text-amber-400">{s.reason}</p>}
                  {s.filterHint && s.available && (
                    <Input
                      aria-label={t("Filter for {{suite}}", { suite: s.label })}
                      placeholder={t("Filter ({{hint}}), optional", { hint: t(s.filterHint) })}
                      value={filters[s.id] ?? ""}
                      onChange={(e) => setFilters({ ...filters, [s.id]: e.target.value })}
                      onKeyDown={(e) => e.key === "Enter" && !running && start(s)}
                      className="font-mono text-xs"
                      spellCheck={false}
                    />
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>

        {q.data && q.data.runs.length > 0 && (
          <Card>
            <CardHeader title={t("Recent runs")} />
            <ul className="divide-y divide-[var(--border)]">
              {q.data.runs.map((r) => (
                <li key={r.id}>
                  <button type="button" onClick={() => void openRun(r)} className={clsx("flex w-full items-center justify-between gap-2 px-4 py-2 text-left text-xs hover:bg-muted", shown?.id === r.id && "bg-accent-500/10")}>
                    <span className="min-w-0">
                      <span className="block truncate font-medium">
                        {q.data.suites.find((s) => s.id === r.suite)?.label ?? r.suite}
                        {r.filter && <span className="font-mono text-subtle"> · {r.filter}</span>}
                      </span>
                      <span className="block text-subtle">
                        {formatDateTime(r.startedAt)} · {duration(r.durationMs)}
                      </span>
                    </span>
                    <RunBadge run={r} />
                  </button>
                </li>
              ))}
            </ul>
          </Card>
        )}
      </div>

      <div className="space-y-6">
        <Card className="flex h-[55vh] min-h-[20rem] flex-col overflow-hidden">
          <div className="flex items-center gap-2 border-b border-default px-3 py-2 text-xs">
            {current ? (
              <>
                <Code>{current.suite.label}</Code>
                <Badge tone={running ? "blue" : live.run?.state === "finished" ? "green" : "red"}>{running ? t("running") : live.run?.exitCode !== null && live.run?.exitCode !== undefined ? t("exit {{code}}", { code: live.run.exitCode }) : (live.run?.message ?? t("failed"))}</Badge>
                {running && (
                  <Button size="sm" variant="ghost" className="ml-auto" onClick={live.cancel} icon={<Square className="size-3.5" />}>
                    {t("Cancel")}
                  </Button>
                )}
              </>
            ) : (
              <span className="text-muted">{t("Run a suite to see its output here.")}</span>
            )}
          </div>
          <div ref={live.host} className="min-h-0 flex-1 overflow-hidden bg-[#0f1115] p-2" data-testid="test-output" />
        </Card>
        {shown && q.data && (
          <Card>
            <div className="p-5">
              <RunDetails run={shown} suites={q.data.suites} />
            </div>
          </Card>
        )}
      </div>
    </div>
  );
}
