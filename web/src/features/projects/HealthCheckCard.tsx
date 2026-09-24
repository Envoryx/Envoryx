import { HeartPulse, Play, Power, Save } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys } from "@/api/hooks";
import type { HealthCheck, HealthResult, HealthState, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Field, Input } from "@/components/ui";
import { errorText, translateMessage } from "@/lib/errors";
import { formatRelative } from "@/lib/format";

const defaults = { status: 200, intervalSec: 30, timeoutSec: 5, failures: 3 };

interface Form {
  path: string;
  status: string;
  intervalSec: string;
  timeoutSec: string;
  failures: string;
}

function toForm(h: HealthCheck | undefined): Form {
  return {
    path: h?.path ?? "",
    status: String(h?.status ?? defaults.status),
    intervalSec: String(h?.intervalSec ?? defaults.intervalSec),
    timeoutSec: String(h?.timeoutSec ?? defaults.timeoutSec),
    failures: String(h?.failures ?? defaults.failures),
  };
}

function fromForm(f: Form): HealthCheck {
  const n = (v: string) => parseInt(v, 10) || 0;
  return { path: f.path.trim(), status: n(f.status), intervalSec: n(f.intervalSec), timeoutSec: n(f.timeoutSec), failures: n(f.failures) };
}

const tones: Record<HealthState, "green" | "amber" | "red" | "gray" | "blue"> = { up: "green", failing: "amber", down: "red", paused: "gray", pending: "blue" };

/**
 * The application health check: a path that must answer with the expected status. Envoryx
 * asks the web server the way the proxy does and notifies when the check fails several
 * times in a row, and again when it answers.
 */
export function HealthCheckCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const current = p.healthCheck;
  const [form, setForm] = useState<Form>(toForm(current));
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [result, setResult] = useState<HealthResult | null>(null);
  const currentKey = JSON.stringify(current ?? {});
  useEffect(() => {
    setForm(toForm(current));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- compared by content
  }, [currentKey]);

  const body = fromForm(form);
  const dirty = JSON.stringify(body) !== JSON.stringify(fromForm(toForm(current)));
  const running = p.status.state === "running" || p.status.state === "partial";

  const save = useMutation({
    mutationFn: (h: HealthCheck) => api.projects.setHealthCheck(p.id, h),
    onSuccess: (_res, h) => {
      setMsg({ tone: "green", text: h.path ? t("Saved. The first check runs within a few seconds.") : t("The health check is off.") });
      void qc.invalidateQueries({ queryKey: keys.project(p.id) });
      void qc.invalidateQueries({ queryKey: keys.projects });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  const test = useMutation({
    mutationFn: () => api.projects.testHealthCheck(p.id, body),
    onSuccess: (res) => setResult(res.result),
    onError: (err) => {
      setResult(null);
      setMsg({ tone: "red", text: errorText(err, t, t("The check could not run")) });
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    save.mutate(body);
  };

  const h = p.status.health;
  const stateLabel: Record<HealthState, string> = { up: t("up"), failing: t("failing"), down: t("down"), paused: t("paused"), pending: t("waiting") };
  const time = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <HeartPulse className="size-4 text-accent-500" aria-hidden /> {t("Health check")}
          </span>
        }
        description={t("Envoryx requests a path of the application the way a visitor would – straight to the web server, with the project's host name – and sends a notification when it fails several times in a row.")}
        actions={current ? h ? <Badge tone={tones[h.state]}>{stateLabel[h.state]}</Badge> : <Badge tone="blue">{t("waiting")}</Badge> : <Badge>{t("off")}</Badge>}
      />
      <div className="space-y-4 p-5">
        {current && h && (
          <div className="rounded-md border border-default px-3 py-2 text-sm" aria-live="polite">
            {h.state === "up" && <p className="text-fg">{t("Answers {{status}} in {{ms}} ms · up since {{time}}", { status: h.status, ms: h.latencyMs ?? 0, time: time(h.since) })}</p>}
            {h.state === "failing" && <p className="text-amber-700 dark:text-amber-400">{t("Failed {{n}} of {{max}} times in a row: {{error}}", { n: h.failures ?? 1, max: current.failures ?? defaults.failures, error: translateMessage(h.error, t) })}</p>}
            {h.state === "down" && <p className="text-red-600 dark:text-red-400">{t("Down since {{time}}: {{error}}", { time: time(h.downSince ?? h.since), error: translateMessage(h.error, t) })}</p>}
            {h.state === "paused" && <p className="text-muted">{t("Paused: the project is stopped, busy with an operation, or its application container is not running.")}</p>}
            {h.state === "pending" && <p className="text-muted">{t("Waiting for the first check.")}</p>}
            {h.checked && <p className="text-xs text-subtle">{t("Last checked {{when}}", { when: formatRelative(h.checked, t) })}</p>}
          </div>
        )}
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {result && (
          <Alert tone={result.ok ? "green" : "red"}>
            <span className="font-mono text-xs">GET {result.url}</span>
            <br />
            {result.ok
              ? t("Answered {{status}} in {{ms}} ms – the check passes.", { status: result.status, ms: result.latencyMs })
              : t("The check fails: {{error}}", { error: translateMessage(result.error, t) })}
          </Alert>
        )}
        <form onSubmit={submit} className="space-y-4">
          <Field label={t("Path")} htmlFor="hc-path" hint={t("For example /health or /up – an address that checks what the application needs (database, cache) and answers quickly. Empty: no check.")}>
            <Input id="hc-path" value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} placeholder="/health" className="font-mono" />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t("Expected status")} htmlFor="hc-status">
              <Input id="hc-status" type="number" min={100} max={599} value={form.status} onChange={(e) => setForm({ ...form, status: e.target.value })} />
            </Field>
            <Field label={t("Interval (seconds)")} htmlFor="hc-interval">
              <Input id="hc-interval" type="number" min={10} max={3600} value={form.intervalSec} onChange={(e) => setForm({ ...form, intervalSec: e.target.value })} />
            </Field>
            <Field label={t("Timeout (seconds)")} htmlFor="hc-timeout">
              <Input id="hc-timeout" type="number" min={1} max={60} value={form.timeoutSec} onChange={(e) => setForm({ ...form, timeoutSec: e.target.value })} />
            </Field>
            <Field label={t("Down after")} htmlFor="hc-failures" hint={t("failed checks in a row")}>
              <Input id="hc-failures" type="number" min={1} max={20} value={form.failures} onChange={(e) => setForm({ ...form, failures: e.target.value })} />
            </Field>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" variant="primary" size="sm" loading={save.isPending} disabled={!dirty || (!form.path.trim() && !current)} icon={<Save className="size-3.5" />}>
              {t("Save")}
            </Button>
            <Button
              type="button"
              size="sm"
              loading={test.isPending}
              disabled={!running || !form.path.trim()}
              title={running ? undefined : t("Start the project to test the check.")}
              onClick={() => {
                setMsg(null);
                test.mutate();
              }}
              icon={<Play className="size-3.5" />}
            >
              {t("Test")}
            </Button>
            {current && (
              <Button
                type="button"
                size="sm"
                variant="ghost"
                loading={save.isPending}
                onClick={() => {
                  setMsg(null);
                  setResult(null);
                  save.mutate({ path: "" });
                }}
                icon={<Power className="size-3.5" />}
              >
                {t("Switch off")}
              </Button>
            )}
          </div>
        </form>
      </div>
    </Card>
  );
}
