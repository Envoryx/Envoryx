import { Download, Save, ShieldCheck, Trash2, Upload } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { useSettings, useTLSInfo, useUpdateSettings } from "@/api/hooks";
import type { TLSInfo } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Field, Input, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

type Msg = { tone: "green" | "red"; text: string } | null;

function ProxyStatus({ tls }: { tls: TLSInfo }) {
  const p = tls.proxy;
  if (!p.enabled) {
    return (
      <Alert tone="amber" title="Embedded proxy disabled">
        Set <Code>STAQIO_PROXY_HTTP</Code> (default <Code>:80</Code>) and <Code>STAQIO_PROXY_HTTPS</Code> (default <Code>:443</Code>) to enable host-name routing.
      </Alert>
    );
  }
  const missing = p.httpPort === 0 && p.httpsPort === 0;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted">Proxy</span>
        <Badge tone={missing ? "amber" : "green"}>{missing ? "ports not published" : "active"}</Badge>
        <span className="text-muted">HTTP</span>
        <Badge tone={p.httpPort ? "green" : "gray"}>{p.httpPort ? `host port ${p.httpPort}` : "not published"}</Badge>
        <span className="text-muted">HTTPS</span>
        <Badge tone={p.httpsPort && p.tls ? "green" : "gray"}>{p.httpsPort && p.tls ? `host port ${p.httpsPort}` : "not published"}</Badge>
      </div>
      {missing && p.inDocker && (
        <Alert tone="amber" title="Map the proxy ports">
          The Staqio container listens on 80 and 443, but neither port is published on the host. Add port mappings <Code>80:80</Code> and <Code>443:443</Code> (or any free host ports) to the container, then restart it. Project links keep using the direct port until then.
        </Alert>
      )}
      {missing && !p.inDocker && (
        <Alert tone="amber" title="Proxy ports unavailable">
          Binding ports 80/443 on bare metal needs elevated privileges (e.g. <Code>setcap cap_net_bind_service=+ep staqio</Code>) or other listen addresses via <Code>STAQIO_PROXY_HTTP</Code>/<Code>STAQIO_PROXY_HTTPS</Code>.
        </Alert>
      )}
    </div>
  );
}

function BaseDomainForm({ baseDomain, forceHttps, tlsAvailable }: { baseDomain: string; forceHttps: boolean; tlsAvailable: boolean }) {
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
        onSuccess: () => setMsg({ tone: "green", text: "Saved. Project domains follow the new base domain immediately." }),
        onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Saving failed" }),
      },
    );
  }
  const dirty = base.trim() !== baseDomain || force !== forceHttps;
  return (
    <form onSubmit={submit} className="space-y-4">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <Field label="Base domain" htmlFor="base-domain" hint={`Projects are reachable at <slug>.${base.trim() || "test"}, the Staqio UI at staqio.${base.trim() || "test"}. Point *.${base.trim() || "test"} at this host in your DNS (Pi-hole, AdGuard, dnsmasq) or add entries to your hosts file.`}>
        <Input id="base-domain" value={base} onChange={(e) => setBase(e.target.value)} placeholder="test" spellCheck={false} autoCapitalize="none" />
      </Field>
      <Checkbox label="Force HTTPS" description={tlsAvailable ? "Redirect plain HTTP requests for project and UI domains to HTTPS." : "Requires the HTTPS listener to be published."} checked={force} onChange={(e) => setForce(e.target.checked)} disabled={!tlsAvailable} />
      <Button type="submit" variant="primary" loading={update.isPending} disabled={!dirty} icon={<Save className="size-4" />}>
        Save
      </Button>
    </form>
  );
}

