import type { QueryClient } from "@tanstack/react-query";
import { ApiError, type ApiClient } from "./api";

export interface Session {
  user: { id: string; username: string; is_platform_admin: boolean };
  csrf_token: string;
  expires_at: string;
  databases: { id: string; name: string; permission: string }[];
  needs_refresh?: boolean;
}

export interface LoginChallenge {
  csrf_token: string;
  expires_at: string;
}
export type LoginResult = Pick<Session, "user" | "csrf_token" | "expires_at">;

export interface AuthSnapshot {
  session: Session | null;
  revision: number;
  logoutUnconfirmed?: true;
}

export function createAuth(api: ApiClient, queryClient: QueryClient) {
  let snapshot: AuthSnapshot = { session: null, revision: 0 };
  const listeners = new Set<() => void>();
  function publish(next: AuthSnapshot) {
    snapshot = next;
    for (const listener of listeners) listener();
  }
  function clear(logoutUnconfirmed = false) {
    publish({
      session: null,
      revision: snapshot.revision + 1,
      ...(logoutUnconfirmed ? { logoutUnconfirmed: true as const } : {}),
    });
    api.clear();
    void queryClient.cancelQueries();
    queryClient.clear();
  }
  function attempt(init: RequestInit = {}) {
    const revision = snapshot.revision;
    const timeout = AbortSignal.timeout(15_000);
    const signal = init.signal ? AbortSignal.any([init.signal, timeout]) : timeout;
    return {
      revision,
      init: { ...init, signal },
      check() {
        if (revision !== snapshot.revision)
          throw new DOMException("Authentication changed", "AbortError");
        signal.throwIfAborted();
      },
    };
  }
  return {
    clear,
    getSnapshot: () => snapshot,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    async session(init?: RequestInit): Promise<Session | null> {
      const current = attempt(init);
      try {
        const value = await api.request<Session>("/auth/session", current.init);
        current.check();
        if (snapshot.session && snapshot.session.user.id !== value.user.id) clear();
        api.setCSRF(value.csrf_token);
        publish({ session: value, revision: snapshot.revision });
        return value;
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) {
          if (snapshot.revision === current.revision) clear();
          return null;
        }
        throw error;
      }
    },
    async loginChallenge(init?: RequestInit) {
      const current = attempt(init);
      const value = await api.request<LoginChallenge>("/auth/login-challenge", current.init);
      current.check();
      api.setCSRF(value.csrf_token);
      return value;
    },
    async login(
      input: { username: string; password: string },
      init?: RequestInit,
    ): Promise<LoginResult> {
      const current = attempt(init);
      const result = await api.request<LoginResult>("/auth/login", {
        ...current.init,
        method: "POST",
        body: JSON.stringify(input),
      });
      current.check();
      clear();
      api.setCSRF(result.csrf_token);
      return result;
    },
    async logout(): Promise<void> {
      let unconfirmed = false;
      try {
        await api.request<void>("/auth/logout", {
          method: "POST",
          signal: AbortSignal.timeout(15_000),
        });
      } catch (error) {
        unconfirmed = true;
        throw error;
      } finally {
        clear(unconfirmed);
      }
    },
  };
}

export type AuthClient = ReturnType<typeof createAuth>;
