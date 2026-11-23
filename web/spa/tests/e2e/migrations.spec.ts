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
  operation: "up",
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
  sql_down: "DROP TABLE sample;",
  irreversible: false,
  irreversible_reason: "",
  definition_hash: `definition-${revision}`,
  executed: false,
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

const head = { active: [], pending: [] };
const detail = (d: ReturnType<typeof draft>, requests: unknown[] = []) => ({
  workspace,
  current_version: 0,
  draft: d,
  attempts: [attempt],
  requests,
  eligible: ["up"],
  head,
  blocked: "",
  blocked_by: null,
});

async function mockMigration(
  page: import("@playwright/test").Page,
  getDraft: () => ReturnType<typeof draft>,
) {
  await page.route("**/api/v1/auth/session", (route) => route.fulfill(envelope(session)));
  await page.route("**/api/v1/databases/db-1/migrations", (route) =>
    route.fulfill(
      envelope({
        workspace,
        current_version: 0,
        drafts: [getDraft()],
        attempts: [attempt],
        committed: [],
        head,
      }),
    ),
  );
  await page.route("**/api/v1/databases/db-1/migrations/migration-1", (route) =>
    route.fulfill(envelope(detail(getDraft()))),
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

test("populated execution history scrolls inside its table without widening the page", async ({
  page,
}) => {
  await mockMigration(page, () => draft(1));
  await page.goto("/databases/db-1/migrations");
  await expect(page.getByRole("heading", { name: "Execution history", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "SQL and details", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
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
    return route.fulfill(envelope(detail(draft(revision))));
  });
  let requestBody: Record<string, unknown> | undefined;
  await page.route(`**/api/v1${migrationURL}/requests`, (route) => {
    requestBody = route.request().postDataJSON();
    return route.fulfill(envelope({}, 201));
  });
  await page.goto(migrationURL);
  const sql = page.getByLabel("SQL", { exact: true });
  await sql.fill("CREATE TABLE local_edit (id int);");
  revision = 2;
  await page.getByRole("button", { name: "Request apply as v1" }).click();
  // A request pins the saved definition, never the unsaved local SQL.
  await expect
    .poll(() => requestBody)
    .toEqual({
      operation: "up",
      expected_revision: 1,
      expected_definition_hash: "definition-1",
      expected_version: 0,
    });
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
  await sql.fill("CREATE TABLE second_edit (id int);");
  await page.getByRole("button", { name: "Save revision 3" }).click();
  expect(saveBody).toMatchObject({
    sql: "CREATE TABLE second_edit (id int);",
    expected_revision: 2,
  });
  await expect(sql).toHaveValue("CREATE TABLE second_edit (id int);");
});

test("editing a batch migration keeps its template and explains checkpointed commits", async ({
  page,
}) => {
  const batch = {
    schema: "public",
    table: "customers",
    key: "id",
    column: "status",
    value: "'active'",
    batch_size: 250,
  };
  const batchedDraft = { ...draft(1), batch };
  await mockMigration(page, () => batchedDraft);
  let saveBody: Record<string, unknown> | undefined;
  await page.route(`**/api/v1${migrationURL}`, (route) => {
    if (route.request().method() === "PATCH") {
      saveBody = route.request().postDataJSON();
      return route.fulfill(envelope({ ...batchedDraft, title: "Update customers" }));
    }
    return route.fulfill(envelope(detail(batchedDraft)));
  });

  await page.goto(migrationURL);
  await expect(page.getByLabel("SQL", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("SQL down", { exact: true })).toHaveCount(0);
  await expect(page.getByText("Batch key:", { exact: false })).toBeVisible();
  await expect(page.getByText("Each batch commits independently", { exact: false })).toBeVisible();
  await page.getByLabel("Title", { exact: true }).fill("Update customers");
  await page.getByRole("button", { name: "Save revision" }).click();

  await expect
    .poll(() => saveBody)
    .toMatchObject({
      title: "Update customers",
      sql: batchedDraft.sql,
      batch,
      expected_revision: 1,
    });
});

test("ordinary migrations retain the single-transaction review explanation", async ({ page }) => {
  await mockMigration(page, () => draft(1));
  await page.goto(migrationURL);
  await expect(
    page.getByText("approved requests run in the background in one transaction."),
  ).toBeVisible();
});

test("failed plan refresh warns about the retained plan without claiming unchanged SQL changed", async ({
  page,
}) => {
  await mockMigration(page, () => draft(1));
  let calls = 0;
  await page.route("**/api/v1/databases/db-1/explain", (route) =>
    ++calls === 2
      ? route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "unavailable", message: "Plan service unavailable." },
          }),
        })
      : route.fulfill(
          envelope({
            dialect: "postgresql",
            mode: "explain",
            sql_hash: "hash",
            warnings: [],
            root: { type: "Seq Scan", startup_cost: 0, total_cost: 1, plan_rows: 1, children: [] },
          }),
        ),
  );
  await page.goto(migrationURL);
  await page.getByRole("button", { name: "Show plan" }).click();
  await expect(page.getByText("Seq Scan", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Show plan" }).click();
  await expect(page.getByRole("alert")).toContainText("Plan service unavailable.");
  await expect(page.getByRole("status")).toContainText(
    "The latest plan request failed. Showing the previous plan; show the plan again to refresh it.",
  );
  await expect(page.getByRole("status")).not.toContainText("SQL changed after this plan");
  await expect(page.getByText("Seq Scan", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Show plan" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByRole("status")).toHaveCount(0);
  await expect(page.getByText("Seq Scan", { exact: false })).toBeVisible();
});

test("failed plan refresh after SQL edits retains the SQL-changed warning", async ({ page }) => {
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
  await page.getByLabel("SQL", { exact: true }).fill("CREATE TABLE edited (id int);");
  await page.getByRole("button", { name: "Show plan" }).click();
  await expect(page.getByRole("alert")).toContainText("Plan service unavailable.");
  await expect(page.getByRole("status")).toContainText("SQL changed after this plan");
  await expect(page.getByRole("status")).not.toContainText("latest plan request failed");
});

test("an approved request is enqueued, polled and reported without blocking on the SQL", async ({
  page,
}) => {
  const approval = {
    group_id: "group-edit",
    group_name: "Editors",
    permission: "edit",
    leader_name: "editor",
    leader_active: true,
    decision: "approved",
    decided_at: "2026-11-05T10:00:00Z",
    can_decide: false,
  };
  const request = {
    id: "request-1",
    migration_id: "migration-1",
    migration_title: "Create sample",
    operation: "up",
    definition_revision: 1,
    definition_hash: "definition-1",
    expected_version: 0,
    requested_by_name: "editor",
    status: "approved",
    review_round: 1,
    approvals: [approval],
    execution: null,
    created_at: "2026-11-05T09:30:00Z",
    updated_at: "2026-11-05T10:00:00Z",
  };
  const executed = {
    ...attempt,
    request_id: "request-1",
    status: "succeeded",
    applied_version: 1,
    error: "",
  };
  let queued = false;
  let executeBody: Record<string, unknown> | undefined;
  let jobReads = 0;
  await mockMigration(page, () => draft(1));
  await page.route(`**/api/v1${migrationURL}`, (route) =>
    route.fulfill(
      envelope(
        detail(draft(1), [
          queued ? { ...request, status: "queued", execution: executed } : request,
        ]),
      ),
    ),
  );
  await page.route(`**/api/v1${migrationURL}/execute`, (route) => {
    executeBody = route.request().postDataJSON();
    queued = true;
    return route.fulfill(
      envelope(
        { job_id: "job-1", request_id: "request-1", execution_id: "attempt-1", status: "queued" },
        202,
      ),
    );
  });
  await page.route("**/api/v1/databases/db-1/jobs/job-1", (route) =>
    route.fulfill(
      envelope({
        job_id: "job-1",
        request_id: "request-1",
        execution_id: "attempt-1",
        status: ++jobReads < 2 ? "running" : "succeeded",
      }),
    ),
  );
  await page.route("**/api/v1/databases/db-1/requests/request-1", (route) =>
    route.fulfill(envelope({ ...request, status: "queued", execution: executed })),
  );
  await page.goto(migrationURL);
  await expect(page.getByText("Approved 2026-11-05 10:00:00 UTC")).toBeVisible();
  await page.getByRole("button", { name: "Execute apply as v1" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Applied as v1." })).toBeVisible();
  expect(jobReads).toBeGreaterThan(1);
  expect(executeBody?.request_id).toBe("request-1");
  expect(typeof executeBody?.idempotency_key).toBe("string");
  await expect(page.getByText("Queued for execution", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "View execution" })).toBeVisible();
});

test("committed history can hide rollback pairs through the server filter", async ({ page }) => {
  const up = { ...attempt, status: "succeeded", applied_version: 1, error: "" };
  const down = { ...up, id: "attempt-2", operation: "rollback", applied_version: 2 };
  let filtered = false;
  await mockMigration(page, () => draft(1));
  await page.route("**/api/v1/databases/db-1/migrations?*", (route) => {
    filtered = new URL(route.request().url()).searchParams.get("hide_rollback") === "true";
    return route.fulfill(
      envelope({
        workspace,
        current_version: 2,
        drafts: [draft(1)],
        attempts: [down, up],
        committed: [],
        head,
      }),
    );
  });
  await page.route("**/api/v1/databases/db-1/migrations", (route) =>
    route.fulfill(
      envelope({
        workspace,
        current_version: 2,
        drafts: [draft(1)],
        attempts: [down, up],
        committed: [up, down],
        head,
      }),
    ),
  );
  await page.goto("/databases/db-1/migrations");
  const committed = page.getByRole("region", { name: "Committed history" });
  await expect(committed.getByText("Roll back", { exact: true })).toBeVisible();
  await page.getByLabel("Hide rollbacks").check();
  await expect(committed).toContainText("Every committed migration was rolled back.");
  expect(filtered).toBe(true);
  await expect(page.getByRole("region", { name: "Execution history" })).toContainText("Roll back");
});

test("a view-only leader can approve for their group but cannot request, submit or execute", async ({
  page,
}) => {
  const viewer = { ...workspace, can_edit: false };
  const pending = {
    id: "request-1",
    migration_id: "migration-1",
    migration_title: "Create sample",
    operation: "up",
    definition_revision: 1,
    definition_hash: "definition-1",
    expected_version: 0,
    requested_by_name: "editor",
    status: "pending_review",
    review_round: 1,
    approvals: [
      {
        group_id: "group-view",
        group_name: "Auditors",
        permission: "view",
        leader_name: "auditor",
        leader_active: true,
        decision: "",
        can_decide: true,
      },
      {
        group_id: "group-edit",
        group_name: "Editors",
        permission: "edit",
        leader_name: "editor",
        leader_active: true,
        decision: "",
        can_decide: false,
      },
    ],
    execution: null,
    created_at: "2026-11-05T09:30:00Z",
    updated_at: "2026-11-05T09:30:00Z",
  };
  let decision: Record<string, unknown> | undefined;
  await mockMigration(page, () => draft(1));
  await page.route(`**/api/v1${migrationURL}`, (route) =>
    route.fulfill(envelope({ ...detail(draft(1), [pending]), workspace: viewer })),
  );
  await page.route("**/api/v1/databases/db-1/requests/request-1/approve", (route) => {
    decision = route.request().postDataJSON();
    return route.fulfill(envelope(pending));
  });
  await page.goto(migrationURL);
  await expect(page.getByText("Waiting for review", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Request / })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Submit for review/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Execute / })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Approve for Editors" })).toHaveCount(0);
  await expect(page.getByText("Waiting for the leader", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByRole("button", { name: "Approve for Auditors" }).click();
  await expect.poll(() => decision).toEqual({ group_id: "group-view" });
});
