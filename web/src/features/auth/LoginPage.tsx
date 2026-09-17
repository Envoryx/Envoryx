import { useState, type FormEvent } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { ApiError } from "@/api/client";
import { Button, Field, Input, Alert } from "@/components/ui";
import { Logo } from "@/layout/Logo";
import { useAuth } from "./AuthContext";

export function LoginPage({ mode }: { mode: "login" | "setup" }) {
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
      setError("Passwords do not match.");
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
      setError(err instanceof ApiError ? err.message : "The server could not be reached.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-app px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <Logo size={40} />
          <div className="text-center">
            <h1 className="text-lg font-semibold text-fg">{mode === "setup" ? "Welcome to Staqio" : "Sign in to Staqio"}</h1>
            <p className="mt-1 text-sm text-muted">
              {mode === "setup" ? "Create the administrator account to get started." : "Docker-native development environments."}
            </p>
          </div>
        </div>
        <form onSubmit={onSubmit} className="space-y-4 rounded-xl border border-default bg-elevated p-6 shadow-sm" noValidate>
          {error && <Alert tone="red">{error}</Alert>}
          <Field label="Username" htmlFor="username">
            <Input id="username" autoComplete="username" autoFocus value={username} onChange={(e) => setUsername(e.target.value)} required />
          </Field>
          <Field label="Password" htmlFor="password" hint={mode === "setup" ? "At least 10 characters." : undefined}>
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
            <Field label="Confirm password" htmlFor="confirm">
              <Input id="confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
            </Field>
          )}
          <Button type="submit" variant="primary" className="w-full" loading={busy}>
            {mode === "setup" ? "Create account" : "Sign in"}
          </Button>
        </form>
      </div>
    </div>
  );
}
