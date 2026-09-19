import { Bot, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { Alert, Button, Card, CardHeader, Code, Field, Input, Spinner } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/format";

const key = ["tokens"] as const;

function mcpConfig(url: string, secret: string): string {
  return JSON.stringify({ mcpServers: { envoryx: { type: "http", url, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2);
}

export function TokensCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: key, queryFn: api.tokens.list });
  const [name, setName] = useState("");
  const [created, setCreated] = useState<{ secret: string; url: string; name: string } | null>(null);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: (n: string) => api.tokens.create(n),
    onSuccess: (r) => {
      setCreated({ secret: r.secret, url: r.mcpUrl, name: r.token.name });
      setName("");
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Creating the token failed") }),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.tokens.revoke(id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: key }),
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Revoking failed") }),
  });

  async function copy(label: string, value: string) {
    setCopied((await copyText(value)) ? label : null);
    setTimeout(() => setCopied(null), 2000);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    setCreated(null);
    create.mutate(name.trim());
  }

  return (
    <Card>
      <CardHeader
        title={t("API tokens & MCP")}
        description={t("Tokens act with your account and authenticate AI assistants (Claude Code, Cursor, …) on the MCP server, SSH/SFTP logins and scripts calling the REST API with an Authorization: Bearer header. The MCP tools expose no destructive operations; a token can never change the password or manage tokens.")}
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {created && (
          <Alert tone="green" title={t('Token "{{name}}" created – copy it now, it is not shown again.', { name: created.name })}>
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
          <Alert tone="red">{q.error.message}</Alert>
        ) : q.data.tokens.length === 0 ? (
          <p className="text-sm text-muted">{t("No tokens yet. The MCP endpoint is")} <Code>{q.data.mcpUrl}</Code>.</p>
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {q.data.tokens.map((tok) => (
              <li key={tok.id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm">
                <div className="flex items-center gap-3">
                  <Bot className="size-4 text-accent-500" aria-hidden />
                  <div>
                    <p className="font-medium">{tok.name}</p>
                    <p className="font-mono text-[11px] text-subtle">
                      {tok.prefix}… · {t("created {{date}}", { date: formatDateTime(tok.createdAt) })} · {tok.lastUsedAt ? t("last used {{date}}", { date: formatDateTime(tok.lastUsedAt) }) : t("never used")}
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
        <form onSubmit={submit} className="flex items-end gap-2">
          <div className="flex-1">
            <Field label={t("New token")} htmlFor="token-name" hint={t("A name that tells you which client uses it.")}>
              <Input id="token-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t("Claude Code on my laptop")} maxLength={64} />
            </Field>
          </div>
          <Button type="submit" variant="primary" className="mb-6" loading={create.isPending} disabled={!name.trim()} icon={<Plus className="size-4" />}>
            {t("Create token")}
          </Button>
        </form>
      </div>
    </Card>
  );
}
