import { expect, test } from "@playwright/test";

const publicTable = "cHVibGljAHRoaW5ncw";
const salesTable = "c2FsZXMAb3JkZXJz";
const workspace = {
  id: "db-1",
  name: "QA target",
  can_edit: true,
  engine: "PostgreSQL",
  dialect: "postgresql",
  current_version: 0,
  version_track: [],
  track_earlier: 0,
  capabilities: {
    schema_inspection: true,
    data_preview: true,
    explain: true,
    explain_analyze: false,
  },
};

test.beforeEach(async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.slice("/api/v1".length);
    let data: unknown;
    if (path === "/auth/session") {
      data = {
        user: { id: "editor", username: "editor", is_platform_admin: false },
        csrf_token: "csrf",
        expires_at: "2099-01-01",
        needs_refresh: false,
        databases: [{ id: workspace.id, name: workspace.name, permission: "edit" }],
      };
    } else if (path === "/databases") {
      data = {
        username: "editor",
        databases: [
          { id: workspace.id, name: workspace.name, permission: "edit", engine: workspace.engine },
        ],
      };
    } else if (path === "/databases/db-1") {
      const schema = url.searchParams.get("schema") || "public";
      data = {
        workspace,
        schemas: ["public", "sales"],
        schema,
        tables: [
          {
            id: schema === "sales" ? salesTable : publicTable,
            name: schema === "sales" ? "orders" : "things",
            columns: [{ name: "id", type: "integer", nullable: false, primary_key: true }],
            constraints: [],
            indexes: [],
          },
        ],
      };
    } else if (path.startsWith("/databases/db-1/tables/")) {
      const offset = Number(url.searchParams.get("offset") || 0);
      const limit = Number(url.searchParams.get("limit") || 50);
      const rows = Array.from({ length: 77 }, (_, i) => [
        { text: String(i + 1), null: false, binary: false, truncated: false },
        {
          text: i === 2 ? "<script>unsafe()</script>" : `row ${i + 1}`,
          null: i === 0,
          binary: false,
          truncated: i === 3,
        },
        { text: i === 1 ? "\\xdeadbeef" : "", null: i !== 1, binary: i === 1, truncated: false },
      ]).slice(offset, offset + limit);
      data = {
        workspace,
        schema: "public",
        table: "things",
        offset,
        limit,
        rows,
        has_next: offset + limit < 77,
        ordered: false,
        truncated: false,
        columns: [
          { name: "id", type: "integer" },
          { name: "label", type: "text" },
          { name: "bytes", type: "bytea" },
        ],
      };
    } else {
      await route.fulfill({
        status: 403,
        json: { error: { code: "permission_denied", message: "Permission denied.", fields: {} } },
      });
      return;
    }
    await route.fulfill({ json: { data } });
  });
});

test("schema selection, table links and browser history stay in the SPA", async ({ page }) => {
  const documents: string[] = [];
  page.on("request", (request) => {
    if (request.resourceType() === "document") documents.push(request.url());
  });
  await page.goto("/databases");
  await page.getByRole("link", { name: "QA target", exact: true }).click();
  await expect(page.getByRole("heading", { name: "public.things", exact: true })).toBeVisible();
  await page.getByLabel("Schema", { exact: true }).selectOption("sales");
  await expect(page).toHaveURL(/schema=sales/);
  await expect(page.getByRole("heading", { name: "sales.orders", exact: true })).toBeVisible();
  await page.goBack();
  await expect(page.getByRole("heading", { name: "public.things", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "View data", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/tables/${publicTable}`));
  await expect(page.getByText("\\xdeadbeef", { exact: true })).toBeVisible();
  await expect(page.getByText("<script>unsafe()</script>", { exact: true })).toBeVisible();
  expect(documents).toHaveLength(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("custom page sizes, NULL/binary cells and bounded paging preserve URL state", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(`/databases/db-1/tables/${publicTable}?offset=0&limit=75`);
  await expect(page.getByLabel("Rows per page", { exact: true })).toHaveValue("75");
  await expect(page.getByText("Rows 1–75", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Previous", exact: true })).toHaveCount(0);
  await expect(page.getByText("NULL", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("… truncated", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await expect(page).toHaveURL(/offset=75/);
  await expect(page.getByText("Rows 76–77", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Next", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Previous", exact: true }).click();
  await expect(page.getByText("Rows 1–75", { exact: true })).toBeVisible();
  await page.getByLabel("Rows per page", { exact: true }).selectOption("100");
  await expect(page).toHaveURL(/limit=100/);
  await expect(page.getByText("Rows 1–77", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});
