import { Download, Save, ShieldCheck, Trash2, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useSettings, useTLSInfo, useUpdateSettings } from "@/api/hooks";
import type { TLSInfo } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Field, Input, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";
import { AcmeForm } from "./AcmeForm";
import { errorText } from "@/lib/errors";

type Msg = { tone: "green" | "red"; text: string } | null;

function ProxyStatus({ tls }: { tls: TLSInfo }) {
  const { t } = useTranslation();
  const p = tls.proxy;
  if (!p.enabled) {
    return (
      <Alert tone="amber" title={t("Embedded proxy disabled")}>
        {t("Set ENVORYX_PROXY_HTTP (default :80) and ENVORYX_PROXY_HTTPS (default :443) to enable host-name routing.")}
      </Alert>
    );
  }
  const missing = p.httpPort === 0 && p.httpsPort === 0;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted">{t("Proxy")}</span>
        <Badge tone={missing ? "amber" : "green"}>{missing ? t("ports not published") : t("active")}</Badge>
        <span className="text-muted">HTTP</span>
        <Badge tone={p.httpPort ? "green" : "gray"}>{p.httpPort ? t("host port {{port}}", { port: p.httpPort }) : t("not published")}</Badge>
        <span className="text-muted">HTTPS</span>
        <Badge tone={p.httpsPort && p.tls ? "green" : "gray"}>{p.httpsPort && p.tls ? t("host port {{port}}", { port: p.httpsPort }) : t("not published")}</Badge>
      </div>
      {p.address && (
        <Alert tone="blue" title={t("Envoryx has its own IP address: {{address}}", { address: p.address })}>
          {t("The proxy is reachable directly on that address (no port mapping needed). Point your DNS entries for the base domain at")} <Code>{p.address}</Code> – {t("not at the Docker host.")}
        </Alert>
      )}
      {missing && p.inDocker && (
        <Alert tone="amber" title={t("Map the proxy ports")}>
          {t("The Envoryx container listens on 80 and 443, but neither port is published on the host. Add port mappings 80:80 and 443:443 (or any free host ports) to the container, then restart it. Project links keep using the direct port until then.")}
        </Alert>
      )}
      {missing && !p.inDocker && (
        <Alert tone="amber" title={t("Proxy ports unavailable")}>
          {t("Binding ports 80/443 on bare metal needs elevated privileges (e.g. setcap cap_net_bind_service=+ep envoryx) or other listen addresses via ENVORYX_PROXY_HTTP/ENVORYX_PROXY_HTTPS.")}
        </Alert>
      )}
    </div>
  );
}

function BaseDomainForm({ baseDomain, forceHttps, tlsAvailable }: { baseDomain: string; forceHttps: boolean; tlsAvailable: boolean }) {
  const { t } = useTranslation();
  const update = useUpdateSettings();
  const [base, setBase] = useState(baseDomain);
  const [force, setForce] = useState(forceHttps);
  const [msg, setMsg] = useState<Msg>(null);
  useEffect(() => setBase(baseDomain), [baseDomain]);
  useEffect(() => setForce(forceHttps), [forceHttps]);

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    update.mutate(
      { baseDomain: base.trim(), forceHttps: force },
      {
        onSuccess: () => setMsg({ tone: "green", text: t("Saved. Project domains follow the new base domain immediately.") }),
        onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
      },
    );
  }
  const dirty = base.trim() !== baseDomain || force !== forceHttps;
  return (
    <form onSubmit={submit} className="space-y-4">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <Field label={t("Base domain")} htmlFor="base-domain" hint={t("Projects are reachable at <slug>.{{base}}, the Envoryx UI at envoryx.{{base}}. Point *.{{base}} at this host in your DNS (Pi-hole, AdGuard, dnsmasq) or add entries to your hosts file.", { base: base.trim() || "test" })}>
        <Input id="base-domain" value={base} onChange={(e) => setBase(e.target.value)} placeholder="test" spellCheck={false} autoCapitalize="none" />
      </Field>
      <Checkbox label={t("Force HTTPS")} description={tlsAvailable ? t("Redirect plain HTTP requests for project and UI domains to HTTPS.") : t("Requires the HTTPS listener to be published.")} checked={force} onChange={(e) => setForce(e.target.checked)} disabled={!tlsAvailable} />
      <Button type="submit" variant="primary" loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
        {t("Save")}
      </Button>
    </form>
  );
}

