import { Globe, RefreshCw, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { ACMEInfo, ACMEProviderInfo } from "@/api/types";
import { Alert, Badge, Button, Checkbox, Code, Field, Input, Select, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";
import { errorText, translateMessage } from "@/lib/errors";

const key = ["tls", "acme"] as const;

/** The provider list of the server, or Cloudflare with a token for a server without one. */
function providerList(info: ACMEInfo | undefined): ACMEProviderInfo[] {
  if (info?.providerList?.length) return info.providerList;
  return Object.entries(info?.providers ?? { cloudflare: "Cloudflare" }).map(([k, name]) => ({
    key: k,
    name,
    propagationMinutes: 5,
    fields: [{ key: "token", label: "API token", secret: true }],
  }));
}

/**
 * Let's Encrypt wildcard certificate via DNS challenge: no CA installation on clients.
 * Returns the current ACME status so the parent can hide the manual upload while active.
 */
export function AcmeForm({ baseDomain }: { baseDomain: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: key,
    queryFn: api.tls.acme,
    refetchInterval: (query) => (query.state.data?.status?.issuing ? 3000 : false),
  });
  const [provider, setProvider] = useState("cloudflare");
  const [domain, setDomain] = useState("");
  const [email, setEmail] = useState("");
  const [creds, setCreds] = useState<Record<string, string>>({});
  const [staging, setStaging] = useState(false);
  const [useAsBase, setUseAsBase] = useState(true);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const status = q.data?.status;
  useEffect(() => {
    if (!status?.configured) return;
    setProvider(status.provider ?? "cloudflare");
    setDomain(status.domain ?? "");
    setEmail(status.email ?? "");
    setStaging(status.staging ?? false);
    setCreds({ ...(status.fields ?? {}) });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- compared by content: the poll while issuing brings a new object every time
  }, [status?.configured, status?.provider, status?.domain, status?.email, status?.staging, JSON.stringify(status?.fields ?? {})]);
  const providers = providerList(q.data);
  const current = providers.find((p) => p.key === provider) ?? providers[0];
  // Stored secrets count only for the provider they were stored for.
  const stored = (field: string) => status?.configured === true && status.provider === provider && (status.secrets ?? []).includes(field);
  const credsComplete = (current?.fields ?? []).every((f) => f.optional || (creds[f.key] ?? "").trim() !== "" || (f.secret && stored(f.key)));
  const selectProvider = (next: string) => {
    setProvider(next);
    setCreds(next === status?.provider ? { ...(status?.fields ?? {}) } : {});
  };

  const refresh = (data: ACMEInfo) => {
    qc.setQueryData(key, data);
    void qc.invalidateQueries({ queryKey: ["tls"] });
    void qc.invalidateQueries({ queryKey: ["settings"] });
    void qc.invalidateQueries({ queryKey: ["projects"] });
  };
  const save = useMutation({
    mutationFn: () => {
      const credentials: Record<string, string> = {};
      for (const f of current?.fields ?? []) {
        const v = (creds[f.key] ?? "").trim();
        if (v) credentials[f.key] = v;
      }
      return api.tls.setAcme({ provider, domain: domain.trim(), email: email.trim(), credentials, staging, useAsBaseDomain: useAsBase });
    },
    onSuccess: (data) => {
      refresh(data);
      setCreds({ ...(data.status?.fields ?? {}) });
      setMsg({ tone: "green", text: t("Saved. The certificate is being requested in the background; this takes a minute or two.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  const issue = useMutation({
    mutationFn: () => api.tls.issueAcme(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      setMsg({ tone: "green", text: t("Renewal requested.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Request failed")) }),
  });
  const clear = useMutation({
    mutationFn: () => api.tls.clearAcme(),
    onSuccess: (data) => {
      refresh(data);
      setDomain("");
      setEmail("");
      setCreds({});
      setMsg({ tone: "green", text: t("Let's Encrypt configuration and certificate removed.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Removing failed")) }),
  });

  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert tone="red">{errorText(q.error, t)}</Alert>;
  if (!q.data.available) return <p className="text-sm text-muted">{t("Not available (HTTPS listener disabled).")}</p>;

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    save.mutate();
  }
  const configured = status?.configured ?? false;
  const credsChanged = (current?.fields ?? []).some((f) => (creds[f.key] ?? "").trim() !== (f.secret ? "" : (status?.fields?.[f.key] ?? "")));
  const dirty = !configured || domain.trim() !== status?.domain || email.trim() !== status?.email || credsChanged || staging !== (status?.staging ?? false) || provider !== status?.provider;

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted">
        {t("Get a public wildcard certificate for your own domain (e.g. *.dev.example.com) from Let's Encrypt through a DNS challenge. Browsers trust it out of the box – no CA installation on any device. Nothing is exposed to the internet: only a temporary TXT record is created at your DNS provider; the domain never needs to point at this host publicly.")} {t("Projects are then")} <Code>&lt;slug&gt;.{domain.trim() || "dev.example.com"}</Code>; {t("point")} <Code>*.{domain.trim() || "dev.example.com"}</Code> {t("at Envoryx in your local DNS.")}
      </p>
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      {configured && status && (
        <div className="rounded-md border border-default p-3 text-sm">
          <p className="flex flex-wrap items-center gap-2 font-medium">
            <Globe className="size-4 text-accent-500" aria-hidden /> *.{status.domain}
            {status.issuing ? <Badge tone="blue">{t("requesting…")}</Badge> : status.notAfter ? <Badge tone="green">{t("active")}</Badge> : <Badge tone="amber">{t("no certificate yet")}</Badge>}
            {status.staging && <Badge tone="amber">{t("staging")}</Badge>}
          </p>
          <dl className="mt-2 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-[8rem_1fr]">
            <dt className="text-muted">{t("Provider")}</dt>
            <dd>{providers.find((p) => p.key === status.provider)?.name ?? status.provider}</dd>
            {status.notAfter && (
              <>
                <dt className="text-muted">{t("Valid until")}</dt>
                <dd>{formatDateTime(status.notAfter)} {t("(renewed automatically 30 days before)")}</dd>
              </>
            )}
            {status.lastAttempt && (
              <>
                <dt className="text-muted">{t("Last attempt")}</dt>
                <dd>{formatDateTime(status.lastAttempt)}</dd>
              </>
            )}
          </dl>
          {status.lastError && (
            <div className="mt-2">
              <Alert tone="red" title={t("Last attempt failed")}>{translateMessage(status.lastError, t)}</Alert>
            </div>
          )}
          {baseDomain !== status.domain && (
            <p className="mt-2 text-xs text-amber-600 dark:text-amber-400">{t("The project base domain is currently")} <Code>{baseDomain}</Code>; {t("save with “Use as base domain” to switch.")}</p>
          )}
          <div className="mt-3 flex gap-2">
            <Button size="sm" onClick={() => issue.mutate()} loading={issue.isPending} disabled={status.issuing} icon={<RefreshCw className="size-3.5" />}>
              {t("Renew now")}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => clear.mutate()} loading={clear.isPending} icon={<Trash2 className="size-3.5" />}>
              {t("Remove")}
            </Button>
          </div>
        </div>
      )}
      <form onSubmit={submit} className="grid gap-4 sm:grid-cols-2">
        <Field label={t("DNS provider")} htmlFor="acme-provider">
          <Select id="acme-provider" value={provider} onChange={(e) => selectProvider(e.target.value)}>
            {providers.map((p) => (
              <option key={p.key} value={p.key}>{p.name}</option>
            ))}
          </Select>
        </Field>
        <Field label={t("Domain")} htmlFor="acme-domain" hint={t("Wildcard is added automatically: *.dev.example.com + dev.example.com")}>
          <Input id="acme-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="dev.example.com" spellCheck={false} autoCapitalize="none" />
        </Field>
        <Field label={t("E-mail")} htmlFor="acme-email" hint={t("Let's Encrypt account contact (expiry warnings)")}>
          <Input id="acme-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
        </Field>
        {current?.fields.map((f) => (
          <Field
            key={`${current.key}-${f.key}`}
            label={f.optional ? t("{{label}} (optional)", { label: t(f.label) }) : t(f.label)}
            htmlFor={`acme-${f.key}`}
            hint={f.secret && stored(f.key) ? t("Stored. Leave empty to keep it.") : f.hint ? t(f.hint) : undefined}
          >
            <Input
              id={`acme-${f.key}`}
              type={f.secret ? "password" : "text"}
              autoComplete="off"
              spellCheck={false}
              value={creds[f.key] ?? ""}
              onChange={(e) => setCreds((c) => ({ ...c, [f.key]: e.target.value }))}
              placeholder={f.secret && stored(f.key) ? "••••••••" : ""}
            />
          </Field>
        ))}
        {current && current.propagationMinutes > 5 && (
          <p className="text-xs text-amber-600 sm:col-span-2 dark:text-amber-400">
            {t("{{name}} takes a while to publish new records; Envoryx waits up to {{minutes}} minutes for them, so a certificate takes that much longer.", { name: current.name, minutes: current.propagationMinutes })}
          </p>
        )}
        <div className="space-y-2 sm:col-span-2">
          <Checkbox label={t("Use as base domain")} description={t("Projects become <slug>.<domain> and the UI envoryx.<domain>.")} checked={useAsBase} onChange={(e) => setUseAsBase(e.target.checked)} />
          <Checkbox label={t("Use Let's Encrypt staging")} description={t("For testing the setup without hitting rate limits; staging certificates are not trusted by browsers.")} checked={staging} onChange={(e) => setStaging(e.target.checked)} />
        </div>
        <div className="sm:col-span-2">
          <Button type="submit" variant="primary" loading={save.isPending} disabled={!domain.trim() || !email.trim() || !credsComplete || !dirty}>
            {configured ? t("Save changes") : t("Enable Let's Encrypt")}
          </Button>
        </div>
      </form>
    </div>
  );
}
