import { expect, test } from "@playwright/test";

test("schema diff preview preserves review input and only creates an explicitly reviewed draft", async ({
  page,
}) => {
  let previews = 0;
  let created: Record<string, unknown> | undefined;
  const workspace = {
    id: "db-1",
    name: "Database",
    can_edit: true,
    engine: "PostgreSQL",
    version_track: [],
    track_earlier: 0,
  };
  const session = {
    user: { id: "editor", username: "editor", is_platform_admin: false },
    csrf_token: "csrf",
    expires_at: "2099-01-01",
    needs_refresh: false,
    databases: [],
  };
  const draft = {
    id: "new-draft",
    title: "Reviewed change",
    sql: "CREATE TABLE reviewed (id int);",
    sql_hash: "abc",
    dialect: "postgresql",
    revision: 1,
    applied_version: 0,
    last_status: "",
    created_by_name: "editor",
    created_at: "2026-11-05T00:00:00Z",
    updated_by_name: "editor",
    updated_at: "2026-11-05T00:00:00Z",
  };
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let data: unknown;
    if (path.endsWith("/auth/session")) data = session;
    else if (path.endsWith("/migrations"))
      data = { workspace, current_version: 0, drafts: [], attempts: [] };
    else if (path.endsWith("/schema-diff/preview")) {
      previews++;
      data =
        previews === 1
          ? {
              schema: "public",
              source_fingerprint: "fingerprint-1",
              changes: [],
              warnings: [],
              sql: "",
              requires_review: false,
            }
          : {
              schema: "public",
              source_fingerprint: `fingerprint-${previews}`,
              changes: [{ text: "Add table reviewed", destructive: false }],
              warnings: [],
              sql: draft.sql,
              requires_review: false,
            };
    } else if (path.endsWith("/schema-diff/draft")) {
      created = route.request().postDataJSON();
      data = draft;
    } else if (path.endsWith("/migrations/new-draft"))
      data = { workspace, current_version: 0, draft, attempts: [], blocked: "", blocked_by: null };
    else throw new Error(`Unexpected schema diff request: ${path}`);
    await route.fulfill({ json: { data } });
  });
  await page.goto("/databases/db-1/schema-diff");
  await page.getByRole("button", { name: "Preview changes", exact: true }).click();
  await expect(
    page.getByText("The live schema already matches the desired snapshot.", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Use as draft", exact: true })).toHaveCount(0);
  const desired = '{"dialect":"postgresql","schema":"public","tables":[]}';
  await page.getByLabel("Desired snapshot (JSON)", { exact: true }).fill(desired);
  await page.getByRole("button", { name: "Preview changes", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Candidate SQL", exact: true })).toBeVisible();
  await page.getByLabel("Desired snapshot (JSON)", { exact: true }).fill(desired + "\n");
  await expect(page.getByRole("button", { name: "Use as draft", exact: true })).toBeDisabled();
  await expect(page.getByRole("status")).toContainText("Preview it again");
  await page.getByRole("button", { name: "Preview changes", exact: true }).click();
  await page.getByLabel("Draft title", { exact: true }).fill("Reviewed change");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByRole("button", { name: "Use as draft", exact: true }).click();
  await expect(page).toHaveURL(/\/migrations\/new-draft$/);
  await expect(page.getByRole("heading", { name: "Reviewed change", exact: true })).toBeVisible();
  expect(created).toEqual({
    desired: desired + "\n",
    source_fingerprint: "fingerprint-3",
    title: "Reviewed change",
  });
});