function CustomCertForm({ tls }: { tls: TLSInfo }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [cert, setCert] = useState("");
  const [key, setKey] = useState("");
  const [msg, setMsg] = useState<Msg>(null);
  const refresh = (data: TLSInfo) => {
    qc.setQueryData(["tls"], data);
    setCert("");
    setKey("");
  };
  const set = useMutation({
    mutationFn: () => api.tls.setCustom(cert, key),
    onSuccess: (data) => {
      refresh(data);
      setMsg({ tone: "green", text: t("Custom certificate installed. It is used for every name it covers.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Installing the certificate failed")) }),
  });
  const clear = useMutation({
    mutationFn: () => api.tls.clearCustom(),
    onSuccess: (data) => {
      refresh(data);
      setMsg({ tone: "green", text: t("Custom certificate removed.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Removing the certificate failed")) }),
  });
  const custom = tls.ca?.custom ?? null;
  return (
    <div className="space-y-4">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      {custom ? (
        <div className="rounded-md border border-default p-3 text-sm">
          <p className="flex items-center gap-2 font-medium">
            <ShieldCheck className="size-4 text-emerald-500" aria-hidden /> {custom.subject}
            {custom.expired && <Badge tone="red">{t("expired")}</Badge>}
          </p>
          <dl className="mt-2 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-[8rem_1fr]">
            <dt className="text-muted">{t("Names")}</dt>
            <dd className="font-mono">{custom.dnsNames.join(", ")}</dd>
            <dt className="text-muted">{t("Issuer")}</dt>
            <dd>{custom.issuer}</dd>
            <dt className="text-muted">{t("Valid until")}</dt>
            <dd>{formatDateTime(custom.notAfter)}</dd>
          </dl>
          <Button size="sm" variant="ghost" className="mt-3" onClick={() => clear.mutate()} loading={clear.isPending} icon={<Trash2 className="size-3.5" />}>
            {t("Remove custom certificate")}
          </Button>
        </div>
      ) : (
        <p className="text-sm text-muted">
          {t("Optional: upload a certificate from your own CA or a public wildcard certificate (e.g. *.dev.example.com via Let's Encrypt DNS challenge). It is used for the names it covers; everything else keeps using the local CA.")}
        </p>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setMsg(null);
          set.mutate();
        }}
        className="grid gap-4 sm:grid-cols-2"
      >
        <Field label={t("Certificate (PEM, full chain)")} htmlFor="custom-cert">
          <textarea id="custom-cert" value={cert} onChange={(e) => setCert(e.target.value)} rows={6} spellCheck={false} className="w-full rounded-md border border-default bg-elevated p-2 font-mono text-[11px] text-fg focus:border-accent-500 focus:outline-none" placeholder="-----BEGIN CERTIFICATE-----" />
        </Field>
        <Field label={t("Private key (PEM)")} htmlFor="custom-key" hint={t("Stored under /config/ca with owner-only permissions. Never shown again.")}>
          <textarea id="custom-key" value={key} onChange={(e) => setKey(e.target.value)} rows={6} spellCheck={false} className="w-full rounded-md border border-default bg-elevated p-2 font-mono text-[11px] text-fg focus:border-accent-500 focus:outline-none" placeholder="-----BEGIN PRIVATE KEY-----" />
        </Field>
        <div className="sm:col-span-2">
          <Button type="submit" variant="primary" loading={set.isPending} disabled={!cert.trim() || !key.trim()} icon={<Upload className="size-4" />}>
            {t("Install certificate")}
          </Button>
        </div>
      </form>
    </div>
  );
}

