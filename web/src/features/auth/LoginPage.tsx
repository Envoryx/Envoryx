import { useCallback, useEffect, useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import { api, ApiError } from "@/api/client";
import type { SecondStep } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { Button, Field, Input, Alert } from "@/components/ui";
import { LogoFull } from "@/layout/Logo";
import { useAuth, type PasskeyRequest } from "./AuthContext";
import { errorText, translateMessage } from "@/lib/errors";
import { passkeyCancelled, passkeysAvailable } from "@/lib/webauthn";

// The rescue commands (envoryx admin …) live in the container shell; the page can only point there.
const LOST_ACCESS_DOCS = "https://github.com/envoryx/envoryx/blob/main/DEPLOYMENT.md#lost-access";

export function LoginPage({ mode }: { mode: "login" | "setup" }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  // A failed single sign-on comes back here with ?sso_error=1; the reason is in a cookie
  // the server set, so a link can't put text of its choosing on this page.
  const [error, setError] = useState<string | null>(() => {
    if (!new URLSearchParams(location.search).has("sso_error")) return null;
    const reason = takeSsoError();
    return reason ? translateMessage(reason, t) : t("Single sign-on failed.");
  });
  const [busy, setBusy] = useState(false);
  const [sso, setSso] = useState<{ enabled: boolean; name?: string } | null>(null);
  const [managed, setManaged] = useState(false);
  // The second step after the password; an invitation link that reset the password
  // hands it over in the location state.
  const handed = location.state as { secondFactor?: SecondStep; passwordSet?: boolean } | null;
  const [step, setStep] = useState<SecondStep | null>(handed?.secondFactor ?? null);
  const [notice, setNotice] = useState<string | null>(() => (handed?.passwordSet ? t("The new password is saved. Confirm the sign-in with your second factor.") : null));
  const passkeys = passkeysAvailable();
  const prepared = usePasskeyOptions(mode === "login" && passkeys && !step && !auth.user);
  useEffect(() => {
    if (mode !== "login") return;
    let live = true;
    api.auth.oidcStatus().then((s) => live && setSso(s), () => undefined);
    api.auth.setupStatus().then((s) => live && setManaged(!!s.managed), () => undefined);
    return () => {
      live = false;
    };
  }, [mode]);

  // Where RequireAuth sent the user from; also the target once signed in (this render
  // happens before onSubmit's own navigate and must not override it with "/").
  const from = (location.state as { from?: string } | null)?.from ?? "/";

  if (auth.user) {
    return <Navigate to={from} replace />;
  }
  if (mode === "login" && auth.needsSetup) {
    return <Navigate to="/setup" replace />;
  }
  if (mode === "setup" && !auth.needsSetup && !auth.loading) {
    return <Navigate to="/login" replace />;
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (mode === "setup" && password !== confirm) {
      setError(t("Passwords do not match."));
      return;
    }
    setBusy(true);
    try {
      if (mode === "setup") {
        await auth.setup(username.trim(), password);
      } else {
        const next = await auth.login(username.trim(), password);
        if (next) {
          setStep(next);
          return;
        }
      }
      navigate(from, { replace: true });
    } catch (err) {
      setError(errorText(err, t, t("The server could not be reached.")));
    } finally {
      setBusy(false);
    }
  }

  async function passkeySignIn() {
    setError(null);
    setBusy(true);
    try {
      await auth.loginWithPasskey(prepared.take());
      navigate(from, { replace: true });
    } catch (err) {
      if (!passkeyCancelled(err)) setError(errorText(err, t, t("The passkey was not accepted.")));
    } finally {
      setBusy(false);
    }
  }

  if (step) {
    return (
      <SecondStepForm
        step={step}
        notice={notice}
        onDone={() => navigate(from, { replace: true })}
        onRestart={(why) => {
          setStep(null);
          setNotice(null);
          setPassword("");
          setError(why);
        }}
      />
    );
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-app px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <LogoFull className="w-96 max-w-full" />
          <div className="text-center">
            <h1 className="text-lg font-semibold text-fg">{mode === "setup" ? t("Welcome to Envoryx") : t("Sign in to Envoryx")}</h1>
            <p className="mt-1 text-sm text-muted">
              {mode === "setup" ? t("Create the administrator account to get started.") : t("Docker-native development environments.")}
            </p>
          </div>
        </div>
        <form onSubmit={onSubmit} className="space-y-4 rounded-xl border border-default bg-elevated p-6 shadow-sm" noValidate>
          {error && <Alert tone="red">{error}</Alert>}
          <Field label={t("Username")} htmlFor="username">
            <Input id="username" autoComplete="username" autoFocus value={username} onChange={(e) => setUsername(e.target.value)} required />
          </Field>
          <Field label={t("Password")} htmlFor="password" hint={mode === "setup" ? t("At least 10 characters.") : undefined}>
            <Input
              id="password"
              type="password"
              autoComplete={mode === "setup" ? "new-password" : "current-password"}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              minLength={mode === "setup" ? 10 : undefined}
            />
          </Field>
          {mode === "setup" && (
            <Field label={t("Confirm password")} htmlFor="confirm">
              <Input id="confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
            </Field>
          )}
          <Button type="submit" variant="primary" className="w-full" loading={busy}>
            {mode === "setup" ? t("Create account") : t("Sign in")}
          </Button>
          {mode === "login" && (passkeys || sso?.enabled) && (
            <div className="flex items-center gap-3 text-xs text-subtle">
              <span className="h-px flex-1 bg-[var(--border)]" /> {t("or")} <span className="h-px flex-1 bg-[var(--border)]" />
            </div>
          )}
          {mode === "login" && passkeys && (
            <Button type="button" className="w-full" icon={<KeyRound className="size-4" />} disabled={busy} onClick={() => passkeySignIn()}>
              {t("Sign in with a passkey")}
            </Button>
          )}
          {mode === "login" && sso?.enabled && (
            <>
              <a href={`/api/v1/auth/oidc/start?return=${encodeURIComponent(from)}`} className="flex w-full items-center justify-center rounded-md border border-default px-3 py-2 text-sm font-medium text-fg hover:bg-muted">
                {t("Sign in with {{name}}", { name: sso.name ?? "SSO" })}
              </a>
            </>
          )}
          {mode === "login" && managed && <p className="text-center text-xs text-fg-muted">{t("Forgot the password? An admin of this instance can send you a reset link; if you are the only admin, ask your hoster.")}</p>}
          {mode === "login" && !managed && (
            <p className="text-center text-xs text-fg-muted">
              <a href={LOST_ACCESS_DOCS} target="_blank" rel="noreferrer" className="underline hover:text-fg">
                {t("Forgot the password?")}
              </a>
            </p>
          )}
        </form>
      </div>
    </div>
  );
}

/** The second step of a sign-in: a TOTP code, a passkey or a recovery code. */
function SecondStepForm({ step, notice, onDone, onRestart }: { step: SecondStep; notice: string | null; onDone: () => void; onRestart: (why: string | null) => void }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const hasTotp = step.methods.includes("totp");
  const hasPasskey = step.methods.includes("passkey") && passkeysAvailable();
  const prepared = usePasskeyOptions(hasPasskey, step.ticket);
  const [recovery, setRecovery] = useState(!hasTotp && !hasPasskey);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await auth.completeWithCode(step.ticket, recovery ? { recoveryCode: code.trim() } : { code: code.replace(/\s/g, "") });
      onDone();
    } catch (err) {
      if (err instanceof ApiError && err.code === "second_factor_expired") {
        onRestart(t("The sign-in took too long or failed too often; sign in again."));
        return;
      }
      setError(err instanceof ApiError && err.code === "invalid_code" ? t("The code is wrong or was used already.") : errorText(err, t));
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  async function passkey() {
    setError(null);
    setBusy(true);
    try {
      await auth.loginWithPasskey(prepared.take(), step.ticket);
      onDone();
    } catch (err) {
      if (err instanceof ApiError && err.code === "second_factor_expired") {
        onRestart(t("The sign-in took too long or failed too often; sign in again."));
        return;
      }
      if (!passkeyCancelled(err)) setError(errorText(err, t, t("The passkey was not accepted.")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-app px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <LogoFull className="w-96 max-w-full" />
          <div className="text-center">
            <h1 className="text-lg font-semibold text-fg">{t("Two-factor authentication")}</h1>
            <p className="mt-1 text-sm text-muted">
              {recovery ? t("Enter one of your recovery codes. Each works once.") : hasTotp ? t("Enter the 6-digit code from your authenticator app.") : t("Confirm the sign-in with your passkey.")}
            </p>
          </div>
        </div>
        <div className="space-y-4 rounded-xl border border-default bg-elevated p-6 shadow-sm">
          {notice && <Alert tone="green">{notice}</Alert>}
          {error && <Alert tone="red">{error}</Alert>}
          {(hasTotp || recovery) && (
            <form onSubmit={submit} className="space-y-4" noValidate>
              <Field label={recovery ? t("Recovery code") : t("Code")} htmlFor="second-factor-code">
                <Input
                  id="second-factor-code"
                  autoFocus
                  autoComplete={recovery ? "off" : "one-time-code"}
                  inputMode={recovery ? "text" : "numeric"}
                  placeholder={recovery ? "xxxxx-xxxxx" : "123456"}
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  className="font-mono tracking-widest"
                />
              </Field>
              <Button type="submit" variant="primary" className="w-full" loading={busy} disabled={!code.trim()}>
                {t("Verify")}
              </Button>
            </form>
          )}
          {hasPasskey && !recovery && (
            <Button type="button" variant={hasTotp ? "secondary" : "primary"} className="w-full" icon={<KeyRound className="size-4" />} disabled={busy} autoFocus={!hasTotp} onClick={() => void passkey()}>
              {t("Use a passkey")}
            </Button>
          )}
          {step.methods.includes("passkey") && !passkeysAvailable() && !recovery && (
            <p className="text-xs text-muted">{t("Passkeys work only when Envoryx is opened over https with a host name.")}</p>
          )}
          <div className="flex flex-wrap justify-between gap-2 text-xs text-fg-muted">
            {step.methods.includes("recovery") && (
              <button
                type="button"
                className="underline hover:text-fg"
                onClick={() => {
                  setRecovery(!recovery);
                  setCode("");
                  setError(null);
                }}
              >
                {recovery ? t("Use the app or a passkey") : t("Use a recovery code")}
              </button>
            )}
            <button type="button" className="underline hover:text-fg" onClick={() => onRestart(null)}>
              {t("Back to sign-in")}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

/**
 * Fetches the options of a passkey sign-in ahead of the click, so the browser's dialog
 * opens on the click itself (Safari refuses one that follows a request). take() hands
 * them out once; null means the click fetches its own.
 */
function usePasskeyOptions(enabled: boolean, ticket?: string) {
  const [req, setReq] = useState<{ value: PasskeyRequest; at: number } | null>(null);
  const [round, setRound] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    api.auth.passkeyOptions(ticket).then(
      (value) => live && setReq({ value, at: Date.now() }),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [enabled, ticket, round]);
  const take = useCallback((): PasskeyRequest | null => {
    const r = req;
    setReq(null);
    setRound((n) => n + 1);
    // The server keeps a ceremony for five minutes.
    return r && Date.now() - r.at < 4 * 60_000 ? r.value : null;
  }, [req]);
  return { take };
}

/** Reads and clears the reason of a failed single sign-on the server left in a cookie. */
function takeSsoError(): string | null {
  for (const name of ["__Host-envoryx_sso_error", "envoryx_sso_error"]) {
    const raw = document.cookie
      .split("; ")
      .find((c) => c.startsWith(name + "="))
      ?.slice(name.length + 1);
    if (!raw) continue;
    document.cookie = `${name}=; Path=/; Max-Age=0${name.startsWith("__Host-") ? "; Secure" : ""}`;
    try {
      const b64 = raw.replace(/-/g, "+").replace(/_/g, "/");
      const bytes = Uint8Array.from(atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4)), (c) => c.charCodeAt(0));
      return new TextDecoder().decode(bytes);
    } catch {
      return null;
    }
  }
  return null;
}
