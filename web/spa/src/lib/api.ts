export class ApiError extends Error {
  readonly name = "ApiError";
  constructor(
    readonly code: string,
    readonly status: number,
    message: string,
    readonly attempt?: unknown,
  ) {
    super(message);
  }
}

export function createApi(onUnauthorized: () => void = () => {}) {
  let csrf = "";
  let generation = 0;
  const pending = new Set<AbortController>();

  return {
    setCSRF(token: string) {
      csrf = token;
    },
    clear() {
      csrf = "";
      generation++;
      for (const controller of pending) controller.abort();
      pending.clear();
    },
    async request<T>(path: string, init: RequestInit = {}): Promise<T> {
      const url = new URL(`/api/v1${path}`, "http://same-origin.invalid");
      if (
        !path.startsWith("/") ||
        path.startsWith("//") ||
        path.includes("\\") ||
        !url.pathname.startsWith("/api/v1/")
      ) {
        throw new Error("API paths must stay within /api/v1/");
      }
      const controller = new AbortController();
      const signal = init.signal
        ? AbortSignal.any([init.signal, controller.signal])
        : controller.signal;
      const started = generation;
      pending.add(controller);
      try {
        const headers = new Headers(init.headers);
        headers.set("Accept", "application/json");
        if (init.body != null && !headers.has("Content-Type"))
          headers.set("Content-Type", "application/json");
        const method = (init.method ?? "GET").toUpperCase();
        if (!["GET", "HEAD", "OPTIONS"].includes(method) && csrf) headers.set("X-CSRF-Token", csrf);
        const response = await fetch(`/api/v1${path}`, {
          ...init,
          headers,
          signal,
          credentials: "same-origin",
          redirect: "error",
        });
        const payload =
          response.status === 204 ? undefined : await response.json().catch(() => undefined);
        if (started !== generation || signal.aborted)
          throw new DOMException("Request cancelled", "AbortError");
        if (!response.ok) {
          const error = payload?.error;
          if (response.status === 401 && path !== "/auth/login") onUnauthorized();
          throw new ApiError(
            typeof error?.code === "string" ? error.code : "request_failed",
            response.status,
            typeof error?.message === "string"
              ? error.message
              : "The request could not be completed.",
            error?.attempt,
          );
        }
        if (response.status === 204) return undefined as T;
        if (!payload || !Object.hasOwn(payload, "data"))
          throw new ApiError(
            "invalid_response",
            response.status,
            "The server returned an invalid response.",
          );
        return payload.data as T;
      } finally {
        pending.delete(controller);
      }
    },
  };
}

export type ApiClient = ReturnType<typeof createApi>;