const trustSteps: { os: string; steps: string }[] = [
  { os: "Windows", steps: "Double-click envoryx-ca.crt → Install Certificate → Local Machine → “Place all certificates in the following store” → Trusted Root Certification Authorities." },
  { os: "macOS", steps: "Open the file in Keychain Access (System keychain), then double-click the certificate → Trust → “Always Trust”." },
  { os: "Linux", steps: "sudo cp envoryx-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates (Debian/Ubuntu) or sudo trust anchor envoryx-ca.crt (Fedora/Arch)." },
  { os: "iOS / iPadOS", steps: "Open the file, install the profile under Settings → General → VPN & Device Management, then enable it under Settings → General → About → Certificate Trust Settings." },
  { os: "Android", steps: "Settings → Security → Encryption & credentials → Install a certificate → CA certificate." },
  { os: "Firefox", steps: "Uses its own store: Settings → Privacy & Security → Certificates → View Certificates → Authorities → Import (or set security.enterprise_roots.enabled to true)." },
];

export function DomainsCard() {
  const { t } = useTranslation();
  const settings = useSettings();
  const tls = useTLSInfo();
  if (settings.isPending || tls.isPending) return <Card><CardHeader title={t("Domains & HTTPS")} /><Spinner /></Card>;
  if (settings.isError || tls.isError) return <Card><CardHeader title={t("Domains & HTTPS")} /><div className="p-5"><Alert tone="red">{(settings.error ?? tls.error)?.message}</Alert></div></Card>;
  const info = tls.data;
  const tlsAvailable = info.enabled && info.proxy.httpsPort > 0;
  return (
    <Card>
      <CardHeader title={t("Domains & HTTPS")} description={t("The embedded reverse proxy opens every project under its own host name and issues certificates from a local certificate authority.")} />
      <div className="space-y-6 p-5">
        <ProxyStatus tls={info} />
        <BaseDomainForm baseDomain={settings.data.baseDomain} forceHttps={settings.data.forceHttps} tlsAvailable={tlsAvailable} />
        {info.enabled && info.ca && (
          <>
            <div className="border-t border-default pt-5">
              <h3 className="text-sm font-semibold">{t("Local certificate authority")}</h3>
              <p className="mt-1 text-sm text-muted">
                {t("Install the CA once on each device that should open projects over HTTPS without warnings. Only the public certificate leaves Envoryx; the key stays in /config/ca.")}
              </p>
              <dl className="mt-3 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-[8rem_1fr]">
                <dt className="text-muted">{t("Subject")}</dt>
                <dd>{info.ca.caSubject}</dd>
                <dt className="text-muted">SHA-256</dt>
                <dd className="break-all font-mono">{info.ca.caFingerprint}</dd>
                <dt className="text-muted">{t("Valid until")}</dt>
                <dd>{formatDateTime(info.ca.caNotAfter)}</dd>
              </dl>
              <a href={api.tls.caUrl} download="envoryx-ca.crt" className="mt-3 inline-flex h-9 items-center gap-2 rounded-md bg-accent-600 px-3.5 text-sm font-medium text-white shadow-sm hover:bg-accent-500">
                <Download className="size-4" aria-hidden /> {t("Download envoryx-ca.crt")}
              </a>
              <details className="mt-3 text-sm">
                <summary className="cursor-pointer text-muted hover:text-fg">{t("How to trust the CA")}</summary>
                <ul className="mt-2 space-y-1.5 text-xs text-muted">
                  {trustSteps.map((step) => (
                    <li key={step.os}>
                      <span className="font-medium text-fg">{step.os}:</span> {t(step.steps)}
                    </li>
                  ))}
                </ul>
              </details>
            </div>
            <div className="border-t border-default pt-5">
              <h3 className="text-sm font-semibold">{t("Let's Encrypt (public certificate, no CA installation)")}</h3>
              <div className="mt-3">
                <AcmeForm baseDomain={settings.data.baseDomain} />
              </div>
            </div>
            <div className="border-t border-default pt-5">
              <h3 className="text-sm font-semibold">{t("Custom certificate")}</h3>
              <div className="mt-3">
                <CustomCertForm tls={info} />
              </div>
            </div>
          </>
        )}
      </div>
    </Card>
  );
}
