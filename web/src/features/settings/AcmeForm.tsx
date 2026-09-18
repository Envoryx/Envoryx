import { Globe, RefreshCw, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import type { ACMEInfo } from "@/api/types";
import { Alert, Badge, Button, Checkbox, Code, Field, Input, Select, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

const key = ["tls", "acme"] as const;

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
  const [token, setToken] = useState("");
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
  }, [status?.configured, status?.provider, status?.domain, status?.email, status?.staging]);

  const refresh = (data: ACMEInfo) => {
    qc.setQueryData(key, data);
    void qc.invalidateQueries({ queryKey: ["tls"] });
    void qc.invalidateQueries({ queryKey: ["settings"] });
    void qc.invalidateQueries({ queryKey: ["projects"] });
  };
  const save = useMutation({
    mutationFn: () => api.tls.setAcme({ provider, domain: domain.trim(), email: email.trim(), token: token.trim(), staging, useAsBaseDomain: useAsBase }),
    onSuccess: (data) => {
      refresh(data);
      setToken("");
      setMsg({ tone: "green", text: t("Saved. The certificate is being requested in the background; this takes a minute or two.") });
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Saving failed") }),
  });
  const issue = useMutation({
    mutationFn: () => api.tls.issueAcme(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      setMsg({ tone: "green", text: t("Renewal requested.") });
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Request failed") }),
  });
  const clear = useMutation({
    mutationFn: () => api.tls.clearAcme(),
    onSuccess: (data) => {
      refresh(data);
      setDomain("");
      setEmail("");
      setToken("");
      setMsg({ tone: "green", text: t("Let's Encrypt configuration and certificate removed.") });
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Removing failed") }),
  });

  if (q.isPending) return <Spinner />;
  if (q.isError) return <Alert tone="red">{q.error.message}</Alert>;
  if (!q.data.available) return <p className="text-sm text-muted">{t("Not available (HTTPS listener disabled).")}</p>;

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    save.mutate();
  }
  const configured = status?.configured ?? false;
  const dirty = !configured || domain.trim() !== status?.domain || email.trim() !== status?.email || token.trim() !== "" || staging !== (status?.staging ?? false) || provider !== status?.provider;

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted">
        {t("Get a public wildcard certificate for your own domain (e.g. *.dev.example.com) from Let's Encrypt through a DNS challenge. Browsers trust it out of the box – no CA installation on any device. Nothing is exposed to the internet: only a temporary TXT record is created at your DNS provider; the domain never needs to point at this host publicly.")} {t("Projects are then")} <Code>&lt;slug&gt;.{domain.trim() || "dev.example.com"}</Code>; {t("point")} <Code>*.{domain.trim() || "dev.example.com"}</Code> {t("at Staqio in your local DNS.")}
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
            <dd>{q.data.providers[status.provider ?? ""] ?? status.provider}</dd>
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
              <Alert tone="red" title={t("Last attempt failed")}>{status.lastError}</Alert>
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
          <Select id="acme-provider" value={provider} onChange={(e) => setProvider(e.target.value)}>
            {Object.entries(q.data.providers).map(([k, v]) => (
              <option key={k} value={k}>{v}</option>
            ))}
          </Select>
        </Field>
        <Field label={t("Domain")} htmlFor="acme-domain" hint={t("Wildcard is added automatically: *.dev.example.com + dev.example.com")}>
          <Input id="acme-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="dev.example.com" spellCheck={false} autoCapitalize="none" />
        </Field>
        <Field label={t("E-mail")} htmlFor="acme-email" hint={t("Let's Encrypt account contact (expiry warnings)")}>
          <Input id="acme-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
        </Field>
        <Field label={t("API token")} htmlFor="acme-token" hint={configured ? t("Leave empty to keep the stored token.") : t("Cloudflare: My Profile → API Tokens → Create Token → “Edit zone DNS” template (Zone:Read + DNS:Edit for the zone).")}>
          <Input id="acme-token" type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder={configured ? "••••••••" : ""} />
        </Field>
        <div className="space-y-2 sm:col-span-2">
          <Checkbox label={t("Use as base domain")} description={t("Projects become <slug>.<domain> and the UI staqio.<domain>.")} checked={useAsBase} onChange={(e) => setUseAsBase(e.target.checked)} />
          <Checkbox label={t("Use Let's Encrypt staging")} description={t("For testing the setup without hitting rate limits; staging certificates are not trusted by browsers.")} checked={staging} onChange={(e) => setStaging(e.target.checked)} />
        </div>
        <div className="sm:col-span-2">
          <Button type="submit" variant="primary" loading={save.isPending} disabled={!domain.trim() || !email.trim() || (!configured && !token.trim()) || !dirty}>
            {configured ? t("Save changes") : t("Enable Let's Encrypt")}
          </Button>
        </div>
      </form>
    </div>
  );
}
