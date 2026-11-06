import { QueryClient } from "@tanstack/react-query";
import { createApi } from "./api";
import { createAuth } from "./auth";

export function createAppContext() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const api = createApi(() => auth.clear());
  const auth = createAuth(api, queryClient);
  return { queryClient, api, auth };
}

export type AppContext = ReturnType<typeof createAppContext>;
