import { clsx } from "clsx";
import { ChevronDown, ChevronRight, Download, RotateCcw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Fragment, useEffect, useMemo, useState } from "react";
import { api } from "@/api/client";
import { useAuditLog, useAuditUsers } from "@/api/hooks";
import type { AuditChange, AuditEntry, AuditFilter } from "@/api/types";
import { Button, ErrorState, Field, Input, Select, Spinner } from "@/components/ui";
import { auditActionLabel, auditActor, auditDetails } from "@/lib/audit";
import { errorText } from "@/lib/errors";
import { formatDateTime } from "@/lib/format";

/** The filter's categories: groups of action prefixes. */
export const auditCategories: { key: string; label: string; prefixes: string[] }[] = [
  { key: "auth", label: "Sign-ins and accounts", prefixes: ["auth.", "token."] },
  { key: "projects", label: "Projects", prefixes: ["project."] },
  { key: "databases", label: "Databases", prefixes: ["database."] },
  { key: "backups", label: "Backups", prefixes: ["backup.", "instance."] },
  { key: "commands", label: "Commands and terminals", prefixes: ["terminal.", "project.exec", "action.run", "test.run", "cron.run"] },
  { key: "git", label: "Git", prefixes: ["git."] },
  { key: "system", label: "Settings and system", prefixes: ["settings.", "docker.", "logs.", "cache.", "ollama."] },
];

/** The value the search box settles on once typing pauses. */
function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

