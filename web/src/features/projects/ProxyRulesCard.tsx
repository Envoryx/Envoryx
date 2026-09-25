import { Plus, Save, ShieldCheck, Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys } from "@/api/hooks";
import type { HeaderRule, Project, ProxyRules, ProxyRulesRequest, RedirectRule } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Field, Input, Select } from "@/components/ui";
import { errorText } from "@/lib/errors";

const textareaClass = "w-full rounded-md border border-default bg-elevated p-2 font-mono text-xs text-fg focus:border-accent-500 focus:outline-none";

interface Form {
  allowIPs: string;
  auth: boolean;
  user: string;
  password: string;
  redirects: RedirectRule[];
  headers: HeaderRule[];
  cors: boolean;
  origins: string;
  methods: string;
  corsHeaders: string;
  credentials: boolean;
  maxAge: string;
}

const lines = (s: string) => s.split(/[\n,]/).map((v) => v.trim()).filter(Boolean);

function toForm(r: ProxyRules | undefined): Form {
  return {
    allowIPs: (r?.allowIPs ?? []).join("\n"),
    auth: !!r?.basicAuth,
    user: r?.basicAuth?.user ?? "",
    password: "",
    redirects: r?.redirects ?? [],
    headers: r?.headers ?? [],
    cors: !!r?.cors,
    origins: (r?.cors?.origins ?? []).join("\n"),
    methods: (r?.cors?.methods ?? []).join(", "),
    corsHeaders: (r?.cors?.headers ?? []).join(", "),
    credentials: r?.cors?.credentials ?? false,
    maxAge: r?.cors?.maxAgeSec ? String(r.cors.maxAgeSec) : "",
  };
}

function fromForm(f: Form): ProxyRulesRequest {
  const req: ProxyRulesRequest = {};
  const ips = lines(f.allowIPs);
  if (ips.length) req.allowIPs = ips;
  if (f.auth) req.basicAuth = f.password ? { user: f.user.trim(), password: f.password } : { user: f.user.trim() };
  const redirects = f.redirects.filter((r) => r.from.trim() || r.to.trim());
  if (redirects.length) req.redirects = redirects.map(({ host, ...r }) => (host ? { ...r, host } : r));
  const headers = f.headers.filter((h) => h.name.trim());
  if (headers.length) req.headers = headers;
  if (f.cors) {
    req.cors = { origins: lines(f.origins), credentials: f.credentials };
    if (lines(f.methods).length) req.cors.methods = lines(f.methods);
    if (lines(f.corsHeaders).length) req.cors.headers = lines(f.corsHeaders);
    const age = parseInt(f.maxAge, 10);
    if (age > 0) req.cors.maxAgeSec = age;
  }
  return req;
}

/**
 * What the proxy does with the project's requests before the application sees them:
 * an address allowlist, basic authentication, redirects, response headers and CORS. They
 * apply to every host name of the project and to a share.
 */
