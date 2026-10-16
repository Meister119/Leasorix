import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { api, onUnauthorized, setCsrfToken, unwrap, type S } from "./api/client";

type Status = "loading" | "setup" | "anonymous" | "authenticated";

interface AuthValue {
  status: Status;
  user?: S["User"];
  login: (username: string, password: string) => Promise<void>;
  setup: (token: string, username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  /** True if the user may change DHCP configuration (and the server is not a read-only HA partner). */
  canWrite: boolean;
  /** True if the user may operate on leases (operator or admin). */
  canOperate: boolean;
  isAdmin: boolean;
  /** Configuration is read-only here (HA partner); changes go to the config leader. */
  configReadOnly: boolean;
  configLeader?: string;
  haEnabled: boolean;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>("loading");
  const [user, setUser] = useState<S["User"]>();
  const [system, setSystem] = useState<S["SystemInfo"]>();

  useEffect(() => {
    if (status !== "authenticated") return;
    let cancelled = false;
    api.GET("/system").then((r) => {
      if (!cancelled && r.data) setSystem(r.data);
    });
    return () => {
      cancelled = true;
    };
  }, [status]);

  const apply = useCallback((s: S["Session"]) => {
    setCsrfToken(s.csrf_token);
    setUser(s.user);
    setStatus("authenticated");
  }, []);

  const reset = useCallback(() => {
    setCsrfToken("");
    setUser(undefined);
    setStatus("anonymous");
  }, []);

  useEffect(() => {
    onUnauthorized(reset);
    (async () => {
      try {
        const st = await unwrap(api.GET("/setup"));
        if (st.setup_required) {
          setStatus("setup");
          return;
        }
        const me = await api.GET("/auth/me");
        if (me.data) apply(me.data);
        else reset();
      } catch {
        reset();
      }
    })();
  }, [apply, reset]);

  const value = useMemo<AuthValue>(
    () => ({
      status,
      user,
      login: async (username, password) => apply(await unwrap(api.POST("/auth/login", { body: { username, password } }))),
      setup: async (setup_token, username, password) =>
        apply(await unwrap(api.POST("/setup", { body: { setup_token, username, password } }))),
      logout: async () => {
        await api.POST("/auth/logout");
        reset();
      },
      canOperate: user?.role === "admin" || user?.role === "operator",
      canWrite: (user?.role === "admin" || user?.role === "operator") && !system?.config_read_only,
      isAdmin: user?.role === "admin",
      configReadOnly: !!system?.config_read_only,
      configLeader: system?.config_leader,
      haEnabled: !!system?.ha_enabled,
    }),
    [status, user, system, apply, reset],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const v = useContext(AuthContext);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}
