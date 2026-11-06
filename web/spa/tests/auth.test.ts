import { afterEach, expect, test, vi } from "vite-plus/test";
import { createAppContext } from "../src/lib/query-client";
import { createApi } from "../src/lib/api";
import { createAuth } from "../src/lib/auth";
import { QueryClient } from "@tanstack/react-query";

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

test("snapshot is stable, observable, and clear publishes before cancellation", async () => {
  const context = createAppContext();
  expect(context.auth.getSnapshot()).toBe(context.auth.getSnapshot());
  const observed: unknown[] = [];
  const unsubscribe = context.auth.subscribe(() => observed.push(context.auth.getSnapshot()));
  vi.stubGlobal("fetch", async () => Response.json({ data: session("one") }));
  await context.auth.session();
  expect(context.auth.getSnapshot().session?.user.id).toBe("one");
  const before = context.auth.getSnapshot().revision;
  const cancel = vi.spyOn(context.api, "clear").mockImplementation(() => {
    expect(context.auth.getSnapshot()).toEqual({ session: null, revision: before + 1 });
    expect(observed.at(-1)).toBe(context.auth.getSnapshot());
  });
  context.auth.clear();
  expect(cancel).toHaveBeenCalledOnce();
  unsubscribe();
});

for (const method of ["session", "login", "loginChallenge"] as const) {
  test(`clear racing ${method} cannot restore session or CSRF even when transport ignores abort`, async () => {
    const api = createApi();
    const auth = createAuth(api, new QueryClient());
    let finish!: (value: unknown) => void;
    vi.spyOn(api, "request").mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const token = vi.spyOn(api, "setCSRF");
    const pending =
      method === "login" ? auth.login({ username: "one", password: "password" }) : auth[method]();
    auth.clear();
    finish(session("one"));
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(auth.getSnapshot()).toEqual({ session: null, revision: 1 });
    expect(token).not.toHaveBeenCalled();
  });
}

test("partial login never publishes a full session; checked identity replacement increments revision", async () => {
  const context = createAppContext();
  vi.stubGlobal("fetch", async () => Response.json({ data: session("one") }));
  await context.auth.login({ username: "one", password: "password" });
  expect(context.auth.getSnapshot().session).toBeNull();
  await context.auth.session();
  const before = context.auth.getSnapshot().revision;
  vi.stubGlobal("fetch", async () => Response.json({ data: session("two") }));
  await context.auth.session();
  expect(context.auth.getSnapshot().revision).toBe(before + 1);
  expect(context.auth.getSnapshot().session?.user.id).toBe("two");
});

test("aborted StrictMode attempt cannot publish after a replacement check", async () => {
  const api = createApi();
  const auth = createAuth(api, new QueryClient());
  let finish!: (value: unknown) => void;
  vi.spyOn(api, "request")
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValueOnce(session("two"));
  const controller = new AbortController();
  const pending = auth.session({ signal: controller.signal });
  controller.abort();
  await auth.session();
  finish(session("one"));
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  expect(auth.getSnapshot().session?.user.id).toBe("two");
});

test("auth attempts have a finite timeout and never adopt a timed-out response", async () => {
  const api = createApi();
  const auth = createAuth(api, new QueryClient());
  const controller = new AbortController();
  const timeout = vi.spyOn(AbortSignal, "timeout").mockReturnValue(controller.signal);
  let finish!: (value: unknown) => void;
  vi.spyOn(api, "request").mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  try {
    const pending = auth.session();
    expect(timeout).toHaveBeenCalledWith(15_000);
    controller.abort(new DOMException("Timed out", "TimeoutError"));
    finish(session("one"));
    await expect(pending).rejects.toMatchObject({ name: "TimeoutError" });
    expect(auth.getSnapshot().session).toBeNull();
  } finally {
    timeout.mockRestore();
  }
});

test("failed logout publishes an honest local-only state and removes all private state", async () => {
  const context = createAppContext();
  vi.stubGlobal("fetch", async () => Response.json({ data: session("one") }));
  await context.auth.session();
  context.queryClient.setQueryData(["private"], "secret");
  vi.stubGlobal("fetch", async () => {
    throw new TypeError("Offline");
  });
  await expect(context.auth.logout()).rejects.toThrow("Offline");
  expect(context.auth.getSnapshot()).toMatchObject({ session: null, logoutUnconfirmed: true });
  expect(context.queryClient.getQueryCache().getAll()).toHaveLength(0);
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
