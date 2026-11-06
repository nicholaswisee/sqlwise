import { afterEach, expect, test, vi } from "vite-plus/test";
import { ApiError, createApi } from "../src/lib/api";

afterEach(() => vi.unstubAllGlobals());

test("unwraps data and sends same-origin credentials and in-memory CSRF", async () => {
  const fetcher = vi.fn(async () => Response.json({ data: { ok: true } }));
  vi.stubGlobal("fetch", fetcher);
  const api = createApi();
  api.setCSRF("csrf");
  expect(await api.request("/example", { method: "POST", body: "{}" })).toEqual({ ok: true });
  const [path, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
  expect(path).toBe("/api/v1/example");
  expect(init.credentials).toBe("same-origin");
  expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf");
  expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
});

test("handles 204 without parsing JSON", async () => {
  vi.stubGlobal("fetch", async () => new Response(null, { status: 204 }));
  expect(await createApi().request("/auth/logout", { method: "POST" })).toBeUndefined();
});

test("preserves structured execution errors without retrying mutations", async () => {
  const fetcher = vi.fn(async () =>
    Response.json(
      { error: { code: "partial", message: "Stopped", attempt: { id: "a" } } },
      { status: 409 },
    ),
  );
  vi.stubGlobal("fetch", fetcher);
  await expect(createApi().request("/execute", { method: "POST" })).rejects.toMatchObject({
    name: "ApiError",
    code: "partial",
    status: 409,
    message: "Stopped",
    attempt: { id: "a" },
  });
  expect(fetcher).toHaveBeenCalledTimes(1);
});

test("non-JSON errors remain safe ApiErrors", async () => {
  vi.stubGlobal("fetch", async () => new Response("<html>proxy failed</html>", { status: 502 }));
  await expect(createApi().request("/test")).rejects.toBeInstanceOf(ApiError);
});

test("rejects paths outside the same-origin API boundary", async () => {
  const api = createApi();
  for (const path of [
    "https://evil.test",
    "//evil.test",
    "/../secret",
    "/%2e%2e/secret",
    "/x\\y",
  ]) {
    await expect(api.request(path)).rejects.toThrow();
  }
});

test("clearing aborts in-flight requests and rejects late responses even if fetch ignores abort", async () => {
  let finish!: (response: Response) => void;
  vi.stubGlobal(
    "fetch",
    () =>
      new Promise<Response>((resolve) => {
        finish = resolve;
      }),
  );
  const api = createApi();
  const pending = api.request("/private");
  api.clear();
  finish(Response.json({ data: "old identity" }));
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
});
