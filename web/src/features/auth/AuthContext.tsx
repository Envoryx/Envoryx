import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, ApiError, setUnauthorizedHandler } from "@/api/client";
import type { User } from "@/api/types";

interface AuthState {
  user: User | null;
  /**
   * Whether the session may administer the whole instance (the server's "admin" in
   * /auth/me). Not the same as the user's role: an API token may have less scope than
   * its owner, and a token limited to projects never administers the instance.
   */
  admin: boolean;
  needsSetup: boolean;
  loading: boolean;
  login: (username: string, password: string) => Promise<void>;
  setup: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [admin, setAdmin] = useState(false);
  const [needsSetup, setNeedsSetup] = useState(false);
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

  const login = useCallback(async (username: string, password: string) => {
    const res = await api.auth.login(username, password);
    // A browser session has exactly the user's role.
    setUser(res.user);
    setAdmin(roleIsAdmin(res.user.role));
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
      qc.clear();
    }
  }, [qc]);

  const value = useMemo(() => ({ user, admin, needsSetup, loading, login, setup, logout }), [user, admin, needsSetup, loading, login, setup, logout]);
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