/** What a project update changed: every setting with its value before and after. */
function ChangeTable({ changes }: { changes: AuditChange[] }) {
  const { t } = useTranslation();
  return (
    <table className="w-full text-xs">
      <thead className="text-left text-subtle">
        <tr>
          <th className="py-1 pr-3 font-medium">{t("Setting")}</th>
          <th className="py-1 pr-3 font-medium">{t("Before")}</th>
          <th className="py-1 font-medium">{t("After")}</th>
        </tr>
      </thead>
      <tbody>
        {changes.map((c, i) => (
          <tr key={i} className="align-top">
            <td className="py-1 pr-3 font-mono">
              {c.section}
              {c.item && <span className="text-muted"> · {c.item}</span>}
            </td>
            <td className="py-1 pr-3 font-mono text-red-600 dark:text-red-400">{c.from || <span className="text-subtle">—</span>}</td>
            <td className="py-1 font-mono text-emerald-700 dark:text-emerald-400">{c.to || <span className="text-subtle">—</span>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** The expanded part of a row: the changes, then everything else the entry recorded. */
function EntryDetails({ e }: { e: AuditEntry }) {
  const { t } = useTranslation();
  const { diff, ...rest } = (e.details ?? {}) as Record<string, unknown> & { diff?: AuditChange[] };
  return (
    <div className="space-y-3 bg-muted/40 px-5 py-3">
      {Array.isArray(diff) && diff.length > 0 && <ChangeTable changes={diff} />}
      <dl className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[8rem_1fr]">
        <dt className="text-muted">{t("Time")}</dt>
        <dd className="font-mono">{e.createdAt}</dd>
        <dt className="text-muted">{t("Action")}</dt>
        <dd className="font-mono">{e.action}</dd>
        {e.targetType && (
          <>
            <dt className="text-muted">{t("Target")}</dt>
            <dd className="font-mono">
              {e.targetType} {e.targetId}
            </dd>
          </>
        )}
        {e.ip && (
          <>
            <dt className="text-muted">IP</dt>
            <dd className="font-mono">{e.ip}</dd>
          </>
        )}
      </dl>
      {Object.keys(rest).length > 0 && <pre className="overflow-x-auto rounded-md border border-default bg-elevated p-2 text-[11px] leading-relaxed">{JSON.stringify(rest, null, 2)}</pre>}
    </div>
  );
}

/**
 * The audit log with its filters: searchable by text, user, category and time, a page at
 * a time, exportable with the same filters. project fixes the project filter (the
 * project's History tab).
 */
export function AuditLog({ project }: { project?: string }) {
  const { t } = useTranslation();
  const users = useAuditUsers();
  const [text, setText] = useState("");
  const [user, setUser] = useState("");
  const [category, setCategory] = useState("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const q = useDebounced(text, 300);
  const filter = useMemo<AuditFilter>(
    () => ({ q, user, actions: auditCategories.find((c) => c.key === category)?.prefixes, project, since, until }),
    [q, user, category, project, since, until],
  );
  const log = useAuditLog(filter);
  const entries = log.data?.pages.flatMap((p) => p.entries) ?? [];
  const filtered = !!(text || user || category || since || until);

  return (
    <div className="space-y-4">
      <div className="grid gap-3 px-5 sm:grid-cols-2 lg:grid-cols-[2fr_1fr_1fr_1fr_1fr]">
        <Field label={t("Search")} htmlFor="audit-q">
          <Input id="audit-q" type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={t("Action, user, project, IP …")} />
        </Field>
        <Field label={t("User")} htmlFor="audit-user">
          <Select id="audit-user" value={user} onChange={(e) => setUser(e.target.value)}>
            <option value="">{t("All")}</option>
            {users.data?.map((u) => (
              <option key={u} value={u}>
                {u}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("Category")} htmlFor="audit-category">
          <Select id="audit-category" value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="">{t("All")}</option>
            {auditCategories.map((c) => (
              <option key={c.key} value={c.key}>
                {t(c.label)}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("From")} htmlFor="audit-since">
          <Input id="audit-since" type="date" value={since} onChange={(e) => setSince(e.target.value)} />
        </Field>
        <Field label={t("Until")} htmlFor="audit-until">
          <Input id="audit-until" type="date" value={until} onChange={(e) => setUntil(e.target.value)} />
        </Field>
      </div>
      <div className="flex flex-wrap items-center gap-2 px-5">
        {filtered && (
          <Button
            size="sm"
            variant="ghost"
            icon={<RotateCcw className="size-3.5" />}
            onClick={() => {
              setText("");
              setUser("");
              setCategory("");
              setSince("");
              setUntil("");
            }}
          >
            {t("Reset filters")}
          </Button>
        )}
        <span className="ml-auto text-xs text-subtle">{t("Export with these filters:")}</span>
        {(["csv", "jsonl"] as const).map((f) => (
          <a
            key={f}
            href={api.audit.exportUrl(filter, f)}
            download
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-default bg-elevated px-2.5 text-xs font-medium text-fg hover:bg-muted"
          >
            <Download className="size-3.5" aria-hidden /> {f === "csv" ? "CSV" : "JSON Lines"}
          </a>
        ))}
      </div>

      {log.isPending ? (
        <Spinner />
      ) : log.isError ? (
        <ErrorState message={errorText(log.error, t)} />
      ) : entries.length === 0 ? (
        <p className="px-5 pb-5 text-sm text-muted">{filtered ? t("No entries match these filters.") : t("No entries yet.")}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-subtle">
              <tr className="border-b border-default">
                <th className="w-8" />
                <th className="px-2 py-2 font-medium">{t("Time")}</th>
                <th className="px-3 py-2 font-medium">{t("User")}</th>
                <th className="px-3 py-2 font-medium">{t("Action")}</th>
                <th className="px-3 py-2 font-medium">{t("Details")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[var(--border)]">
              {entries.map((e) => {
                const expanded = open === e.id;
                const changes = Array.isArray(e.details?.["diff"]) ? (e.details!["diff"] as AuditChange[]).length : 0;
                return (
                  <Fragment key={e.id}>
                    <tr className={clsx("cursor-pointer hover:bg-muted/50", expanded && "bg-muted/40")} onClick={() => setOpen(expanded ? null : e.id)}>
                      <td className="pl-3 text-subtle">
                        <button type="button" aria-expanded={expanded} aria-label={expanded ? t("Hide details") : t("Show details")} className="p-1" onClick={(ev) => { ev.stopPropagation(); setOpen(expanded ? null : e.id); }}>
                          {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                        </button>
                      </td>
                      <td className="whitespace-nowrap px-2 py-2 text-xs text-muted">{formatDateTime(e.createdAt)}</td>
                      <td className="px-3 py-2 text-xs">
                        {e.username ? (
                          <button
                            type="button"
                            className="text-left hover:underline"
                            title={t("Show only this user")}
                            onClick={(ev) => {
                              ev.stopPropagation();
                              setUser(e.username.split(" (token: ")[0]!);
                            }}
                          >
                            {auditActor(e, t)}
                          </button>
                        ) : (
                          <span className="italic text-muted">{auditActor(e, t)}</span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-xs" title={e.action}>
                        {auditActionLabel(e.action, t)}
                      </td>
                      <td className="max-w-md truncate px-3 py-2 text-xs text-muted" title={auditDetails(e, t)}>
                        {auditDetails(e, t)}
                        {changes > 0 && <span className="ml-2 text-subtle">({t("{{count}} settings changed", { count: changes })})</span>}
                      </td>
                    </tr>
                    {expanded && (
                      <tr>
                        <td colSpan={5} className="p-0">
                          <EntryDetails e={e} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
          {log.hasNextPage && (
            <div className="p-4 text-center">
              <Button size="sm" loading={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
                {t("Load older entries")}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
