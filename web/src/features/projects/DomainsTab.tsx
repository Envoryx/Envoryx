import { ExternalLink, Globe, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { keys, useProjectDomains, useProjectLinks } from "@/api/hooks";
import type { Project, ProxyInfo } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Code, ErrorState, Field, Input, Spinner } from "@/components/ui";

/** URL of a host name through the proxy, honouring non-standard published ports. */
export function proxyUrl(host: string, proxy: ProxyInfo): string {
  if (proxy.tls && proxy.httpsPort > 0) return `https://${host}${proxy.httpsPort === 443 ? "" : `:${proxy.httpsPort}`}`;
  if (proxy.httpPort > 0) return `http://${host}${proxy.httpPort === 80 ? "" : `:${proxy.httpPort}`}`;
  return "";
}

export function DomainsTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const q = useProjectDomains(project.id);
  const qc = useQueryClient();
  const links = useProjectLinks();
  const [hostname, setHostname] = useState("");
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: keys.project(project.id) });
    void qc.invalidateQueries({ queryKey: keys.projects });
  };
  const add = useMutation({
    mutationFn: (h: string) => api.projects.domains.add(project.id, h),
    onSuccess: (r) => {
      setHostname("");
      setMsg({ tone: "green", text: t("{{hostname}} added.", { hostname: r.domain.hostname }) });
      invalidate();
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Adding the domain failed") }),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.projects.domains.remove(project.id, id),
    onSuccess: invalidate,
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Removing the domain failed") }),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    add.mutate(hostname.trim());
  }

  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={q.error.message} />;
  const { domains, proxy } = q.data;
  const { direct } = links(project);
  const published = proxy.enabled && (proxy.httpPort > 0 || proxy.httpsPort > 0);

  return (
    <div className="space-y-6">
      {!proxy.enabled ? (
        <Alert tone="amber" title={t("The embedded proxy is disabled")}>
          {t("Domains need the proxy (ENVORYX_PROXY_HTTP / ENVORYX_PROXY_HTTPS). The project stays reachable at")} <Code>{direct}</Code>.
        </Alert>
      ) : !published ? (
        <Alert tone="amber" title={t("Proxy ports are not published")}>
          {t("Map host ports 80 and 443 to the Envoryx container to open projects by domain.")} <Link to="/settings?tab=domains" className="underline">{t("Settings → Domains & HTTPS")}</Link>
        </Alert>
      ) : null}

      <Card>
        <CardHeader
          title={t("Domains")}
          description={t("Every project gets slug.base-domain automatically. Additional names route to this project through the proxy; point them at the Envoryx host in your DNS or hosts file.")}
        />
        <ul className="divide-y divide-[var(--border)]">
          {domains.map((d) => {
            const url = proxyUrl(d.hostname, proxy);
            return (
              <li key={d.id ?? d.hostname} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3">
                <div className="flex min-w-0 items-center gap-3">
                  <Globe className="size-4 shrink-0 text-accent-500" aria-hidden />
                  <div className="min-w-0">
                    <p className="flex items-center gap-2 font-mono text-sm">
                      {d.hostname}
                      {d.default && <Badge tone="blue">{t("default")}</Badge>}
                    </p>
                    {url && (
                      <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-xs text-accent-600 hover:underline dark:text-accent-300">
                        {url} <ExternalLink className="size-3" />
                      </a>
                    )}
                  </div>
                </div>
                {!d.default && d.id && (
                  <Button size="sm" variant="ghost" onClick={() => remove.mutate(d.id!)} loading={remove.isPending && remove.variables === d.id} icon={<Trash2 className="size-3.5" />} aria-label={t("Remove {{name}}", { name: d.hostname })}>
                    {t("Remove")}
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
        <form onSubmit={submit} className="space-y-3 border-t border-default p-5">
          {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
          <Field label={t("Add domain")} htmlFor="new-domain" hint={t("Lower-case host name, e.g. shop.local or api.shop.test. Wildcards are not supported.")}>
            <div className="flex gap-2">
              <Input id="new-domain" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="shop.local" spellCheck={false} autoCapitalize="none" />
              <Button type="submit" variant="primary" loading={add.isPending} disabled={!hostname.trim()} icon={<Plus className="size-4" />}>
                {t("Add")}
              </Button>
            </div>
          </Field>
        </form>
      </Card>

      <Card>
        <CardHeader title={t("Direct access")} description={t("The web server port published on the Docker host. Works without DNS or the proxy.")} />
        <div className="p-5 text-sm">
          {direct ? (
            <a href={direct} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
              {direct} <ExternalLink className="size-3" />
            </a>
          ) : (
            <span className="text-muted">{t("No port assigned.")}</span>
          )}
        </div>
      </Card>
    </div>
  );
}
