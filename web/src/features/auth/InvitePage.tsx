import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, api } from "@/api/client";
import { Alert, Button, Field, Input, Spinner } from "@/components/ui";
import { LogoFull } from "@/layout/Logo";
import { errorText } from "@/lib/errors";

/** The page an invitation link opens: set a password, or sign in with single sign-on. */
export function InvitePage() {
  const { t } = useTranslation();
  const { token = "" } = useParams();
  const navigate = useNavigate();
  const [invite, setInvite] = useState<{ username: string; reset: boolean } | null>(null);
  const [sso, setSso] = useState<{ enabled: boolean; name?: string } | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Set when the link itself cannot be used (unknown, used or expired).
  const [gone, setGone] = useState(false);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let live = true;
    api.auth.invitation(token).then(
      (i) => live && setInvite(i),
      (err) => {
        if (!live) return;
        if (isInviteInvalid(err)) setGone(true);
        else setError(errorText(err, t));
      },
    );
    api.auth.oidcStatus().then((s) => live && setSso(s), () => undefined);
    return () => {
      live = false;
    };
  }, [token, t]);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError(t("Passwords do not match."));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await api.auth.acceptInvitation(token, password);
      // The password is set; an account with a second factor still proves it.
      if (res.secondFactor) {
        navigate("/login", { replace: true, state: { secondFactor: res.secondFactor, passwordSet: true } });
        return;
      }
      // A full load picks up the new session everywhere.
      window.location.assign("/");
    } catch (err) {
      // The link may have expired, or been used elsewhere, since the page was opened.
      if (isInviteInvalid(err)) {
        setInvite(null);
        setGone(true);
      } else setError(errorText(err, t));
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-app px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <LogoFull className="w-96 max-w-full" />
          <h1 className="text-lg font-semibold text-fg">{gone ? t("This invitation link cannot be used") : invite?.reset ? t("Set a new password") : t("Welcome to Envoryx")}</h1>
        </div>
        <div className="space-y-4 rounded-xl border border-default bg-elevated p-6 shadow-sm">
          {error && <Alert tone="red">{error}</Alert>}
          {gone && (
            <>
              <p className="text-sm text-muted">{t("It is unknown, was already used or has expired. Ask an admin of this Envoryx for a new link.")}</p>
              <Link to="/login" className="flex w-full items-center justify-center rounded-md border border-default px-3 py-2 text-sm font-medium text-fg hover:bg-muted">
                {t("Go to sign-in")}
              </Link>
            </>
          )}
          {!invite && !error && !gone && <Spinner />}
          {invite && (
            <form onSubmit={onSubmit} className="space-y-4" noValidate>
              <p className="text-sm text-muted">{t("Choose the password for {{name}}.", { name: invite.username })}</p>
              <Field label={t("Password")} htmlFor="password" hint={t("At least 10 characters.")}>
                <Input id="password" type="password" autoComplete="new-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} required minLength={10} />
              </Field>
              <Field label={t("Confirm password")} htmlFor="confirm">
                <Input id="confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
              </Field>
              <Button type="submit" variant="primary" className="w-full" loading={busy}>
                {invite.reset ? t("Save password") : t("Create account")}
              </Button>
              {/* Only this link connects the account to single sign-on: the provider's
                  account is linked to the invited one, whatever its name there. */}
              {sso?.enabled && (
                <a href={`/api/v1/auth/oidc/start?invite=${encodeURIComponent(token)}`} className="flex w-full items-center justify-center rounded-md border border-default px-3 py-2 text-sm font-medium text-fg hover:bg-muted">
                  {t("Sign in with {{name}} instead", { name: sso.name ?? "SSO" })}
                </a>
              )}
            </form>
          )}
        </div>
      </div>
    </div>
  );
}

const isInviteInvalid = (err: unknown) => err instanceof ApiError && err.code === "invite_invalid";
