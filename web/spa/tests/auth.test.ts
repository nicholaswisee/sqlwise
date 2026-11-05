import { afterEach, expect, test, vi } from "vite-plus/test";
import { createAppContext } from "../src/lib/query-client";

afterEach(() => vi.unstubAllGlobals());

test("401 cancels pending private queries and discards late results", async () => {
  const context = createAppContext();
  let finish!: (value: string) => void;
  const privateResult = context.queryClient.fetchQuery({
    queryKey: ["private"],
    queryFn: () =>
      new Promise<string>((resolve) => {
        finish = resolve;
      }),
  });
  const cancelled = expect(privateResult).rejects.toThrow();
  vi.stubGlobal("fetch", async () =>
    Response.json({ error: { code: "session_expired", message: "Expired" } }, { status: 401 }),
  );
  await expect(context.api.request("/private")).rejects.toMatchObject({ status: 401 });
  finish("old identity");
  await cancelled;
  expect(context.queryClient.getQueryCache().getAll()).toHaveLength(0);
});

test("challenge and login use memory-only CSRF and adopt the new token", async () => {
  const context = createAppContext();
  const requests: RequestInit[] = [];
  vi.stubGlobal("fetch", async (path: string, init: RequestInit) => {
    requests.push(init);
    if (path.endsWith("login-challenge"))
      return Response.json({ data: { csrf_token: "challenge", expires_at: "2099-01-01" } });
    return Response.json({ data: session("one") });
  });
  await context.auth.loginChallenge();
  await context.auth.login({ username: "one", password: "password" });
  expect(new Headers(requests[1].headers).get("X-CSRF-Token")).toBe("challenge");
  expect(JSON.parse(requests[1].body as string)).toEqual({ username: "one", password: "password" });
  await context.api.request("/example", { method: "POST" });
  expect(new Headers(requests[2].headers).get("X-CSRF-Token")).toBe("csrf-one");
});
const session = (id: string) => ({
  user: { id, username: id, is_platform_admin: false },
  csrf_token: `csrf-${id}`,
  expires_at: "2099-01-01",
  databases: [],
  needs_refresh: false,
});

test("anonymous session returns null without redirecting and clears private cache on 401", async () => {
  const context = createAppContext();
  context.queryClient.setQueryData(["private"], "secret");
  vi.stubGlobal("fetch", async () =>
    Response.json({ error: { code: "session_expired", message: "Expired" } }, { status: 401 }),
  );
  expect(await context.auth.session()).toBeNull();
  expect(context.queryClient.getQueryData(["private"])).toBeUndefined();
});

test("identity changes and logout clear cached private data; instances are isolated", async () => {
  const context = createAppContext();
  const other = createAppContext();
  other.queryClient.setQueryData(["private"], "other");
  vi.stubGlobal("fetch", async () => Response.json({ data: session("one") }));
  await context.auth.session();
  context.queryClient.setQueryData(["private"], "one");
  vi.stubGlobal("fetch", async () => Response.json({ data: session("two") }));
  await context.auth.session();
  expect(context.queryClient.getQueryData(["private"])).toBeUndefined();
  context.queryClient.setQueryData(["private"], "two");
  vi.stubGlobal("fetch", async () => new Response(null, { status: 204 }));
  await context.auth.logout();
  expect(context.queryClient.getQueryCache().getAll()).toHaveLength(0);
  expect(other.queryClient.getQueryData(["private"])).toBe("other");
});