function CustomCertForm({ tls }: { tls: TLSInfo }) {
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
      setMsg({ tone: "green", text: "Custom certificate installed. It is used for every name it covers." });
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Installing the certificate failed" }),
  });
  const clear = useMutation({
    mutationFn: () => api.tls.clearCustom(),
    onSuccess: (data) => {
      refresh(data);
      setMsg({ tone: "green", text: "Custom certificate removed." });
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Removing the certificate failed" }),
  });
  const custom = tls.ca?.custom ?? null;
  return (
    <div className="space-y-4">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      {custom ? (
        <div className="rounded-md border border-default p-3 text-sm">
          <p className="flex items-center gap-2 font-medium">
            <ShieldCheck className="size-4 text-emerald-500" aria-hidden /> {custom.subject}
            {custom.expired && <Badge tone="red">expired</Badge>}
          </p>
          <dl className="mt-2 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-[8rem_1fr]">
            <dt className="text-muted">Names</dt>
            <dd className="font-mono">{custom.dnsNames.join(", ")}</dd>
            <dt className="text-muted">Issuer</dt>
            <dd>{custom.issuer}</dd>
            <dt className="text-muted">Valid until</dt>
            <dd>{formatDateTime(custom.notAfter)}</dd>
          </dl>
          <Button size="sm" variant="ghost" className="mt-3" onClick={() => clear.mutate()} loading={clear.isPending} icon={<Trash2 className="size-3.5" />}>
            Remove custom certificate
          </Button>
        </div>
      ) : (
        <p className="text-sm text-muted">
          Optional: upload a certificate from your own CA or a public wildcard certificate (e.g. <Code>*.dev.example.com</Code> via Let&apos;s Encrypt DNS challenge). It is used for the names it covers; everything else keeps using the local CA.
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
        <Field label="Certificate (PEM, full chain)" htmlFor="custom-cert">
          <textarea id="custom-cert" value={cert} onChange={(e) => setCert(e.target.value)} rows={6} spellCheck={false} className="w-full rounded-md border border-default bg-elevated p-2 font-mono text-[11px] text-fg focus:border-accent-500 focus:outline-none" placeholder="-----BEGIN CERTIFICATE-----" />
        </Field>
        <Field label="Private key (PEM)" htmlFor="custom-key" hint="Stored under /config/ca with owner-only permissions. Never shown again.">
          <textarea id="custom-key" value={key} onChange={(e) => setKey(e.target.value)} rows={6} spellCheck={false} className="w-full rounded-md border border-default bg-elevated p-2 font-mono text-[11px] text-fg focus:border-accent-500 focus:outline-none" placeholder="-----BEGIN PRIVATE KEY-----" />
        </Field>
        <div className="sm:col-span-2">
          <Button type="submit" variant="primary" loading={set.isPending} disabled={!cert.trim() || !key.trim()} icon={<Upload className="size-4" />}>
            Install certificate
          </Button>
        </div>
      </form>
    </div>
  );
}

const trustSteps: { os: string; steps: string }[] = [
  { os: "Windows", steps: "Double-click staqio-ca.crt → Install Certificate → Local Machine → “Place all certificates in the following store” → Trusted Root Certification Authorities." },
  { os: "macOS", steps: "Open the file in Keychain Access (System keychain), then double-click the certificate → Trust → “Always Trust”." },
  { os: "Linux", steps: "sudo cp staqio-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates (Debian/Ubuntu) or sudo trust anchor staqio-ca.crt (Fedora/Arch)." },
  { os: "iOS / iPadOS", steps: "Open the file, install the profile under Settings → General → VPN & Device Management, then enable it under Settings → General → About → Certificate Trust Settings." },
  { os: "Android", steps: "Settings → Security → Encryption & credentials → Install a certificate → CA certificate." },
  { os: "Firefox", steps: "Uses its own store: Settings → Privacy & Security → Certificates → View Certificates → Authorities → Import (or set security.enterprise_roots.enabled to true)." },
];

export function DomainsCard() {
  const settings = useSettings();
  const tls = useTLSInfo();
  if (settings.isPending || tls.isPending) return <Card><CardHeader title="Domains & HTTPS" /><Spinner /></Card>;
  if (settings.isError || tls.isError) return <Card><CardHeader title="Domains & HTTPS" /><div className="p-5"><Alert tone="red">{(settings.error ?? tls.error)?.message}</Alert></div></Card>;
  const info = tls.data;
  const tlsAvailable = info.enabled && info.proxy.httpsPort > 0;
  return (
    <Card>
      <CardHeader title="Domains & HTTPS" description="The embedded reverse proxy opens every project under its own host name and issues certificates from a local certificate authority." />
      <div className="space-y-6 p-5">
        <ProxyStatus tls={info} />
        <BaseDomainForm baseDomain={settings.data.baseDomain} forceHttps={settings.data.forceHttps} tlsAvailable={tlsAvailable} />
        {info.enabled && info.ca && (
          <>
            <div className="border-t border-default pt-5">
              <h3 className="text-sm font-semibold">Local certificate authority</h3>
              <p className="mt-1 text-sm text-muted">
                Install the CA once on each device that should open projects over HTTPS without warnings. Only the public certificate leaves Staqio; the key stays in <Code>/config/ca</Code>.
              </p>
              <dl className="mt-3 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-[8rem_1fr]">
                <dt className="text-muted">Subject</dt>
                <dd>{info.ca.caSubject}</dd>
                <dt className="text-muted">SHA-256</dt>
                <dd className="break-all font-mono">{info.ca.caFingerprint}</dd>
                <dt className="text-muted">Valid until</dt>
                <dd>{formatDateTime(info.ca.caNotAfter)}</dd>
              </dl>
              <a href={api.tls.caUrl} download="staqio-ca.crt" className="mt-3 inline-flex h-9 items-center gap-2 rounded-md bg-accent-600 px-3.5 text-sm font-medium text-white shadow-sm hover:bg-accent-500">
                <Download className="size-4" aria-hidden /> Download staqio-ca.crt
              </a>
              <details className="mt-3 text-sm">
                <summary className="cursor-pointer text-muted hover:text-fg">How to trust the CA</summary>
                <ul className="mt-2 space-y-1.5 text-xs text-muted">
                  {trustSteps.map((t) => (
                    <li key={t.os}>
                      <span className="font-medium text-fg">{t.os}:</span> {t.steps}
                    </li>
                  ))}
                </ul>
              </details>
            </div>
            <div className="border-t border-default pt-5">
              <h3 className="text-sm font-semibold">Custom certificate</h3>
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
