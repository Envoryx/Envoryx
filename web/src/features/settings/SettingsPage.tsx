import { KeyRound, RefreshCw, Save } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import type { TFunction } from "i18next";
import type { UpdateStatus } from "@/api/types";
import { useAudit, useDeployKey, useDiagnostics, useSettings, useUpdateSettings } from "@/api/hooks";
import { useSearchParams } from "react-router-dom";
import { clsx } from "clsx";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Badge, Button, Card, CardHeader, ErrorState, Field, Input, PageHeader, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";
import { PublicHostNotice } from "@/components/PublicHostNotice";
import { DomainsCard } from "./DomainsCard";
import { TokensCard } from "./TokensCard";
import { DBToolCard } from "./DBToolCard";
import { NotificationsCard } from "./NotificationsCard";
import { InstanceBackupsCard } from "./InstanceBackupsCard";
import { DiagnosticsTab } from "./DiagnosticsTab";

function PasswordForm() {
  const { t } = useTranslation();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    if (next !== confirm) {
      setMsg({ tone: "red", text: t("New passwords do not match.") });
      return;
    }
    setBusy(true);
    try {
      await api.auth.changePassword(current, next);
      setMsg({ tone: "green", text: t("Password changed. Other sessions were signed out.") });
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Request failed") });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader title={t("Password")} description={t("Changing the password signs out all other sessions.")} />
      <form onSubmit={submit} className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Field label={t("Current password")} htmlFor="pw-current">
          <Input id="pw-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("New password")} htmlFor="pw-next" hint={t("At least 10 characters")}>
            <Input id="pw-next" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} required minLength={10} />
          </Field>
          <Field label={t("Confirm new password")} htmlFor="pw-confirm">
            <Input id="pw-confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
          </Field>
        </div>
        <Button type="submit" variant="primary" loading={busy} icon={<KeyRound className="size-4" />}>
          {t("Change password")}
        </Button>
      </form>
    </Card>
  );
}

function PublicHostForm({ current, xdebugHost }: { current: string; xdebugHost: string }) {
  const { t } = useTranslation();
  const update = useUpdateSettings();
  const [host, setHost] = useState(current);
  const [xhost, setXhost] = useState(xdebugHost);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  useEffect(() => setHost(current), [current]);
  useEffect(() => setXhost(xdebugHost), [xdebugHost]);

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    update.mutate(
      { publicHost: host.trim(), xdebugClientHost: xhost.trim() },
      {
        onSuccess: () => setMsg({ tone: "green", text: t("Saved. Project links now use this host.") }),
        onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Saving failed") }),
      },
    );
  }

  return (
    <Card>
      <CardHeader
        title={t("Project links & developer machine")}
        description={t("Project ports are published on the Docker host. If Envoryx itself is reached under a different address (own container IP, reverse proxy), set the host that browsers should use for project links.")}
      />
      <form onSubmit={submit} className="space-y-4 p-5">
        <PublicHostNotice />
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Field label={t("Host for project links")} htmlFor="public-host" hint={t("Leave empty to use the browser address bar (currently {{host}}). Host name or IP only, no port.", { host: window.location.hostname })}>
          <Input id="public-host" value={host} onChange={(e) => setHost(e.target.value)} placeholder="192.168.1.10" spellCheck={false} />
        </Field>
        <Field label={t("Developer machine for Xdebug")} htmlFor="xdebug-host" hint={t("Fallback IP/host Xdebug connects back to when the request does not reveal it. Projects can override it.")}>
          <Input id="xdebug-host" value={xhost} onChange={(e) => setXhost(e.target.value)} placeholder="192.168.1.20" spellCheck={false} />
        </Field>
        <Button type="submit" variant="primary" loading={update.isPending} disabled={host.trim() === current && xhost.trim() === xdebugHost} icon={<Save className="size-4" />}>
          {t("Save")}
        </Button>
      </form>
    </Card>
  );
}

