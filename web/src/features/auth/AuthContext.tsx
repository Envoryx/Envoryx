import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, ApiError, setSuspendedHandler, setTwoFactorSetupHandler, setUnauthorizedHandler } from "@/api/client";
import { keys } from "@/api/hooks";
import type { SecondStep, User } from "@/api/types";

/** The options of a passkey sign-in, fetched ahead of the click. */
export interface PasskeyRequest {
  id: string;
  publicKey: PublicKeyCredentialRequestOptionsJSON;
}
import { getPasskey } from "@/lib/webauthn";

interface AuthState {
  user: User | null;
  /**
   * Whether the session may administer the whole instance (the server's "admin" in
   * /auth/me). Not the same as the user's role: an API token may have less scope than
   * its owner, and a token limited to projects never administers the instance.
   */
  admin: boolean;
  /** Whether the session is the hoster's support, signed in with the admins' consent. */
  support: boolean;
  needsSetup: boolean;
  /** The instance requires a second factor of the user, who has none yet: only its setup is open. */
  twoFactorSetup: boolean;
  loading: boolean;
  /** Signs in with a password; resolves to the second step when the account has a second factor. */
  login: (username: string, password: string) => Promise<SecondStep | null>;
  /** Finishes the second step with a TOTP code or a recovery code. */
  completeWithCode: (ticket: string, code: { code?: string; recoveryCode?: string }) => Promise<void>;
  /**
   * Signs in with a passkey: alone, or as the second step with its ticket. Options fetched
   * beforehand (usePasskeyOptions) let the browser's dialog open right on the click, which
   * Safari insists on.
   */
  loginWithPasskey: (prepared: PasskeyRequest | null, ticket?: string) => Promise<void>;
  /** Reloads who is signed in (after a second factor was set up). */
  refresh: () => Promise<void>;
  setup: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [admin, setAdmin] = useState(false);
  const [support, setSupport] = useState(false);
  const [needsSetup, setNeedsSetup] = useState(false);
  const [twoFactorSetup, setTwoFactorSetup] = useState(false);
  const [loading, setLoading] = useState(true);
  const qc = useQueryClient();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await api.auth.setupStatus();
        if (cancelled) return;
        setNeedsSetup(status.needsSetup);
        if (!status.needsSetup) {
          try {
            const me = await api.auth.me(true);
            if (!cancelled) {
              setUser(me.user);
              setAdmin(me.admin ?? roleIsAdmin(me.user.role));
              setSupport(me.support ?? false);
              setTwoFactorSetup(me.twoFactorSetup ?? false);
            }
          } catch (err) {
            if (!(err instanceof ApiError && err.status === 401)) throw err;
          }
        }
      } catch {
        // Backend unreachable: the login page will surface the error on submit.
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setUser(null);
      setAdmin(false);
      qc.clear();
    });
    return () => setUnauthorizedHandler(null);
  }, [qc]);

  // The admins required a second factor while this session was open: the setup page follows.
  useEffect(() => {
    setTwoFactorSetupHandler(() => setTwoFactorSetup(true));
    return () => setTwoFactorSetupHandler(null);
  }, []);

  // A refusal for a suspension reloads the plan, which carries it: the lock page follows.
  useEffect(() => {
    setSuspendedHandler(() => void qc.invalidateQueries({ queryKey: keys.plan }));
    return () => setSuspendedHandler(null);
  }, [qc]);

  // A new session: its user, and whether it may only set up a second factor.
  const signedIn = useCallback(async (u: User) => {
    const me = await api.auth.me(true).catch(() => null);
    setTwoFactorSetup(me?.twoFactorSetup ?? false);
    setSupport(me?.support ?? false);
    // A browser session has exactly the user's role.
    setUser(u);
    setAdmin(roleIsAdmin(u.role));
  }, []);

  const login = useCallback(
    async (username: string, password: string) => {
      const res = await api.auth.login(username, password);
      if (res.secondFactor) return res.secondFactor;
      await signedIn(res.user);
      return null;
    },
    [signedIn],
  );

  const completeWithCode = useCallback(
    async (ticket: string, code: { code?: string; recoveryCode?: string }) => {
      const res = await api.auth.loginCode(ticket, code);
      await signedIn(res.user);
    },
    [signedIn],
  );

  const loginWithPasskey = useCallback(
    async (prepared: PasskeyRequest | null, ticket?: string) => {
      const opts = prepared ?? (await api.auth.passkeyOptions(ticket));
      const credential = await getPasskey(opts.publicKey);
      const res = await api.auth.passkeyLogin(opts.id, credential);
      await signedIn(res.user);
    },
    [signedIn],
  );

  const refresh = useCallback(async () => {
    const me = await api.auth.me(true);
    setUser(me.user);
    setAdmin(me.admin ?? roleIsAdmin(me.user.role));
    setSupport(me.support ?? false);
    setTwoFactorSetup(me.twoFactorSetup ?? false);
  }, []);

  const setup = useCallback(async (username: string, password: string) => {
    const res = await api.auth.setup(username, password);
    setNeedsSetup(false);
    setUser(res.user);
    setAdmin(roleIsAdmin(res.user.role));
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.auth.logout();
    } finally {
      setUser(null);
      setAdmin(false);
      setTwoFactorSetup(false);
      qc.clear();
    }
  }, [qc]);

  const value = useMemo(
    () => ({ user, admin, support, needsSetup, twoFactorSetup, loading, login, completeWithCode, loginWithPasskey, refresh, setup, logout }),
    [user, admin, support, needsSetup, twoFactorSetup, loading, login, completeWithCode, loginWithPasskey, refresh, setup, logout],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider");
  return ctx;
}

/** Whether a user role administers the instance (an empty role predates roles and was one). */
function roleIsAdmin(role: string): boolean {
  return role === "admin" || role === "";
}
