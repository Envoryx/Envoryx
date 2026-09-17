import { KeyRound } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import { useAudit, useSettings } from "@/api/hooks";
import { Alert, Button, Card, CardHeader, Code, ErrorState, Field, Input, PageHeader, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

function PasswordForm() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    if (next !== confirm) {
      setMsg({ tone: "red", text: "New passwords do not match." });
      return;
    }
    setBusy(true);
    try {
      await api.auth.changePassword(current, next);
      setMsg({ tone: "green", text: "Password changed. Other sessions were signed out." });
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Request failed" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader title="Password" description="Changing the password signs out all other sessions." />
      <form onSubmit={submit} className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Field label="Current password" htmlFor="pw-current">
          <Input id="pw-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="New password" htmlFor="pw-next" hint="At least 10 characters">
            <Input id="pw-next" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} required minLength={10} />
          </Field>
          <Field label="Confirm new password" htmlFor="pw-confirm">
            <Input id="pw-confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
          </Field>
        </div>
        <Button type="submit" variant="primary" loading={busy} icon={<KeyRound className="size-4" />}>
          Change password
        </Button>
      </form>
    </Card>
  );
}

export function SettingsPage() {
  const s = useSettings();
  const audit = useAudit(50);

  return (
    <div className="space-y-6">
      <PageHeader title="Settings" description="Runtime configuration is provided through environment variables of the Staqio container." />
      {s.isPending ? (
        <Spinner />
      ) : s.isError ? (
        <ErrorState message={s.error.message} />
      ) : (
        <Card>
          <CardHeader title="Instance" />
          <dl className="grid gap-x-8 gap-y-3 p-5 text-sm sm:grid-cols-2">
            <Row label="Version" value={s.data.version} />
            <Row label="Schema version" value={String(s.data.schemaVersion)} />
            <Row label="Config directory" value={s.data.configDir} mono />
            <Row label="Projects directory" value={s.data.projectsDir} mono />
            <Row label="Host path (projects)" value={s.data.hostPath.overrides[s.data.projectsDir] ?? s.data.hostPath.detected[s.data.projectsDir] ?? "unresolved"} mono />
            <Row label="Host path (config)" value={s.data.hostPath.overrides[s.data.configDir] ?? s.data.hostPath.detected[s.data.configDir] ?? "unresolved"} mono />
            <Row label="Project port range" value={`${s.data.portRange.start}–${s.data.portRange.end}`} />
            <Row label="Container user (PUID:PGID)" value={`${s.data.puid}:${s.data.pgid}`} />
            <Row label="Docker host" value={s.data.dockerHost || "default socket"} mono />
            <Row label="Session timeouts" value={`idle ${s.data.session.idleTimeout} · absolute ${s.data.session.absoluteTimeout}`} />
            <Row label="Secure cookies" value={s.data.secureCookies ? "on" : "off (enable when served over HTTPS)"} />
          </dl>
          <p className="border-t border-default px-5 py-3 text-xs text-subtle">
            Change these via <Code>STAQIO_*</Code> environment variables; see DEPLOYMENT.md.
          </p>
        </Card>
      )}

      <PasswordForm />

      <Card>
        <CardHeader title="Audit log" description="Most recent security-relevant events. Secrets are never recorded." />
        {audit.isPending ? (
          <Spinner />
        ) : audit.isError ? (
          <ErrorState message={audit.error.message} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-subtle">
                <tr className="border-b border-default">
                  <th className="px-5 py-2 font-medium">Time</th>
                  <th className="px-3 py-2 font-medium">User</th>
                  <th className="px-3 py-2 font-medium">Action</th>
                  <th className="px-3 py-2 font-medium">Target</th>
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