function SshCard({ keys, ssh }: { keys: string; ssh: { enabled: boolean; port: number; fingerprint: string } | undefined }) {
  const { t } = useTranslation();
  const update = useUpdateSettings();
  const [text, setText] = useState(keys);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  useEffect(() => setText(keys), [keys]);
  return (
    <Card>
      <CardHeader
        title={t("SSH access (IDE remote interpreter)")}
        description={t("Log in as <project-slug> (PHP container) or <project-slug>.node with an API token as password, or with one of the public keys below. Each session runs inside the project's container as the project owner; SFTP exposes /var/www/html and /home/envoryx.")}
      />
      <div className="space-y-4 p-5">
        {!ssh?.enabled ? (
          <Alert tone="amber">{t("Disabled (ENVORYX_SSH is empty).")}</Alert>
        ) : ssh.port === 0 ? (
          <Alert tone="amber">{t("The SSH port 2222 is not published on the host – add a port mapping 2222:2222 to the Envoryx container.")}</Alert>
        ) : (
          <dl className="grid gap-x-8 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
            <dt className="text-muted">{t("Port")}</dt>
            <dd className="font-mono text-xs">{ssh.port}</dd>
            <dt className="text-muted">{t("Host key")}</dt>
            <dd className="break-all font-mono text-xs">{ssh.fingerprint}</dd>
          </dl>
        )}
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Field label={t("Authorized public keys")} htmlFor="ssh-keys" hint={t("One key per line (authorized_keys format), e.g. the content of ~/.ssh/id_ed25519.pub. Lines starting with # are comments.")}>
          <textarea id="ssh-keys" value={text} onChange={(e) => setText(e.target.value)} rows={4} spellCheck={false} className="w-full rounded-md border border-default bg-elevated p-2 font-mono text-[11px] text-fg focus:border-accent-500 focus:outline-none" placeholder="ssh-ed25519 AAAA… you@laptop" />
        </Field>
        <Button
          variant="primary"
          loading={update.isPending}
          disabled={text.trim() === keys.trim()}
          icon={<Save className="size-4" />}
          onClick={() => {
            setMsg(null);
            update.mutate(
              { sshAuthorizedKeys: text },
              {
                onSuccess: () => setMsg({ tone: "green", text: t("Keys saved.") }),
                onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : t("Saving failed") }),
              },
            );
          }}
        >
          {t("Save keys")}
        </Button>
      </div>
    </Card>
  );
}

function DeployKeyCard() {
  const { t } = useTranslation();
  const key = useDeployKey();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  return (
    <Card>
      <CardHeader title={t("Git deploy key")} description={t("Public key used for SSH clones. Register it as a read-only deploy key in your repositories.")} />
      <div className="space-y-3 p-5">
        {key.isPending ? <Spinner /> : key.isError ? <Alert tone="red">{key.error.message}</Alert> : <code className="block select-all break-all rounded-md bg-muted p-3 font-mono text-[11px]">{key.data}</code>}
        {confirm ? (
          <Alert tone="amber" title={t("Regenerate the key?")}>
            {t("All repositories using the current key lose access until the new key is registered.")}
            <div className="mt-2 flex gap-2">
              <Button size="sm" variant="danger" loading={busy} onClick={async () => { setBusy(true); try { await api.git.regenerateDeployKey(); await qc.invalidateQueries({ queryKey: ["deploy-key"] }); } finally { setBusy(false); setConfirm(false); } }}>
                {t("Regenerate")}
              </Button>
              <Button size="sm" onClick={() => setConfirm(false)}>{t("Cancel")}</Button>
            </div>
          </Alert>
        ) : (
          <Button size="sm" onClick={() => setConfirm(true)} icon={<RefreshCw className="size-3.5" />}>
            {t("Regenerate key")}
          </Button>
        )}
      </div>
    </Card>
  );
}

function updateLabel(t: TFunction, version: string, u?: UpdateStatus): string {
  if (!u || !u.enabled) return version;
  if (!u.release) return `${version} · ${t("development build")}`;
  if (u.available && u.latest) return `${version} · ${t("{{latest}} available", { latest: u.latest })}`;
  if (u.latest) return `${version} · ${t("up to date")}`;
  if (u.error) return `${version} · ${t("update check failed")}`;
  return version;
}

const tabs = ["diagnostics", "general", "domains", "access", "notifications", "backups", "tools", "audit"] as const;
type Tab = (typeof tabs)[number];
const tabLabel: Record<Tab, string> = {
  diagnostics: "Diagnostics",
  general: "General",
  domains: "Domains & HTTPS",
  access: "Access",
  notifications: "Notifications",
  backups: "Backups",
  tools: "Tools",
  audit: "Audit log",
};

function isTab(v: string | null): v is Tab {
  return tabs.includes(v as Tab);
}

