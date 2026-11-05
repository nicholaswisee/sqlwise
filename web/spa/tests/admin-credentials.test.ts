import { QueryClient } from "@tanstack/react-query";
import { expect, test } from "vite-plus/test";
import { createApi } from "../src/lib/api";
import { field, performAdminWrite } from "../src/features/admin/data";

test("admin text fields never coerce an uploaded file into a credential", () => {
  const form = new FormData();
  form.set("username", "editor");
  form.set("password", new Blob(["not a text field"]), "password.txt");
  expect(field(form, "username")).toBe("editor");
  expect(field(form, "missing")).toBe("");
  expect(field(form, "password")).toBe("");
});

test("credential-bearing writes never retain their password in the query mutation cache", async () => {
  const client = new QueryClient();
  const api = createApi();
  api.setCSRF("csrf-test");
  const previousFetch = globalThis.fetch;
  let status = 200;
  const password = "private-one-time-secret";
  globalThis.fetch = async () =>
    new Response(
      JSON.stringify(
        status === 200 ? { data: {} } : { error: { code: "validation", message: "invalid" } },
      ),
      { status, headers: { "Content-Type": "application/json" } },
    );
  const assertNoSecret = () => {
    const variables = client
      .getMutationCache()
      .getAll()
      .map((mutation) => mutation.state.variables);
    expect(JSON.stringify(variables)).not.toContain(password);
    expect(variables).toEqual(baseline);
  };
  const cachedMutation = client.getMutationCache().build(client, {
    mutationFn: async (variables: { path: string; body: unknown }) => variables,
  });
  const cachedWrite = () => cachedMutation.execute({ path: "/admin/users/u1", body: { password } });
  const baseline = client
    .getMutationCache()
    .getAll()
    .map((mutation) => mutation.state.variables);
  try {
    await performAdminWrite(api, "/admin/users/u1", { password }, cachedWrite);
    assertNoSecret();
    status = 422;
    await expect(
      performAdminWrite(api, "/admin/databases", { password }, cachedWrite),
    ).rejects.toMatchObject({ status: 422 });
    assertNoSecret();
  } finally {
    globalThis.fetch = previousFetch;
    api.clear();
    client.clear();
  }
});
