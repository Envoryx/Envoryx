import { Check, Copy, KeyRound, Pencil, ShieldCheck, Smartphone, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { useId, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import type { PasskeyInfo, TwoFactorStatus } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Dialog, ErrorState, Field, Input, Spinner, type Tone } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { createPasskey, passkeyCancelled, passkeysAvailable } from "@/lib/webauthn";

const statusKey = ["two-factor"] as const;

/** A wrong password confirming a change: the session itself is fine. */
function passwordError(err: unknown, t: TFunction, fallback: string): string {
  if (err instanceof ApiError && err.code === "wrong_password") return t("The password is wrong.");
  return errorText(err, t, fallback);
}

/** Asks for the password before a change to the second factors, then runs it. */
function PasswordDialog({ title, description, open, onClose, onConfirm, danger = false }: { title: string; description?: string; open: boolean; onClose: () => void; onConfirm: (password: string) => Promise<void>; danger?: boolean }) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // One dialog per change sits in the page: each needs its own field id.
  const fieldId = useId();
  const close = () => {
    setPassword("");
    setError(null);
    onClose();
  };
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onConfirm(password);
      setPassword("");
    } catch (err) {
      if (!passkeyCancelled(err)) setError(passwordError(err, t, t("Saving failed")));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog open={open} onClose={close} title={title} description={description ?? t("Confirm with your password.")}>
      <form onSubmit={submit} className="space-y-3" noValidate>
        {error && <Alert tone="red">{error}</Alert>}
        <Field label={t("Password")} htmlFor={fieldId}>
          <Input id={fieldId} type="password" autoComplete="current-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={close}>
            {t("Cancel")}
          </Button>
          <Button type="submit" variant={danger ? "danger" : "primary"} loading={busy} disabled={!password}>
            {t("Continue")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Recovery codes, shown once after they were made. */
export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const text = codes.join("\n");
  return (
    <Alert tone="amber" title={t("Your recovery codes")}>
      <p className="mb-2 text-xs">{t("Keep them somewhere safe, apart from your devices: each code signs you in once when you have no authenticator at hand. They are shown only now.")}</p>
      <pre className="mb-2 grid select-all grid-cols-2 gap-x-6 rounded-md bg-muted p-3 font-mono text-sm text-fg">
        {codes.map((c) => (
          <span key={c}>{c}</span>
        ))}
      </pre>
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          icon={copied ? <Check className="size-3.5 text-emerald-500" /> : <Copy className="size-3.5" />}
          onClick={async () => {
            setCopied(await copyText(text));
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          {t("Copy")}
        </Button>
        <Button size="sm" onClick={() => downloadCodes(text)}>
          {t("Download")}
        </Button>
        <Button size="sm" variant="primary" onClick={onDone}>
          {t("I saved them")}
        </Button>
      </div>
    </Alert>
  );
}

function downloadCodes(text: string) {
  const url = URL.createObjectURL(new Blob([text + "\n"], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = `envoryx-recovery-codes-${window.location.hostname}.txt`;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * The signed-in user's second factors: an authenticator app (TOTP), passkeys and the
 * recovery codes. onChanged runs after a factor was added (the setup page goes on then).
 */
export function TwoFactorCard({ onChanged }: { onChanged?: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const status = useQuery({ queryKey: statusKey, queryFn: api.twoFactor.status });
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [ask, setAsk] = useState<null | "totp" | "totp-remove" | "passkey" | "codes" | { remove: PasskeyInfo }>(null);
  const [totp, setTotp] = useState<{ secret: string; uri: string; qr: string } | null>(null);
  const [totpCode, setTotpCode] = useState("");
  const [passkeyName, setPasskeyName] = useState("");
  const [renaming, setRenaming] = useState<{ id: string; name: string } | null>(null);
  // The options of a passkey to create, fetched after the password: the browser's dialog
  // then opens on the next click (Safari wants it right on a click).
  const [creating, setCreating] = useState<{ id: string; publicKey: PublicKeyCredentialCreationOptionsJSON } | null>(null);
  const [createBusy, setCreateBusy] = useState(false);

  // A factor was added or removed. New recovery codes are shown first; onChanged waits
  // until they are put away (the setup page leaves when it runs).
  const changed = (recovery?: string[] | null) => {
    void qc.invalidateQueries({ queryKey: statusKey });
    if (recovery && recovery.length > 0) setCodes(recovery);
    else onChanged?.();
  };

  async function createNow() {
    if (!creating) return;
    setCreateBusy(true);
    try {
      const credential = await createPasskey(creating.publicKey);
      const r = await api.twoFactor.passkeyAdd(creating.id, passkeyName.trim(), credential);
      setPasskeyName("");
      setMsg({ tone: "green", text: t("The passkey {{name}} is added.", { name: r.passkey.name }) });
      changed(r.recoveryCodes);
    } catch (err) {
      if (!passkeyCancelled(err)) setMsg({ tone: "red", text: errorText(err, t, t("The passkey could not be added.")) });
    } finally {
      setCreating(null);
      setCreateBusy(false);
    }
  }

  const confirmTotp = useMutation({
    mutationFn: () => api.twoFactor.totpConfirm(totpCode.replace(/\s/g, "")),
    onSuccess: (r) => {
      setTotp(null);
      setTotpCode("");
      setMsg({ tone: "green", text: t("The authenticator app is set up.") });
      changed(r.recoveryCodes);
    },
    onError: (err) => setMsg({ tone: "red", text: err instanceof ApiError && err.code === "invalid_code" ? t("The code is wrong or was used already.") : errorText(err, t, t("Saving failed")) }),
  });
  const rename = useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) => api.twoFactor.passkeyRename(id, name),
    onSuccess: () => {
      setRenaming(null);
      changed();
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });

  if (status.isPending) return <Spinner />;
  if (status.isError) return <ErrorState message={errorText(status.error, t)} />;
  const st: TwoFactorStatus = status.data;
  const canPasskey = passkeysAvailable();
  const host = window.location.hostname;
  const any = st.totp || st.passkeys.length > 0;

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <ShieldCheck className="size-4 text-accent-500" aria-hidden /> {t("Two-factor authentication")}
            {any ? <Badge tone="green">{t("on")}</Badge> : <Badge>{t("off")}</Badge>}
            {st.required && <Badge tone="amber">{t("required")}</Badge>}
          </span>
        }
        description={t("A second factor protects the sign-in with your password: an authenticator app, or a passkey on your device or security key. A passkey also signs in on its own, without the password.")}
      />
      <div className="space-y-5 p-5">
        {!st.available ? (
          <Alert tone="blue">{t("Your account signs in through single sign-on. Set up two-factor authentication with your identity provider.")}</Alert>
        ) : (
          <>
            {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
            {codes && (
              <RecoveryCodes
                codes={codes}
                onDone={() => {
                  setCodes(null);
                  onChanged?.();
                }}
              />
            )}

            <section className="space-y-2">
              <div className="flex flex-wrap items-center gap-3">
                <Smartphone className="size-4 text-muted" aria-hidden />
                <h3 className="text-sm font-medium text-fg">{t("Authenticator app")}</h3>
                {st.totp && <Badge tone="green">{t("set up")}</Badge>}
                <span className="ml-auto">
                  {st.totp ? (
                    <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={() => setAsk("totp-remove")}>
                      {t("Remove")}
                    </Button>
                  ) : (
                    !totp && (
                      <Button size="sm" onClick={() => setAsk("totp")}>
                        {t("Set up")}
                      </Button>
                    )
                  )}
                </span>
              </div>
              <p className="text-xs text-muted">{t("Codes that change every 30 seconds, from an app such as Aegis, Google Authenticator, 1Password or Bitwarden. Works wherever Envoryx is opened.")}</p>
              {totp && (
                <div className="flex flex-wrap items-start gap-4 rounded-md border border-default p-3">
                  <div className="size-40 shrink-0 rounded-md bg-white p-1" role="img" aria-label={t("QR code for the authenticator app")} dangerouslySetInnerHTML={{ __html: totp.qr }} />
                  <form
                    className="min-w-0 flex-1 space-y-3"
                    onSubmit={(e) => {
                      e.preventDefault();
                      confirmTotp.mutate();
                    }}
                  >
                    <p className="text-xs text-muted">{t("Scan the code with the app, or enter the key by hand. Then type the code the app shows.")}</p>
                    <code className="block select-all break-all rounded-md bg-muted p-2 font-mono text-xs text-fg">{totp.secret}</code>
                    <Field label={t("Code")} htmlFor="totp-code">
                      <Input id="totp-code" autoComplete="one-time-code" inputMode="numeric" placeholder="123456" value={totpCode} onChange={(e) => setTotpCode(e.target.value)} className="font-mono tracking-widest" />
                    </Field>
                    <div className="flex gap-2">
                      <Button type="submit" variant="primary" loading={confirmTotp.isPending} disabled={!totpCode.trim()}>
                        {t("Verify")}
                      </Button>
                      <Button type="button" variant="ghost" onClick={() => setTotp(null)}>
                        {t("Cancel")}
                      </Button>
                    </div>
                  </form>
                </div>
              )}
            </section>

            <section className="space-y-2 border-t border-default pt-4">
              <div className="flex flex-wrap items-center gap-3">
                <KeyRound className="size-4 text-muted" aria-hidden />
                <h3 className="text-sm font-medium text-fg">{t("Passkeys")}</h3>
                <span className="ml-auto">
                  <Button size="sm" disabled={!canPasskey} title={canPasskey ? undefined : t("Passkeys work only when Envoryx is opened over https with a host name.")} onClick={() => setAsk("passkey")}>
                    {t("Add passkey")}
                  </Button>
                </span>
              </div>
              <p className="text-xs text-muted">
                {canPasskey
                  ? t("A passkey belongs to the address it was created at, here {{host}}. Opened under another address, Envoryx asks for the app code instead.", { host })
                  : t("Passkeys work only when Envoryx is opened over https with a host name.")}
              </p>
              {creating && (
                <div className="flex flex-wrap items-center gap-2 rounded-md border border-default p-3">
                  <span className="text-xs text-muted">{t("Your browser asks for the passkey next.")}</span>
                  <Button size="sm" variant="primary" icon={<KeyRound className="size-3.5" />} loading={createBusy} autoFocus onClick={() => void createNow()}>
                    {t("Create passkey")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setCreating(null)}>
                    {t("Cancel")}
                  </Button>
                </div>
              )}
              {canPasskey && !creating && (
                <Field label={t("Name of the new passkey")} htmlFor="passkey-name">
                  <Input id="passkey-name" placeholder={t("e.g. Laptop or YubiKey")} value={passkeyName} onChange={(e) => setPasskeyName(e.target.value)} maxLength={64} />
                </Field>
              )}
              {st.passkeys.length > 0 && (
                <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
                  {st.passkeys.map((k) => (
                    <li key={k.id} className="flex flex-wrap items-center gap-3 px-3 py-2 text-sm">
                      {renaming?.id === k.id ? (
                        <form
                          className="flex flex-1 items-center gap-2"
                          onSubmit={(e) => {
                            e.preventDefault();
                            rename.mutate(renaming);
                          }}
                        >
                          <Input aria-label={t("Name")} value={renaming.name} autoFocus maxLength={64} onChange={(e) => setRenaming({ id: k.id, name: e.target.value })} />
                          <Button size="sm" type="submit" variant="primary" loading={rename.isPending} disabled={!renaming.name.trim()}>
                            {t("Save")}
                          </Button>
                          <Button size="sm" type="button" variant="ghost" onClick={() => setRenaming(null)}>
                            {t("Cancel")}
                          </Button>
                        </form>
                      ) : (
                        <>
                          <span className="font-medium text-fg">{k.name}</span>
                          {k.rpId !== host && <Badge tone="amber">{k.rpId}</Badge>}
                          <span className="text-xs text-subtle">
                            {k.lastUsedAt ? t("last used {{when}}", { when: formatDateTime(k.lastUsedAt) }) : t("added {{when}}", { when: formatDateTime(k.createdAt) })}
                          </span>
                          <span className="ml-auto flex gap-1">
                            <Button size="sm" variant="ghost" icon={<Pencil className="size-3.5" />} aria-label={t("Rename {{name}}", { name: k.name })} onClick={() => setRenaming({ id: k.id, name: k.name })} />
                            <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={t("Remove {{name}}", { name: k.name })} onClick={() => setAsk({ remove: k })} />
                          </span>
                        </>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>

            {any && (
              <section className="space-y-2 border-t border-default pt-4">
                <div className="flex flex-wrap items-center gap-3">
                  <h3 className="text-sm font-medium text-fg">{t("Recovery codes")}</h3>
                  <Badge tone={st.recoveryCodes > 2 ? "gray" : "amber"}>{t("{{count}} left", { count: st.recoveryCodes })}</Badge>
                  <span className="ml-auto">
                    <Button size="sm" onClick={() => setAsk("codes")}>
                      {t("New codes")}
                    </Button>
                  </span>
                </div>
                <p className="text-xs text-muted">{t("Each code signs you in once when you have neither the app nor a passkey at hand. New codes replace the old ones.")}</p>
              </section>
            )}
          </>
        )}
      </div>

      <PasswordDialog
        open={ask === "totp"}
        title={t("Set up an authenticator app")}
        onClose={() => setAsk(null)}
        onConfirm={async (password) => {
          setTotp(await api.twoFactor.totpSetup(password));
          setMsg(null);
          setAsk(null);
        }}
      />
      <PasswordDialog
        open={ask === "totp-remove"}
        danger
        title={t("Remove the authenticator app")}
        onClose={() => setAsk(null)}
        onConfirm={async (password) => {
          await api.twoFactor.totpRemove(password);
          setAsk(null);
          setMsg({ tone: "green", text: t("The authenticator app is removed.") });
          changed();
        }}
      />
      <PasswordDialog
        open={ask === "passkey"}
        title={t("Add passkey")}
        description={t("Confirm with your password; then your browser asks for the passkey.")}
        onClose={() => setAsk(null)}
        onConfirm={async (password) => {
          setCreating(await api.twoFactor.passkeyOptions(password));
          setMsg(null);
          setAsk(null);
        }}
      />
      <PasswordDialog
        open={typeof ask === "object" && ask !== null}
        danger
        title={typeof ask === "object" && ask !== null ? t("Remove {{name}}", { name: ask.remove.name }) : ""}
        onClose={() => setAsk(null)}
        onConfirm={async (password) => {
          if (typeof ask !== "object" || ask === null) return;
          await api.twoFactor.passkeyRemove(ask.remove.id, password);
          setAsk(null);
          setMsg({ tone: "green", text: t("The passkey is removed.") });
          changed();
        }}
      />
      <PasswordDialog
        open={ask === "codes"}
        title={t("New recovery codes")}
        description={t("The old codes stop working. Confirm with your password.")}
        onClose={() => setAsk(null)}
        onConfirm={async (password) => {
          const r = await api.twoFactor.recoveryCodes(password);
          setAsk(null);
          setMsg(null);
          setCodes(r.recoveryCodes);
          void qc.invalidateQueries({ queryKey: statusKey });
        }}
      />
    </Card>
  );
}
