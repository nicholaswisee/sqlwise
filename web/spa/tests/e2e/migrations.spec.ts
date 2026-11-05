import { expect, test } from "@playwright/test";

const session = {
  user: { id: "editor", username: "editor", is_platform_admin: false },
  csrf_token: "csrf",
  expires_at: "2099-01-01T00:00:00Z",
  databases: [{ id: "db-1", name: "Database", permission: "edit" }],
};
const workspace = {
  id: "db-1",
  name: "Database",
  can_edit: true,
  engine: "PostgreSQL",
  dialect: "postgresql",
  capabilities: {
    connection_test: true,
    schema_inspection: true,
    data_preview: true,
    migration_execution: true,
    atomic_migrations: true,
    schema_diff: true,
    explain: true,
    explain_analyze: false,
  },
  current_version: 0,
  version_track: [],
  track_earlier: 0,
};
const attempt = {
  id: "attempt-1",
  sequence: 1,
  database_id: "db-1",
  migration_id: "migration-1",
  migration_title: "Create sample",
  draft_revision: 1,
  candidate_version: 1,
  applied_version: 0,
  sql: "CREATE TABLE sample (id int);",
  sql_hash: "abcdef0123456789",
  dialect: "postgresql",
  statement_count: 1,
  executor_name: "editor",
  status: "failed",
  error: "simulated failure",
  failed_statement: 1,
  started_at: "2026-11-05T10:00:00Z",
  finished_at: "2026-11-05T10:00:01Z",
  reconciled_at: null,
};
const envelope = (data: unknown, status = 200) => ({
  status,
  contentType: "application/json",
  body: JSON.stringify({ data }),
});
const migrationURL = "/databases/db-1/migrations/migration-1";
const draft = (revision: number, sql = attempt.sql) => ({
  id: "migration-1",
  database_id: "db-1",
  title: "Create sample",
  sql,
  sql_hash: "abcdef0123456789",
  dialect: "postgresql",
  revision,
  applied_version: 0,
  last_status: "",
  created_by_name: "editor",
  created_at: "2026-11-05T09:00:00Z",
  updated_by_name: "editor",
  updated_at: "2026-11-05T09:00:00Z",
});

async function mockMigration(
  page: import("@playwright/test").Page,
  getDraft: () => ReturnType<typeof draft>,
) {
  await page.route("**/api/v1/auth/session", (route) => route.fulfill(envelope(session)));
  await page.route("**/api/v1/databases/db-1/migrations", (route) =>
    route.fulfill(
      envelope({ workspace, current_version: 0, drafts: [getDraft()], attempts: [attempt] }),
    ),
  );
  await page.route("**/api/v1/databases/db-1/migrations/migration-1", (route) =>
    route.fulfill(
      envelope({
        workspace,
        current_version: 0,
        draft: getDraft(),
        attempts: [attempt],
        blocked: "",
        blocked_by: null,
      }),
    ),
  );
  await page.route("**/api/v1/databases/db-1/migrations/migration-1/attempts/attempt-1", (route) =>
    route.fulfill(envelope(attempt)),
  );
}

test("direct attempt route and migration SQL details link render immutable executed SQL", async ({
  page,
}) => {
  await mockMigration(page, () => draft(1));
  await page.goto(`${migrationURL}/attempts/attempt-1`);
  await expect(page.getByRole("heading", { name: "Candidate version 1" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Executed SQL (immutable)" })).toBeVisible();
  await expect(page.getByText(attempt.sql, { exact: true })).toBeVisible();
  await expect(page.getByLabel("SQL", { exact: true })).toHaveCount(0);
  await page.getByRole("link", { name: "Back to Create sample" }).click();
  await expect(page.getByRole("heading", { name: "Create sample" })).toBeVisible();
  await page.getByRole("link", { name: "SQL and details" }).click();
  await expect(page.getByRole("heading", { name: "Executed SQL (immutable)" })).toBeVisible();
});

test("a revision refresh never submits local SQL against the newer server revision", async ({
  page,
}) => {
  let revision = 1;
  let detailReads = 0;
  let saveBody: Record<string, unknown> | undefined;
  await mockMigration(page, () => draft(revision));
  await page.route(`**/api/v1${migrationURL}`, async (route) => {
    if (route.request().method() === "PATCH") {
      saveBody = route.request().postDataJSON();
      return route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "revision_conflict",
            message: "Draft revision changed. Review the latest version before saving.",
          },
        }),
      });
    }
    detailReads++;
    return route.fulfill(
      envelope({
        workspace,
        current_version: 0,
        draft: draft(revision),
        attempts: [attempt],
        blocked: "",
        blocked_by: null,
      }),
    );
  });
  await page.route(`**/api/v1${migrationURL}/execute`, (route) =>
    route.fulfill(
      envelope({
        ...attempt,
        status: "succeeded",
        applied_version: 1,
        error: "",
        finished_at: "2026-11-05T10:00:01Z",
      }),
    ),
  );
  await page.goto(migrationURL);
  const sql = page.getByLabel("SQL", { exact: true });
  await sql.fill("CREATE TABLE local_edit (id int);");
  revision = 2;
  await page.getByRole("button", { name: "Execute as v1" }).click();
  await expect(page.getByRole("status")).toContainText("Applied as v1.");
  await expect.poll(() => detailReads).toBeGreaterThan(1);
  await expect(page.getByText("Draft revision 2", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Save revision" }).click();
  expect(saveBody).toMatchObject({
    sql: "CREATE TABLE local_edit (id int);",
    expected_revision: 1,
  });
  await expect(sql).toHaveValue("CREATE TABLE local_edit (id int);");
  await expect(page.getByRole("alert")).toContainText("Review the latest version");
  await page.getByRole("button", { name: "Reload latest revision" }).last().click();
  await expect(sql).toHaveValue(attempt.sql);
  await expect(page.getByRole("button", { name: "Save revision 3" })).toBeVisible();
});

test("failed plan refresh keeps the old result visibly stale with the error", async ({ page }) => {
  await mockMigration(page, () => draft(1));
  let calls = 0;
  await page.route("**/api/v1/databases/db-1/explain", (route) =>
    ++calls === 1
      ? route.fulfill(
          envelope({
            dialect: "postgresql",
            mode: "explain",
            sql_hash: "hash",
            warnings: [],
            root: { type: "Seq Scan", startup_cost: 0, total_cost: 1, plan_rows: 1, children: [] },
          }),
        )
      : route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "unavailable", message: "Plan service unavailable." },
          }),
        }),
  );
  await page.goto(migrationURL);
  await page.getByRole("button", { name: "Show plan" }).click();
  await expect(page.getByText("Seq Scan", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Show plan" }).click();
  await expect(page.getByRole("alert")).toContainText("Plan service unavailable.");
  await expect(page.getByRole("status")).toContainText("SQL changed after this plan");
  await expect(page.getByText("Seq Scan", { exact: false })).toBeVisible();
});
