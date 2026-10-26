import { Bot, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/features/auth/AuthContext";
import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useProjects } from "@/api/hooks";
import type { TokenScope } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Field, Input, Select, Spinner, type Tone } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { covers, type Access } from "@/lib/access";

const key = ["tokens"] as const;

const scopeTone: Record<TokenScope, Tone> = { read: "gray", operate: "blue", admin: "amber" };

// The scope a global role gives on the instance (auth.Role.Scope); none gives nothing.
const roleScope: Record<string, Access | undefined> = { admin: "admin", developer: "operate", viewer: "read" };

/**
 * The highest scope the user can give a token, as the server's Principal.MaxScope: the
 * global role, or a higher role in one of the projects. The server refuses anything above.
 */
export function maxTokenScope(admin: boolean, role: string | undefined, projectAccess: (string | undefined)[]): Access | undefined {
  if (admin) return "admin";
  let best = role ? roleScope[role] : undefined;
  for (const a of projectAccess) {
    if (a && !covers(best, a as Access)) best = a as Access;
  }
  return best;
}

/** Lifetimes offered for a new token, in days; 0 never expires. */
const lifetimes = [7, 30, 90, 365, 0] as const;

function mcpConfig(url: string, secret: string): string {
  return JSON.stringify({ mcpServers: { envoryx: { type: "http", url, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2);
}

export function TokensCard() {
  const { t } = useTranslation();
  const { user, admin } = useAuth();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: key, queryFn: api.tokens.list });
  const projects = useProjects();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<TokenScope>("operate");
  const [confine, setConfine] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const [lifetime, setLifetime] = useState<number>(90);
  const [created, setCreated] = useState<{ secret: string; url: string; name: string } | null>(null);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: (body: { name: string; scope: TokenScope; projects: string[]; expiresInDays: number }) => api.tokens.create(body),
    onSuccess: (r) => {
      setCreated({ secret: r.secret, url: r.mcpUrl, name: r.token.name });
      setName("");
      setConfine(false);
      setSelected([]);
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Creating the token failed")) }),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.tokens.revoke(id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: key }),
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Revoking failed")) }),
  });

  async function copy(label: string, value: string) {
    setCopied((await copyText(value)) ? label : null);
    setTimeout(() => setCopied(null), 2000);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    setCreated(null);
    create.mutate({ name: name.trim(), scope: chosen, projects: confine ? selected : [], expiresInDays: lifetime });
  }

  const scopes: { value: TokenScope; label: string; description: string }[] = [
    { value: "read", label: t("Read"), description: t("Status, logs, statistics and listings. No secrets, no changes.") },
    { value: "operate", label: t("Operate"), description: t("Also start, stop and restart, run actions, create backups and databases, git, domains, SSH/SFTP and the terminal.") },
    { value: "admin", label: t("Admin"), description: t("Everything the browser can do: create and delete projects, settings, TLS, instance backups, image clean-up, restores.") },
  ];
  const projectName = (id: string) => projects.data?.find((p) => p.id === id)?.name ?? id.slice(0, 8);
  const max = maxTokenScope(admin, user?.role, (projects.data ?? []).map((p) => p.access));
  const allowed = (s: TokenScope) => covers(max, s);
  // The default (operate) may be above what the role gives: take the highest allowed one.
  const chosen = allowed(scope) ? scope : ([...scopes].reverse().find((s) => allowed(s.value))?.value ?? scope);
  const scopeLabel = (s: TokenScope) => scopes.find((x) => x.value === s)?.label ?? s;
  const lifetimeLabel = (days: number) => (days === 0 ? t("Never") : days === 365 ? t("1 year") : t("{{count}} days", { count: days }));
  const now = Date.now();

  return (
    <Card>
      <CardHeader
        title={t("API tokens & MCP")}
        description={t("Tokens authenticate AI assistants (Claude Code, Cursor, …) on the MCP server, SSH/SFTP logins and scripts calling the REST API with an Authorization: Bearer header. Each token has a scope - read, operate or admin - and can be confined to particular projects. A token can never change the password or manage tokens.")}
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {created && (
          <Alert tone="green" title={t('Token "{{name}}" created - copy it now, it is not shown again.', { name: created.name })}>
            <div className="mt-2 space-y-3">
              <div>
                <code className="block select-all break-all rounded-md bg-muted p-2 font-mono text-[11px]">{created.secret}</code>
                <Button size="sm" className="mt-1" onClick={() => copy("secret", created.secret)}>
                  {copied === "secret" ? t("Copied") : t("Copy token")}
                </Button>
              </div>
              <div>
                <p className="text-xs text-muted">{t("MCP client configuration (e.g. .mcp.json for Claude Code, mcp.json for Cursor):")}</p>
                <pre className="mt-1 overflow-x-auto rounded-md bg-muted p-2 font-mono text-[11px]">{mcpConfig(created.url, created.secret)}</pre>
                <Button size="sm" className="mt-1" onClick={() => copy("config", mcpConfig(created.url, created.secret))}>
                  {copied === "config" ? t("Copied") : t("Copy configuration")}
                </Button>
              </div>
              <p className="text-xs text-muted">
                Claude Code: <Code>claude mcp add --transport http envoryx {created.url} --header "Authorization: Bearer …"</Code>
              </p>
            </div>
          </Alert>
        )}
        {q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <Alert tone="red">{errorText(q.error, t)}</Alert>
        ) : q.data.tokens.length === 0 ? (
          <p className="text-sm text-muted">{t("No tokens yet. The MCP endpoint is")} <Code>{q.data.mcpUrl}</Code>.</p>
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {q.data.tokens.map((tok) => (
              <li key={tok.id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm">
                <div className="flex items-center gap-3">
                  <Bot className="size-4 text-accent-500" aria-hidden />
                  <div>
                    <p className="flex flex-wrap items-center gap-2 font-medium">
                      {tok.name}
                      <Badge tone={scopeTone[tok.scope] ?? "gray"}>{scopeLabel(tok.scope)}</Badge>
                      {tok.owner && tok.owner !== user?.username && <span className="text-xs font-normal text-subtle">{t("of {{name}}", { name: tok.owner })}</span>}
                      {tok.expiresAt && Date.parse(tok.expiresAt) <= now && <Badge tone="red">{t("Expired")}</Badge>}
                      {tok.projects.length > 0 && (
                        <Badge tone="gray" className="font-normal">
                          {t("only")} {tok.projects.map(projectName).join(", ")}
                        </Badge>
                      )}
                    </p>
                    <p className="font-mono text-[11px] text-subtle">
                      {tok.prefix}… · {t("created {{date}}", { date: formatDateTime(tok.createdAt) })} · {tok.expiresAt ? (Date.parse(tok.expiresAt) <= now ? t("expired {{date}}", { date: formatDateTime(tok.expiresAt) }) : t("expires {{date}}", { date: formatDateTime(tok.expiresAt) })) : t("never expires")} · {tok.lastUsedAt ? t("last used {{date}}", { date: formatDateTime(tok.lastUsedAt) }) : t("never used")}
                    </p>
                  </div>
                </div>
                <Button size="sm" variant="ghost" onClick={() => revoke.mutate(tok.id)} loading={revoke.isPending && revoke.variables === tok.id} icon={<Trash2 className="size-3.5" />} aria-label={t("Revoke {{name}}", { name: tok.name })}>
                  {t("Revoke")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <form onSubmit={submit} className="space-y-4 rounded-md border border-default p-4">
          <Field label={t("New token")} htmlFor="token-name" hint={t("A name that tells you which client uses it.")}>
            <Input id="token-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t("Claude Code on my laptop")} maxLength={64} />
          </Field>
          <fieldset>
            <legend className="mb-2 text-sm font-medium">{t("Scope")}</legend>
            <div className="grid gap-2 sm:grid-cols-3">
              {scopes.map((s) => (
                <label key={s.value} className={`flex flex-col gap-1 rounded-md border p-3 text-sm ${allowed(s.value) ? "cursor-pointer" : "cursor-not-allowed opacity-50"} ${chosen === s.value ? "border-accent-500 bg-accent-500/5" : "border-default"}`}>
                  <span className="flex items-center gap-2 font-medium">
                    <input type="radio" name="token-scope" value={s.value} checked={chosen === s.value} disabled={!allowed(s.value)} onChange={() => setScope(s.value)} className="accent-[var(--color-accent-500)]" />
                    {s.label}
                  </span>
                  <span className="text-xs text-muted">{s.description}</span>
                </label>
              ))}
            </div>
            {max !== "admin" && projects.data && <p className="mt-2 text-xs text-muted">{max ? t("Your role allows tokens up to {{scope}}.", { scope: scopeLabel(max) }) : t("Your role allows no API tokens yet.")}</p>}
          </fieldset>
          <Field label={t("Expires after")} htmlFor="token-lifetime" hint={t("An expired token stops working; create a new one then. A token that never expires stays valid until it is revoked.")}>
            <Select id="token-lifetime" value={String(lifetime)} onChange={(e) => setLifetime(Number(e.target.value))} className="sm:w-56">
              {lifetimes.map((d) => (
                <option key={d} value={d}>
                  {lifetimeLabel(d)}
                </option>
              ))}
            </Select>
          </Field>
          <div className="space-y-2">
            <Checkbox label={t("Limit to particular projects")} description={t("The token then sees and touches only these projects and cannot create new ones.")} checked={confine} onChange={(e) => setConfine(e.target.checked)} />
            {confine && (
              <div className="ml-7 grid gap-1 sm:grid-cols-2">
                {(projects.data ?? []).map((p) => (
                  <Checkbox
                    key={p.id}
                    label={p.name}
                    checked={selected.includes(p.id)}
                    onChange={(e) => setSelected((cur) => (e.target.checked ? [...cur, p.id] : cur.filter((id) => id !== p.id)))}
                  />
                ))}
                {projects.data?.length === 0 && <p className="text-xs text-muted">{t("No projects yet.")}</p>}
              </div>
            )}
          </div>
          <Button type="submit" variant="primary" loading={create.isPending} disabled={!name.trim() || !max || (confine && selected.length === 0)} icon={<Plus className="size-4" />}>
            {t("Create token")}
          </Button>
        </form>
      </div>
    </Card>
  );
}
