import { createRouter } from "@tanstack/react-router";
import { QueryClientProvider } from "@tanstack/react-query";
import { routeTree } from "./routeTree.gen";
import { createAppContext } from "./lib/query-client";

export function getRouter() {
  const context = createAppContext();
  return createRouter({
    routeTree,
    context,
    scrollRestoration: true,
    defaultPreload: "intent",
    Wrap: ({ children }) => (
      <QueryClientProvider client={context.queryClient}>{children}</QueryClientProvider>
    ),
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof getRouter>;
  }
}
