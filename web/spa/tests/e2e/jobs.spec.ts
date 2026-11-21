import { expect, test, type Page } from "@playwright/test";

const session = (permission: "view" | "edit") => ({
  user: { id: "u1", username: "editor", is_platform_admin: false },
  csrf_token: "csrf",
  expires_at: "2099-01-01T00:00:00Z",
  databases: [{ id: "db-1", name: "Database", permission }],
});
const workspace = (canEdit: boolean) => ({
  id: "db-1",
  name: "Database",
  can_edit: canEdit,
  engine: "PostgreSQL",
  current_version: 1,
  version_track: [],
  track_earlier: 0,
});
const draft = {
  id: "migration-1",
  database_id: "db-1",
  title: "Archive accounts",
  sql: "UPDATE accounts SET status = 'archived';",
  sql_down: "UPDATE accounts SET status = 'active';",
  irreversible: false,
  irreversible_reason: "",
  definition_hash: "definition-1",
  executed: true,
  sql_hash: "abcdef0123456789",
  dialect: "postgresql",
  revision: 3,
  applied_version: 0,
  last_status: "",
  created_by_name: "editor",
  created_at: "2026-11-20T09:00:00Z",
  updated_by_name: "editor",
  updated_at: "2026-11-20T09:00:00Z",
};
const job = (over: Record<string, unknown> = {}) => ({
  job_id: "job-aaaaaaaaaaaa1111",
  database_id: "db-1",
  migration_id: "migration-1",
  request_id: "request-1",
  execution_id: "execution-1",
  operation: "up",
  status: "queued",
  effect: "none",
  terminal: false,
  attempts: 0,
  max_attempts: 3,
  committed_batches: 0,
  committed_rows: 0,
  total_rows: null,
  recovery: [],
  queued_at: "2026-11-20T10:00:00Z",
  updated_at: "2026-11-20T10:00:00Z",
  ...over,
});
const partial = job({
  status: "blocked",
  effect: "partial",
  terminal: true,
  attempts: 1,
  committed_batches: 2,
  committed_rows: 200,
  total_rows: 500,
  error: "The database rejected the request (SQLSTATE P0001). 2 batches (200 rows) stay committed.",
  recovery: ["resume", "compensate"],
  started_at: "2026-11-20T10:00:01Z",
  finished_at: "2026-11-20T10:00:03Z",
});
const envelope = (data: unknown, status = 200) => ({
  status,
  contentType: "application/json",
  body: JSON.stringify({ data }),
});
const failure = (status: number, code: string, message: string) => ({
  status,
  contentType: "application/json",
  body: JSON.stringify({ error: { code, message, fields: {} } }),
});

async function mock(page: Page, opts: { canEdit?: boolean; jobs?: unknown[] } = {}) {
  const canEdit = opts.canEdit ?? true;
  await page.route("**/api/v1/auth/session", (r) =>
    r.fulfill(envelope(session(canEdit ? "edit" : "view"))),
  );
  await page.route("**/api/v1/databases/db-1/migrations", (r) =>
    r.fulfill(
      envelope({
        workspace: workspace(canEdit),
        current_version: 1,
        drafts: [draft],
        attempts: [],
        committed: [],
        head: { active: [], pending: [] },
      }),
    ),
  );
  await page.route("**/api/v1/databases/db-1/jobs", (r) =>
    r.fulfill(envelope({ jobs: opts.jobs ?? [] })),
  );
}

const noPageOverflow = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);

test("an empty job list explains how jobs are created", async ({ page }) => {
  await mock(page);
  await page.goto("/databases/db-1/jobs");
  await expect(page.getByRole("heading", { name: "Jobs", exact: true })).toBeVisible();
  await expect(page.getByText("No jobs yet")).toBeVisible();
  await expect(page.getByRole("link", { name: "Jobs", exact: true })).toHaveAttribute(
    "aria-current",
    "page",
  );
});

test("the list polls active jobs and stops once they settle", async ({ page }) => {
  let reads = 0;
  let first = 0;
  await mock(page);
  await page.route("**/api/v1/databases/db-1/jobs", (r) => {
    reads++;
    first ||= Date.now();
    // The job finishes after the page has polled for a while.
    const settled = Date.now() - first > 1500;
    return r.fulfill(
      envelope({
        jobs: [
          settled
            ? job({ status: "succeeded", effect: "committed", terminal: true, attempts: 1 })
            : job({ status: "running", attempts: 1 }),
        ],
      }),
    );
  });
  await page.goto("/databases/db-1/jobs");
  await expect(page.getByText("Running", { exact: true })).toBeVisible();
  await expect(page.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 6000 });
  const settledReads = reads;
  await page.waitForTimeout(2500);
  expect(reads).toBe(settledReads);
  await expect(page.getByRole("cell", { name: "Archive accounts" })).toBeVisible();
  expect(await noPageOverflow(page)).toBe(true);
});

