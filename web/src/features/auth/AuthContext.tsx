import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, ApiError, setUnauthorizedHandler } from "@/api/client";
import type { User } from "@/api/types";

interface AuthState {
  user: User | null;
  needsSetup: boolean;
  loading: boolean;
  login: (username: string, password: string) => Promise<void>;
  setup: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
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
            if (!cancelled) setUser(me.user);
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
      qc.clear();
    });
    return () => setUnauthorizedHandler(null);
  }, [qc]);

  const login = useCallback(async (username: string, password: string) => {
    const res = await api.auth.login(username, password);
    setUser(res.user);
  }, []);

  const setup = useCallback(async (username: string, password: string) => {
    const res = await api.auth.setup(username, password);
    setNeedsSetup(false);
    setUser(res.user);
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.auth.logout();
    } finally {
      setUser(null);
      qc.clear();
    }
  }, [qc]);

  const value = useMemo(() => ({ user, needsSetup, loading, login, setup, logout }), [user, needsSetup, loading, login, setup, logout]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider");
  return ctx;
}
