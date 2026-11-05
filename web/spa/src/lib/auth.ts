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

export function createAuth(api: ApiClient, queryClient: QueryClient) {
  let identity: string | null = null;
  function clear() {
    identity = null;
    api.clear();
    void queryClient.cancelQueries();
    queryClient.clear();
  }
  function adopt<T extends LoginResult>(value: T): T {
    if (identity !== null && identity !== value.user.id) clear();
    identity = value.user.id;
    api.setCSRF(value.csrf_token);
    return value;
  }
  return {
    clear,
    async session(init?: RequestInit): Promise<Session | null> {
      try {
        return adopt(await api.request<Session>("/auth/session", init));
      } catch (error) {
        if (error instanceof ApiError && error.status === 401) return null;
        throw error;
      }
    },
    async loginChallenge(init?: RequestInit) {
      const value = await api.request<LoginChallenge>("/auth/login-challenge", init);
      api.setCSRF(value.csrf_token);
      return value;
    },
    async login(input: { username: string; password: string }): Promise<LoginResult> {
      const result = await api.request<LoginResult>("/auth/login", {
        method: "POST",
        body: JSON.stringify(input),
      });
      clear();
      return adopt(result);
    },
    async logout(): Promise<void> {
      try {
        await api.request<void>("/auth/logout", { method: "POST" });
      } finally {
        clear();
      }
    },
  };
}

export type AuthClient = ReturnType<typeof createAuth>;
