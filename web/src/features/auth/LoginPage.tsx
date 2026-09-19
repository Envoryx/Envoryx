import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { ApiError } from "@/api/client";
import { Button, Field, Input, Alert } from "@/components/ui";
import { LogoFull } from "@/layout/Logo";
import { useAuth } from "./AuthContext";

export function LoginPage({ mode }: { mode: "login" | "setup" }) {
  const { t } = useTranslation();
  const auth = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (auth.user) {
    return <Navigate to="/" replace />;
  }
  if (mode === "login" && auth.needsSetup) {
    return <Navigate to="/setup" replace />;
  }
  if (mode === "setup" && !auth.needsSetup && !auth.loading) {
    return <Navigate to="/login" replace />;
  }

  const from = (location.state as { from?: string } | null)?.from ?? "/";

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
        await auth.login(username.trim(), password);
      }
      navigate(from, { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("The server could not be reached."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-app px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <LogoFull className="w-64" />
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
        </form>
      </div>
    </div>
  );
}
