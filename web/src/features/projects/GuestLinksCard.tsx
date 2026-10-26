import { useState, type FormEvent } from "react";
import { Link2, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { GuestLink, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Field, Input, Select } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { projectAccess } from "@/lib/access";
import { formatDateTime } from "@/lib/format";
import { CopyButton } from "./DatabaseTab";

const durations = [1, 7, 30, 90] as const;

/**
 * Guest links let people without an Envoryx account into a project behind the Envoryx
 * sign-in: the customer reviewing a staging site. The address is shown once, right after
 * the link is made; only its hash stays on the server.
 */
export function GuestLinksCard({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const key = ["projects", project.id, "guest-links"];
  const access = projectAccess(project);
  const links = useQuery({ queryKey: key, queryFn: async () => (await api.guestLinks.list(project.id)).links });
  const [label, setLabel] = useState("");
  const [days, setDays] = useState(7);
  const [made, setMade] = useState<GuestLink | null>(null);
  const [error, setError] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: async () => (await api.guestLinks.create(project.id, label.trim(), days)).link,
    onSuccess: (link) => {
      setMade(link);
      setLabel("");
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (err) => setError(errorText(err, t, t("Creating the guest link failed"))),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.guestLinks.revoke(project.id, id),
    onSuccess: (_, id) => {
      if (made?.id === id) setMade(null);
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (err) => setError(errorText(err, t, t("Revoking the guest link failed"))),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate();
  };
  const list = links.data ?? [];

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Link2 className="size-4 text-accent-500" aria-hidden /> {t("Guest links")}
          </span>
        }
        description={t("For people without an Envoryx account, such as a customer reviewing the site: the link opens the project's addresses under the base domain until it ends or you revoke it; for a domain of its own, send the link with that domain in it. Not its addons, terminal or files.")}
        actions={list.length ? <Badge tone="blue">{list.length}</Badge> : undefined}
      />
      <div className="space-y-4 p-5">
        {error && <Alert tone="red">{error}</Alert>}
        {!project.proxyRules?.login && <Alert tone="amber">{t("The project is open to everyone right now. Guest links only matter once it lets in only people signed in to Envoryx (Rules above).")}</Alert>}
        {made?.url && (
          <Alert tone="green">
            <p className="mb-2">{t("Send this address to {{label}}. It is shown only now.", { label: made.label })}</p>
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 break-all rounded bg-elevated px-2 py-1 font-mono text-xs">{made.url}</code>
              <CopyButton value={made.url} label={t("guest link")} />
            </div>
          </Alert>
        )}
        {list.length > 0 && (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {list.map((l) => (
              <li key={l.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-sm">
                <div className="min-w-0">
                  <div className="font-medium text-fg">{l.label}</div>
                  <div className="text-xs text-muted">
                    {t("by {{user}}, until {{time}}", { user: l.createdBy, time: formatDateTime(l.expiresAt) })}
                    {" · "}
                    {l.usedAt ? t("last used {{time}}", { time: formatDateTime(l.usedAt) }) : t("not used yet")}
                  </div>
                </div>
                {access.operate && (
                  <Button variant="ghost" size="sm" className="text-red-600 dark:text-red-400" icon={<Trash2 className="size-3.5" />} loading={revoke.isPending && revoke.variables === l.id} onClick={() => revoke.mutate(l.id)}>
                    {t("Revoke")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
        {access.admin && (
          <form onSubmit={submit} className="grid gap-3 sm:grid-cols-[1fr_auto_auto] sm:items-end">
            <Field label={t("For whom")} htmlFor="guest-label">
              <Input id="guest-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder={t("e.g. Acme, review of the new shop")} maxLength={80} />
            </Field>
            <Field label={t("Valid for")} htmlFor="guest-days">
              <Select id="guest-days" value={String(days)} onChange={(e) => setDays(Number(e.target.value))}>
                {durations.map((d) => (
                  <option key={d} value={d}>
                    {t("{{count}} days", { count: d })}
                  </option>
                ))}
              </Select>
            </Field>
            <Button type="submit" variant="primary" loading={create.isPending} disabled={!label.trim()} icon={<Plus className="size-4" />}>
              {t("Create link")}
            </Button>
          </form>
        )}
      </div>
    </Card>
  );
}
