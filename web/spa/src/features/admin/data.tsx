import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ApiError, type ApiClient } from "../../lib/api";
import { useAuthenticatedContext } from "../../lib/authenticated-context";
import type { AdminSnapshot, ConnectionDetail, ConnectionList, ViewStatus } from "./types";

type Write = { path: string; body?: unknown; refreshSession?: boolean };

export function postCredentialWrite(api: ApiClient, path: string, body: unknown) {
  return api.request<unknown>(path, { method: "POST", body: JSON.stringify(body) });
}

export function performAdminWrite(
  api: ApiClient,
  path: string,
  body: unknown,
  nonCredentialWrite: () => Promise<unknown>,
): Promise<unknown> {
  return body && typeof body === "object" && "password" in body
    ? postCredentialWrite(api, path, body)
    : nonCredentialWrite();
}

export function useAdminSnapshot(path: string) {
  const { api, session } = useAuthenticatedContext();
  return useQuery({
    queryKey: ["admin", session.user.id, path],
    queryFn: ({ signal }) => api.request<AdminSnapshot>(path, { signal }),
  });
}

export function useConnections() {
  const { api, session } = useAuthenticatedContext();
  return useQuery({
    queryKey: ["admin", session.user.id, "/admin/databases"],
    queryFn: ({ signal }) => api.request<ConnectionList>("/admin/databases", { signal }),
  });
}

export function useConnection(id: string) {
  const { api, session } = useAuthenticatedContext();
  const path = `/admin/databases/${encodeURIComponent(id)}`;
  return useQuery({
    queryKey: ["admin", session.user.id, path],
    queryFn: ({ signal }) => api.request<ConnectionDetail>(path, { signal }),
  });
}

export function useAdminActions() {
  const { api, auth, queryClient, session } = useAuthenticatedContext();
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [refreshNeeded, setRefreshNeeded] = useState(false);
  const [credentialPending, setCredentialPending] = useState(false);
  const resetStatus = () => {
    setMessage("");
    setError("");
    setRefreshNeeded(false);
  };
  const success = async (refreshSession: boolean) => {
    setMessage("Changes saved successfully.");
    await queryClient.invalidateQueries({ queryKey: ["admin", session.user.id] });
    if (refreshSession) {
      await auth.session();
      await queryClient.invalidateQueries({ queryKey: ["account", session.user.id] });
    }
  };
  const failure = (cause: unknown) => {
    if (cause instanceof ApiError && cause.status === 401) return;
    if (cause instanceof ApiError && cause.status === 403) {
      setError("You do not have permission to perform this action.");
    } else if (
      cause instanceof ApiError &&
      cause.status === 409 &&
      cause.code === "authorization_stale"
    ) {
      setError("Your permissions changed. Refresh and try again.");
      setRefreshNeeded(true);
    } else {
      setError(
        cause instanceof ApiError
          ? cause.message
          : "The change could not be completed. Try again shortly.",
      );
    }
  };
  const mutation = useMutation({
    retry: false,
    mutationFn: ({ path, body }: Write) =>
      api.request<unknown>(path, {
        method: "POST",
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      }),
    onMutate: resetStatus,
    onSuccess: (_result, write) => success(write.refreshSession ?? false),
    onError: failure,
  });

  const refresh = async () => {
    setMessage("");
    setError("");
    setRefreshNeeded(false);
    try {
      const current = await auth.session();
      if (current) await queryClient.invalidateQueries({ queryKey: ["admin", current.user.id] });
    } catch (cause) {
      setError(
        cause instanceof ApiError ? cause.message : "Unable to refresh the current session.",
      );
    }
  };
  return {
    pending: mutation.isPending || credentialPending,
    message,
    error,
    refreshNeeded,
    onRefresh: refresh,
    run: async (path: string, body?: unknown, refreshSession = false) => {
      resetStatus();
      if (body && typeof body === "object" && "password" in body) {
        setCredentialPending(true);
        try {
          await performAdminWrite(api, path, body, () =>
            mutation.mutateAsync({ path, body, refreshSession }),
          );
          await success(refreshSession);
        } catch (cause) {
          failure(cause);
        } finally {
          setCredentialPending(false);
        }
        return;
      }
      await performAdminWrite(api, path, body, () =>
        mutation.mutateAsync({ path, body, refreshSession }),
      ).then(
        () => undefined,
        () => undefined,
      );
    },
    status: {
      message,
      error,
      pending: mutation.isPending || credentialPending,
      refreshNeeded,
      onRefresh: refresh,
    } satisfies ViewStatus,
  };
}

export const field = (data: FormData, name: string) => String(data.get(name) ?? "");
export const connectionBody = (data: FormData) => ({
  name: field(data, "name"),
  dbms: field(data, "dbms"),
  host: field(data, "host"),
  port: field(data, "port"),
  database: field(data, "database"),
  username: field(data, "username"),
  password: field(data, "password"),
  tls: field(data, "tls"),
  sandbox: data.get("sandbox") === "true",
});
