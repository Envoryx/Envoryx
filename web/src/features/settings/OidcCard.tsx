import { KeyRound, Save, Wifi } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { OIDCSettings } from "@/api/types";
import { Alert, Button, Card, CardHeader, Checkbox, Code, ErrorState, Field, Input, Select, Spinner, type Tone } from "@/components/ui";
import { errorText } from "@/lib/errors";

const list = (s: string) => s.split(/[\n,]/).map((v) => v.trim()).filter(Boolean);
const join = (l?: string[]) => (l ?? []).join(", ");

interface Form {
  enabled: boolean;
  name: string;
  issuer: string;
  clientId: string;
  clientSecret: string;
  usernameClaim: string;
  groupsClaim: string;
  adminGroups: string;
  developerGroups: string;
  viewerGroups: string;
  defaultRole: string;
  autoCreate: boolean;
}

function toForm(c: OIDCSettings): Form {
  return {
    enabled: c.enabled,
    name: c.name ?? "",
    issuer: c.issuer ?? "",
    clientId: c.clientId ?? "",
    clientSecret: "",
    usernameClaim: c.usernameClaim ?? "",
    groupsClaim: c.groupsClaim ?? "",
    adminGroups: join(c.adminGroups),
    developerGroups: join(c.developerGroups),
    viewerGroups: join(c.viewerGroups),
    defaultRole: c.defaultRole || "deny",
    autoCreate: c.autoCreate,
  };
}

function fromForm(f: Form): OIDCSettings {
  return {
    enabled: f.enabled,
    name: f.name.trim(),
    issuer: f.issuer.trim(),
    clientId: f.clientId.trim(),
    clientSecret: f.clientSecret,
    usernameClaim: f.usernameClaim.trim(),
    groupsClaim: f.groupsClaim.trim(),
    adminGroups: list(f.adminGroups),
    developerGroups: list(f.developerGroups),
    viewerGroups: list(f.viewerGroups),
    defaultRole: f.defaultRole,
    autoCreate: f.autoCreate,
  };
}

/** Single sign-on through an OpenID Connect provider. Admins only. */
export function OidcCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["oidc"], queryFn: async () => (await api.oidc.get()).oidc });
  const [form, setForm] = useState<Form | null>(null);
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  useEffect(() => {
    if (q.data) setForm(toForm(q.data));
  }, [q.data]);
  const save = useMutation({
    mutationFn: (f: Form) => api.oidc.set(fromForm(f)),
    onSuccess: (r) => {
      qc.setQueryData(["oidc"], r.oidc);
      setMsg({ tone: "green", text: t("Single sign-on saved.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  const test = useMutation({
    mutationFn: (f: Form) => api.oidc.test(fromForm(f)),
    onSuccess: (r) => setMsg(r.ok ? { tone: "green", text: t("The provider answers.") } : { tone: "red", text: t("The provider does not answer: {{error}}", { error: r.error ?? "" }) }),
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t) }),
  });
  if (q.isPending || !form) return q.isError ? <ErrorState message={errorText(q.error, t)} /> : <Spinner />;
  const set = (patch: Partial<Form>) => setForm({ ...form, ...patch });
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <KeyRound className="size-4 text-accent-500" aria-hidden /> {t("Single sign-on (OpenID Connect)")}
          </span>
        }
        description={t("Sign in through Authentik, Keycloak, Authelia, Google or any other OpenID Connect provider. Users keep their Envoryx roles; groups from the provider can set them.")}
        actions={
          <div className="flex gap-2">
            <Button icon={<Wifi className="size-4" />} loading={test.isPending} disabled={!form.issuer.trim()} onClick={() => test.mutate(form)}>
              {t("Test")}
            </Button>
            <Button variant="primary" icon={<Save className="size-4" />} loading={save.isPending} onClick={() => save.mutate(form)}>
              {t("Save")}
            </Button>
          </div>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Checkbox label={t("Offer single sign-on on the login page")} checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
        <p className="text-xs text-muted">
          {t("Register Envoryx at the provider as a confidential client with this redirect URL:")} <Code>{q.data?.redirectUrl}</Code>
        </p>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("Issuer URL")} htmlFor="oidc-issuer" hint={t("e.g. https://auth.example.com/application/o/envoryx/")}>
            <Input id="oidc-issuer" value={form.issuer} onChange={(e) => set({ issuer: e.target.value })} />
          </Field>
          <Field label={t("Button label")} htmlFor="oidc-name" hint={t("Shown as “Sign in with …”")}>
            <Input id="oidc-name" value={form.name} placeholder="Authentik" onChange={(e) => set({ name: e.target.value })} />
          </Field>
          <Field label={t("Client ID")} htmlFor="oidc-client">
            <Input id="oidc-client" value={form.clientId} onChange={(e) => set({ clientId: e.target.value })} />
          </Field>
          <Field label={t("Client secret")} htmlFor="oidc-secret" hint={q.data?.hasSecret ? t("Stored; leave empty to keep it.") : undefined}>
            <Input id="oidc-secret" type="password" autoComplete="off" value={form.clientSecret} onChange={(e) => set({ clientSecret: e.target.value })} />
          </Field>
          <Field label={t("Username claim")} htmlFor="oidc-username" hint={t("Empty: {{n}}", { n: "preferred_username" })}>
            <Input id="oidc-username" value={form.usernameClaim} onChange={(e) => set({ usernameClaim: e.target.value })} />
          </Field>
          <Field label={t("Groups claim")} htmlFor="oidc-groups" hint={t("Empty: {{n}}", { n: "groups" })}>
            <Input id="oidc-groups" value={form.groupsClaim} onChange={(e) => set({ groupsClaim: e.target.value })} />
          </Field>
        </div>
        <fieldset className="space-y-3 rounded-md border border-default p-3">
          <legend className="px-1 text-sm font-medium text-fg">{t("Roles from groups")}</legend>
          <p className="text-xs text-muted">{t("Comma-separated group names. With any of them set, every sign-in sets the user's role: the first match from admin down wins. Without them the role stays what an admin set in Envoryx.")}</p>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label={t("Admin groups")} htmlFor="oidc-admins">
              <Input id="oidc-admins" value={form.adminGroups} onChange={(e) => set({ adminGroups: e.target.value })} />
            </Field>
            <Field label={t("Developer groups")} htmlFor="oidc-devs">
              <Input id="oidc-devs" value={form.developerGroups} onChange={(e) => set({ developerGroups: e.target.value })} />
            </Field>
            <Field label={t("Viewer groups")} htmlFor="oidc-viewers">
              <Input id="oidc-viewers" value={form.viewerGroups} onChange={(e) => set({ viewerGroups: e.target.value })} />
            </Field>
          </div>
          <Field label={t("Users in none of these groups")} htmlFor="oidc-default">
            <Select id="oidc-default" value={form.defaultRole} onChange={(e) => set({ defaultRole: e.target.value })}>
              <option value="deny">{t("may not sign in")}</option>
              <option value="none">{t("sign in without access to any project")}</option>
              <option value="viewer">{t("sign in as viewers")}</option>
              <option value="developer">{t("sign in as developers")}</option>
            </Select>
          </Field>
        </fieldset>
        <Checkbox
          label={t("Create users at their first sign-in")}
          description={t("Off: only users an admin invited get in; the invitation links the account. An existing Envoryx account is never taken over by name.")}
          checked={form.autoCreate}
          onChange={(e) => set({ autoCreate: e.target.checked })}
        />
      </div>
    </Card>
  );
}
