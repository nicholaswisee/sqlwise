import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ApiError } from "../../lib/api";
import { useAuthenticatedContext } from "../../lib/authenticated-context";
import type { AdminSnapshot, ConnectionDetail, ConnectionList, ViewStatus } from "./types";

type Write = { path: string; body?: unknown; refreshSession?: boolean };

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
  const mutation = useMutation({
    retry: false,
    mutationFn: ({ path, body }: Write) =>
      api.request<unknown>(path, {
        method: "POST",
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      }),
    onMutate: () => {
      setMessage("");
      setError("");
      setRefreshNeeded(false);
    },
    onSuccess: async (_result, write) => {
      setMessage("Changes saved successfully.");
      await queryClient.invalidateQueries({ queryKey: ["admin", session.user.id] });
      if (write.refreshSession) {
        await auth.session();
        await queryClient.invalidateQueries({ queryKey: ["account", session.user.id] });
      }
    },
    onError: (cause) => {
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
    },
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
    pending: mutation.isPending,
    message,
    error,
    refreshNeeded,
    onRefresh: refresh,
    run: (path: string, body?: unknown, refreshSession = false) =>
      mutation.mutateAsync({ path, body, refreshSession }).then(
        () => undefined,
        () => undefined,
      ),
    status: {
      message,
      error,
      pending: mutation.isPending,
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