function InstanceCard() {
  const { t } = useTranslation();
  const s = useSettings();
  if (s.isPending) return <Spinner />;
  if (s.isError) return <ErrorState message={s.error.message} />;
  return (
    <Card>
      <CardHeader title={t("Instance")} description={t("Runtime configuration is provided through environment variables of the Envoryx container.")} />
      <dl className="grid gap-x-8 gap-y-3 p-5 text-sm sm:grid-cols-2">
        <Row label={t("Version")} value={updateLabel(t, s.data.version, s.data.update)} />
        <Row label={t("Schema version")} value={String(s.data.schemaVersion)} />
        <Row label={t("Config directory")} value={s.data.configDir} mono />
        <Row label={t("Projects directory")} value={s.data.projectsDir} mono />
        <Row label={t("Host path (projects)")} value={s.data.hostPath.overrides[s.data.projectsDir] ?? s.data.hostPath.detected[s.data.projectsDir] ?? t("unresolved")} mono />
        <Row label={t("Host path (config)")} value={s.data.hostPath.overrides[s.data.configDir] ?? s.data.hostPath.detected[s.data.configDir] ?? t("unresolved")} mono />
        <Row label={t("Project port range")} value={`${s.data.portRange.start}–${s.data.portRange.end}`} />
        <Row label={t("Container user (PUID:PGID)")} value={`${s.data.puid}:${s.data.pgid}`} />
        <Row label={t("Docker host")} value={s.data.dockerHost || t("default socket")} mono />
        <Row label={t("Session timeouts")} value={t("idle {{idle}} · absolute {{absolute}}", { idle: s.data.session.idleTimeout, absolute: s.data.session.absoluteTimeout })} />
        <Row label={t("Secure cookies")} value={s.data.secureCookies ? t("on") : t("off (enable when served over HTTPS)")} />
      </dl>
      <p className="border-t border-default px-5 py-3 text-xs text-subtle">{t("Change these via ENVORYX_* environment variables; see DEPLOYMENT.md.")}</p>
    </Card>
  );
}

function AuditCard() {
  const { t } = useTranslation();
  const audit = useAudit(50);
  return (
    <Card>
      <CardHeader title={t("Audit log")} description={t("Most recent security-relevant events. Secrets are never recorded.")} />
      {audit.isPending ? (
        <Spinner />
      ) : audit.isError ? (
        <ErrorState message={audit.error.message} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-subtle">
              <tr className="border-b border-default">
                <th className="px-5 py-2 font-medium">{t("Time")}</th>
                <th className="px-3 py-2 font-medium">{t("User")}</th>
                <th className="px-3 py-2 font-medium">{t("Action")}</th>
                <th className="px-3 py-2 font-medium">{t("Target")}</th>
                <th className="px-3 py-2 font-medium">IP</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[var(--border)]">
              {audit.data.entries.map((e) => (
                <tr key={e.id}>
                  <td className="whitespace-nowrap px-5 py-2 text-xs text-muted">{formatDateTime(e.createdAt)}</td>
                  <td className="px-3 py-2 text-xs">{e.username || "—"}</td>
                  <td className="px-3 py-2 font-mono text-xs">{e.action}</td>
                  <td className="px-3 py-2 text-xs text-muted">
                    {e.details && typeof e.details["name"] === "string" ? String(e.details["name"]) : e.targetType ? `${e.targetType} ${e.targetId.slice(0, 8)}` : "—"}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-subtle">{e.ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

export function SettingsPage() {
  const { t } = useTranslation();
  const s = useSettings();
  const diagnostics = useDiagnostics();
  const [params, setParams] = useSearchParams();
  const requested = params.get("tab");
  const tab: Tab = isTab(requested) ? requested : "diagnostics";
  const setTab = (next: Tab) => setParams(next === "diagnostics" ? {} : { tab: next }, { replace: true });
  const attention = diagnostics.data ? diagnostics.data.summary.warning + diagnostics.data.summary.error : 0;

  return (
    <div className="space-y-6">
      <PageHeader title={t("Settings")} description={t("Instance configuration, access and health in one place.")} />

      <div className="flex flex-wrap gap-1 border-b border-default" role="tablist">
        {tabs.map((name) => (
          <button
            key={name}
            role="tab"
            aria-selected={tab === name}
            onClick={() => setTab(name)}
            className={clsx("-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium", tab === name ? "border-accent-500 text-fg" : "border-transparent text-muted hover:text-fg")}
          >
            {t(tabLabel[name])}
            {name === "diagnostics" && diagnostics.data && (
              <Badge tone={attention > 0 ? (diagnostics.data.summary.error > 0 ? "red" : "amber") : "green"}>{attention > 0 ? attention : "✓"}</Badge>
            )}
          </button>
        ))}
      </div>

      {tab === "diagnostics" && <DiagnosticsTab onSwitchTab={(next) => isTab(next) && setTab(next)} />}
      {tab === "general" && (
        <>
          <InstanceCard />
          {s.data && <PublicHostForm current={s.data.publicHost} xdebugHost={s.data.xdebugClientHost ?? ""} />}
          <PasswordForm />
        </>
      )}
      {tab === "domains" && <DomainsCard />}
      {tab === "access" && (
        <>
          <TokensCard />
          {s.data && <SshCard keys={s.data.sshAuthorizedKeys ?? ""} ssh={s.data.ssh} />}
          <DeployKeyCard />
        </>
      )}
      {tab === "notifications" && <NotificationsCard />}
      {tab === "backups" && <InstanceBackupsCard />}
      {tab === "tools" && <DBToolCard />}
      {tab === "audit" && <AuditCard />}
    </div>
  );
}

function Row({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-muted">{label}</dt>
      <dd className={mono ? "truncate font-mono text-xs" : ""}>{value}</dd>
    </div>
  );
}