test("a queued job says it was accepted, not that it succeeded", async ({ page }) => {
  await mock(page);
  await page.route("**/api/v1/databases/db-1/jobs/job-1", (r) =>
    r.fulfill(envelope(job({ job_id: "job-1" }))),
  );
  await page.goto("/databases/db-1/jobs/job-1");
  await expect(
    page.getByText("Accepted and waiting for a worker. Nothing has run yet."),
  ).toBeVisible();
  await expect(page.getByText("No target changes")).toBeVisible();
  await expect(page.getByText("Succeeded")).toHaveCount(0);
});

test("a partial batch job shows committed progress and requests recovery through review", async ({
  page,
}) => {
  await mock(page);
  await page.route("**/api/v1/databases/db-1/jobs/job-1", (r) => r.fulfill(envelope(partial)));
  let body: Record<string, unknown> | undefined;
  await page.route("**/api/v1/databases/db-1/migrations/migration-1/requests", (r) => {
    body = r.request().postDataJSON();
    return r.fulfill(envelope({ id: "request-2" }, 201));
  });
  await page.route("**/api/v1/databases/db-1/migrations/migration-1", (r) =>
    r.fulfill(failure(503, "unavailable", "stub")),
  );
  await page.goto("/databases/db-1/jobs/job-1");
  await expect(page.getByText("Partially applied")).toBeVisible();
  await expect(page.getByText("2 batches · 200 of 500 rows")).toBeVisible();
  await expect(page.getByRole("progressbar", { name: "Committed rows" })).toHaveAttribute(
    "aria-valuenow",
    "40",
  );
  await expect(page.getByRole("alert").first()).toContainText("stay committed");
  expect(await noPageOverflow(page)).toBe(true);
  await page.getByRole("button", { name: "Request resume" }).click();
  await expect
    .poll(() => body)
    .toEqual({
      operation: "resume",
      expected_revision: 3,
      expected_definition_hash: "definition-1",
      expected_version: 1,
      recovers_execution_id: "execution-1",
    });
  await expect(page).toHaveURL(/\/databases\/db-1\/migrations\/migration-1$/);
});

test("viewers see recovery state but cannot request it", async ({ page }) => {
  await mock(page, { canEdit: false });
  await page.route("**/api/v1/databases/db-1/jobs/job-1", (r) => r.fulfill(envelope(partial)));
  await page.goto("/databases/db-1/jobs/job-1");
  await expect(page.getByText("Ask an editor of this database to request recovery.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Request resume" })).toHaveCount(0);
});

test("a rejected recovery request is explained without leaving the job", async ({ page }) => {
  await mock(page);
  await page.route("**/api/v1/databases/db-1/jobs/job-1", (r) => r.fulfill(envelope(partial)));
  await page.route("**/api/v1/databases/db-1/migrations/migration-1/requests", (r) =>
    r.fulfill(failure(409, "conflict", "A recovery request for this execution already exists.")),
  );
  await page.goto("/databases/db-1/jobs/job-1");
  await page.getByRole("button", { name: "Request compensation" }).click();
  await expect(
    page.getByText("A recovery request for this execution already exists."),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/jobs\/job-1$/);
});

test("a job waiting behind a blocked database links to the blocker", async ({ page }) => {
  await mock(page);
  await page.route("**/api/v1/databases/db-1/jobs/job-2", (r) =>
    r.fulfill(envelope(job({ job_id: "job-2", blocked_by: "job-blocker-0001" }))),
  );
  await page.goto("/databases/db-1/jobs/job-2");
  await expect(
    page.getByText("this database is blocked until the earlier job is recovered"),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: "job-blocker-" })).toHaveAttribute(
    "href",
    "/databases/db-1/jobs/job-blocker-0001",
  );
});

test("a job of another database or without access is not shown", async ({ page }) => {
  await mock(page);
  await page.route("**/api/v1/databases/db-1/jobs/foreign", (r) =>
    r.fulfill(failure(404, "not_found", "Job not found for this database.")),
  );
  await page.goto("/databases/db-1/jobs/foreign");
  await expect(page.getByRole("alert")).toContainText("Job not found for this database.");
  await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(0);
});

test("polling a job without permission shows the denial", async ({ page }) => {
  await mock(page);
  await page.route("**/api/v1/databases/db-1/jobs/job-1", (r) =>
    r.fulfill(
      failure(403, "permission_denied", "You do not have permission to perform this action."),
    ),
  );
  await page.goto("/databases/db-1/jobs/job-1");
  await expect(page.getByRole("alert")).toContainText("You do not have permission");
});
