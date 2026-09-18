import { Save, Send } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import type { NotifyConfig } from "@/api/types";
import { Alert, Button, Card, CardHeader, Checkbox, Code, Field, Input, Select, Spinner } from "@/components/ui";
import { formatDateTime } from "@/lib/format";

const key = ["notifications"] as const;

const hints: Record<string, string> = {
  webhook: "Staqio POSTs JSON {kind, level, title, message, project, time} to the URL.",
  ntfy: "Topic URL such as https://ntfy.sh/staqio-abc123 or your own ntfy server. Token only for protected topics.",
  discord: "Server settings → Integrations → Webhooks → New webhook → copy URL.",
  slack: "Slack app → Incoming Webhooks → Add new webhook → copy URL.",
  telegram: "Create a bot with @BotFather (token), start a chat with it, read your chat id via @userinfobot.",
  email: "SMTP credentials of your mail provider. STARTTLS on 587 or TLS on 465; authentication requires an encrypted connection.",
};

export function NotificationsCard() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: key, queryFn: api.notifications.get });
  const [form, setForm] = useState<NotifyConfig | null>(null);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  useEffect(() => {
    if (q.data && !form) {
      const c = q.data.status.config;
      setForm({ ...c, provider: c.provider || "ntfy", kinds: c.kinds ?? q.data.kinds.filter((k) => k.default).map((k) => k.kind), smtpSecurity: c.smtpSecurity || "starttls" });
    }
  }, [q.data, form]);

  const save = useMutation({
    mutationFn: (cfg: NotifyConfig) => api.notifications.set(cfg),
    onSuccess: (data) => {
      qc.setQueryData(key, data);
      setForm((f) => (f ? { ...f, token: "", smtpPassword: "" } : f));
      setMsg({ tone: "green", text: "Notification settings saved." });
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Saving failed" }),
  });
  const test = useMutation({
    mutationFn: (cfg: NotifyConfig) => api.notifications.test(cfg),
    onSuccess: () => setMsg({ tone: "green", text: "Test notification delivered." }),
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Test failed" }),
  });

  if (q.isPending || !form) return <Card><CardHeader title="Notifications" /><Spinner /></Card>;
  if (q.isError) return <Card><CardHeader title="Notifications" /><div className="p-5"><Alert tone="red">{q.error.message}</Alert></div></Card>;
  const set = (patch: Partial<NotifyConfig>) => setForm((f) => (f ? { ...f, ...patch } : f));
  const st = q.data.status;
  const kinds = form.kinds ?? [];
  const toggleKind = (k: string, on: boolean) => set({ kinds: on ? [...kinds, k] : kinds.filter((x) => x !== k) });

  function submit(e: FormEvent) {
    e.preventDefault();
    setMsg(null);
    save.mutate(form!);
  }

  return (
    <Card>
      <CardHeader
        title="Notifications"
        description="Get told when something needs you: failed certificate renewals, projects that stopped unexpectedly, failed backups."
        actions={
          st.lastSent ? <span className="text-xs text-subtle">last sent {formatDateTime(st.lastSent)}</span> : null
        }
      />
      <form onSubmit={submit} className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {st.lastError && <Alert tone="amber" title="Last delivery failed">{st.lastError}</Alert>}
        <Checkbox label="Enable notifications" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Channel" htmlFor="notify-provider" hint={hints[form.provider]}>
            <Select id="notify-provider" value={form.provider} onChange={(e) => set({ provider: e.target.value })}>
              {Object.entries(q.data.providers).map(([k, v]) => (
                <option key={k} value={k}>{v}</option>
              ))}
            </Select>
          </Field>
          {["webhook", "ntfy", "discord", "slack"].includes(form.provider) && (
            <Field label="URL" htmlFor="notify-url">
              <Input id="notify-url" value={form.url ?? ""} onChange={(e) => set({ url: e.target.value })} placeholder="https://…" spellCheck={false} />
            </Field>
          )}
          {form.provider === "ntfy" && (
            <Field label="Access token (optional)" htmlFor="notify-token" hint={st.hasToken ? "Leave empty to keep the stored token." : undefined}>
              <Input id="notify-token" type="password" autoComplete="off" value={form.token ?? ""} onChange={(e) => set({ token: e.target.value })} placeholder={st.hasToken ? "••••••••" : ""} />
            </Field>
          )}
          {form.provider === "telegram" && (
            <>
              <Field label="Bot token" htmlFor="notify-token" hint={st.hasToken ? "Leave empty to keep the stored token." : undefined}>
                <Input id="notify-token" type="password" autoComplete="off" value={form.token ?? ""} onChange={(e) => set({ token: e.target.value })} placeholder={st.hasToken ? "••••••••" : "123456:ABC…"} />
              </Field>
              <Field label="Chat id" htmlFor="notify-chat">
                <Input id="notify-chat" value={form.chatId ?? ""} onChange={(e) => set({ chatId: e.target.value })} placeholder="123456789" />
              </Field>
            </>
          )}
          {form.provider === "email" && (
            <>
              <Field label="SMTP host" htmlFor="smtp-host">
                <Input id="smtp-host" value={form.smtpHost ?? ""} onChange={(e) => set({ smtpHost: e.target.value })} placeholder="smtp.example.com" spellCheck={false} />
              </Field>
              <div className="grid grid-cols-2 gap-4">
                <Field label="Port" htmlFor="smtp-port" hint="0 = default for the security mode">
                  <Input id="smtp-port" type="number" min={0} max={65535} value={form.smtpPort ?? 0} onChange={(e) => set({ smtpPort: Number(e.target.value) })} />
                </Field>
                <Field label="Security" htmlFor="smtp-sec">
                  <Select id="smtp-sec" value={form.smtpSecurity ?? "starttls"} onChange={(e) => set({ smtpSecurity: e.target.value })}>
                    <option value="starttls">STARTTLS (587)</option>
                    <option value="tls">TLS (465)</option>
                    <option value="none">None</option>
                  </Select>
                </Field>
              </div>
              <Field label="Username" htmlFor="smtp-user">
                <Input id="smtp-user" value={form.smtpUser ?? ""} onChange={(e) => set({ smtpUser: e.target.value })} autoComplete="off" />
              </Field>
              <Field label="Password" htmlFor="smtp-pass" hint={st.hasSmtpPassword ? "Leave empty to keep the stored password." : undefined}>
                <Input id="smtp-pass" type="password" autoComplete="off" value={form.smtpPassword ?? ""} onChange={(e) => set({ smtpPassword: e.target.value })} placeholder={st.hasSmtpPassword ? "••••••••" : ""} />
              </Field>
              <Field label="From" htmlFor="smtp-from">
                <Input id="smtp-from" type="email" value={form.from ?? ""} onChange={(e) => set({ from: e.target.value })} placeholder="staqio@example.com" />
              </Field>
              <Field label="To" htmlFor="smtp-to" hint="Comma-separated for several recipients">
                <Input id="smtp-to" value={form.to ?? ""} onChange={(e) => set({ to: e.target.value })} placeholder="you@example.com" />
              </Field>
            </>
          )}
        </div>
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">Events</legend>
          {q.data.kinds.map((k) => (
            <Checkbox key={k.kind} label={k.description} description={k.kind} checked={kinds.includes(k.kind)} onChange={(e) => toggleKind(k.kind, e.target.checked)} />
          ))}
        </fieldset>
        <p className="text-xs text-subtle">
          Repeated events are throttled (unhealthy project: once per 6 h until it recovers, failed renewal: once per day). Secrets are stored in <Code>/config/notify.json</Code> and never returned.
        </p>
        <div className="flex gap-2">
          <Button type="submit" variant="primary" loading={save.isPending} icon={<Save className="size-4" />}>
            Save
          </Button>
          <Button type="button" onClick={() => { setMsg(null); test.mutate(form!); }} loading={test.isPending} icon={<Send className="size-4" />}>
            Send test
          </Button>
        </div>
      </form>
    </Card>
  );
}
