import { expect, test, vi } from "vite-plus/test";
import { createMemoryHistory } from "@tanstack/react-router";
import { getRouter } from "../src/router";

test("root and public home load without any API or private bootstrap", async () => {
  const fetcher = vi.fn(() => {
    throw new Error("Build shell must not fetch");
  });
  vi.stubGlobal("fetch", fetcher);
  try {
    const router = getRouter();
    router.update({
      context: router.options.context,
      history: createMemoryHistory({ initialEntries: ["/"] }),
    });
    await router.load();
    expect(router.state.matches.every((match) => match.status === "success")).toBe(true);
    expect(router.state.matches.map((match) => match.routeId)).toEqual(["__root__", "/"]);
    expect(fetcher).not.toHaveBeenCalled();
    expect(router.options.context.queryClient.getQueryCache().getAll()).toHaveLength(0);
    expect(getRouter().options.context.queryClient).not.toBe(router.options.context.queryClient);
  } finally {
    vi.unstubAllGlobals();
  }
});