export function ProxyRulesCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const current = p.proxyRules;
  const [form, setForm] = useState<Form>(toForm(current));
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const currentKey = JSON.stringify(current ?? {});
  useEffect(() => {
    setForm(toForm(current));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- compared by content
  }, [currentKey]);

  const body = fromForm(form);
  const dirty = JSON.stringify(body) !== JSON.stringify(fromForm(toForm(current)));
  const needsPassword = form.auth && !current?.basicAuth && !form.password;
  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));
  const setRedirect = (i: number, patch: Partial<RedirectRule>) => set({ redirects: form.redirects.map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  const setHeader = (i: number, patch: Partial<HeaderRule>) => set({ headers: form.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) });

  const save = useMutation({
    mutationFn: (r: ProxyRulesRequest) => api.projects.setProxyRules(p.id, r),
    onSuccess: () => {
      setMsg({ tone: "green", text: t("Saved. The proxy applies the rules within a few seconds.") });
      setForm((f) => ({ ...f, password: "" }));
      void qc.invalidateQueries({ queryKey: keys.project(p.id) });
      void qc.invalidateQueries({ queryKey: keys.projects });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    save.mutate(body);
  };

  const active = [
    current?.allowIPs?.length && t("Allowlist"),
    current?.basicAuth && t("Password"),
    current?.redirects?.length && t("Redirects"),
    current?.headers?.length && t("Headers"),
    current?.cors && "CORS",
  ].filter(Boolean) as string[];

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <ShieldCheck className="size-4 text-accent-500" aria-hidden /> {t("Rules")}
          </span>
        }
        description={t("What the proxy does with requests before the application sees them. The rules apply to every host name of the project and to a share, not to the directly published port.")}
        actions={active.length ? <Badge tone="blue">{active.join(" · ")}</Badge> : <Badge>{t("none")}</Badge>}
      />
      <form onSubmit={submit} className="space-y-6 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}

        <section className="space-y-3">
          <h3 className="text-sm font-semibold text-fg">{t("Access")}</h3>
          <Field label={t("Allowed addresses")} htmlFor="pr-allow" hint={t("One address or network per line, e.g. 192.168.1.0/24. Empty: everyone. Behind another reverse proxy, that proxy's address is what counts.")}>
            <textarea id="pr-allow" rows={3} value={form.allowIPs} onChange={(e) => set({ allowIPs: e.target.value })} placeholder={"192.168.1.0/24\n10.8.0.5"} spellCheck={false} className={textareaClass} />
          </Field>
          <Checkbox label={t("Ask for a user name and password")} description={t("HTTP basic authentication in front of the whole project – for a staging copy or a share.")} checked={form.auth} onChange={(e) => set({ auth: e.target.checked })} />
          {form.auth && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("User name")} htmlFor="pr-user">
                <Input id="pr-user" value={form.user} onChange={(e) => set({ user: e.target.value })} autoComplete="off" spellCheck={false} />
              </Field>
              <Field label={t("Password")} htmlFor="pr-password" hint={current?.basicAuth ? t("Empty: keep the current password.") : undefined}>
                <Input id="pr-password" type="password" value={form.password} onChange={(e) => set({ password: e.target.value })} autoComplete="new-password" placeholder={current?.basicAuth ? "••••••••" : ""} />
              </Field>
            </div>
          )}
        </section>

        <section className="space-y-3">
          <h3 className="text-sm font-semibold text-fg">{t("Redirects")}</h3>
          <p className="text-xs text-subtle">{t("The first matching rule answers. /old/* matches everything below /old/; a target ending in * gets the rest of the path.")}</p>
          {form.redirects.map((r, i) => (
            <div key={i} className="grid gap-2 sm:grid-cols-[10rem_1fr_1fr_6rem_auto]">
              <Select aria-label={t("Host name")} value={r.host ?? ""} onChange={(e) => setRedirect(i, { host: e.target.value })}>
                <option value="">{t("All host names")}</option>
                {p.hostnames.map((h) => (
                  <option key={h} value={h}>
                    {h}
                  </option>
                ))}
              </Select>
              <Input aria-label={t("From")} value={r.from} onChange={(e) => setRedirect(i, { from: e.target.value })} placeholder="/old/*" className="font-mono" spellCheck={false} />
              <Input aria-label={t("To")} value={r.to} onChange={(e) => setRedirect(i, { to: e.target.value })} placeholder="/new/*" className="font-mono" spellCheck={false} />
              <Select aria-label={t("Status")} value={r.status} onChange={(e) => setRedirect(i, { status: Number(e.target.value) })}>
                {[302, 301, 307, 308].map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </Select>
              <Button type="button" size="sm" variant="ghost" onClick={() => set({ redirects: form.redirects.filter((_, j) => j !== i) })} icon={<Trash2 className="size-3.5" />} aria-label={t("Remove redirect")} />
            </div>
          ))}
          <Button type="button" size="sm" onClick={() => set({ redirects: [...form.redirects, { from: "", to: "", status: 302 }] })} icon={<Plus className="size-3.5" />}>
            {t("Add redirect")}
          </Button>
        </section>

        <section className="space-y-3">
          <h3 className="text-sm font-semibold text-fg">{t("Response headers")}</h3>
          <p className="text-xs text-subtle">{t("Set on every answer of the application. An empty value removes the header, e.g. X-Powered-By.")}</p>
          {form.headers.map((h, i) => (
            <div key={i} className="grid gap-2 sm:grid-cols-[14rem_1fr_auto]">
              <Input aria-label={t("Header")} value={h.name} onChange={(e) => setHeader(i, { name: e.target.value })} placeholder="X-Robots-Tag" className="font-mono" spellCheck={false} />
              <Input aria-label={t("Value")} value={h.value} onChange={(e) => setHeader(i, { value: e.target.value })} placeholder="noindex" className="font-mono" spellCheck={false} />
              <Button type="button" size="sm" variant="ghost" onClick={() => set({ headers: form.headers.filter((_, j) => j !== i) })} icon={<Trash2 className="size-3.5" />} aria-label={t("Remove header")} />
            </div>
          ))}
          <Button type="button" size="sm" onClick={() => set({ headers: [...form.headers, { name: "", value: "" }] })} icon={<Plus className="size-3.5" />}>
            {t("Add header")}
          </Button>
        </section>

        <section className="space-y-3">
          <h3 className="text-sm font-semibold text-fg">CORS</h3>
          <Checkbox label={t("Allow requests from other origins")} description={t("The proxy answers preflight requests and adds the CORS headers – for a frontend on another host name calling this project's API.")} checked={form.cors} onChange={(e) => set({ cors: e.target.checked })} />
          {form.cors && (
            <div className="space-y-3">
              <Field label={t("Origins")} htmlFor="pr-origins" hint={t("One per line: https://app.test, https://*.shop.test or * for every origin.")}>
                <textarea id="pr-origins" rows={3} value={form.origins} onChange={(e) => set({ origins: e.target.value })} placeholder="https://app.test" spellCheck={false} className={textareaClass} />
              </Field>
              <div className="grid gap-3 sm:grid-cols-3">
                <Field label={t("Methods")} htmlFor="pr-methods" hint={t("Empty: the usual ones")}>
                  <Input id="pr-methods" value={form.methods} onChange={(e) => set({ methods: e.target.value })} placeholder="GET, POST" className="font-mono" />
                </Field>
                <Field label={t("Request headers")} htmlFor="pr-headers" hint={t("Empty: what the browser asks for")}>
                  <Input id="pr-headers" value={form.corsHeaders} onChange={(e) => set({ corsHeaders: e.target.value })} placeholder="Content-Type, Authorization" className="font-mono" />
                </Field>
                <Field label={t("Cache preflight (seconds)")} htmlFor="pr-maxage">
                  <Input id="pr-maxage" type="number" min={0} max={86400} value={form.maxAge} onChange={(e) => set({ maxAge: e.target.value })} placeholder="600" />
                </Field>
              </div>
              <Checkbox label={t("Allow cookies and credentials")} checked={form.credentials} onChange={(e) => set({ credentials: e.target.checked })} />
            </div>
          )}
        </section>

        <div className="flex items-center gap-2">
          <Button type="submit" variant="primary" size="sm" loading={save.isPending} disabled={!dirty || needsPassword} icon={<Save className="size-3.5" />}>
            {t("Save")}
          </Button>
          {needsPassword && <span className="text-xs text-subtle">{t("Enter a password for the basic authentication.")}</span>}
        </div>
      </form>
    </Card>
  );
}
