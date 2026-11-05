import { expect, test, type Page } from "@playwright/test";

const session = {
  user: { id: "admin-1", username: "admin", is_platform_admin: true },
  csrf_token: "csrf-test",
  expires_at: "2099-01-01T00:00:00Z",
  databases: [],
};
const snapshot = {
  users: [{ id: "user-1", username: "member", active: true, is_platform_admin: false }],
  groups: [],
  databases: [],
  memberships: [],
  grants: [],
};
const envelope = (data: unknown, status = 200) => ({
  status,
  contentType: "application/json",
  body: JSON.stringify({ data }),
});
const apiError = (code: string, message: string, status: number) => ({
  status,
  contentType: "application/json",
  body: JSON.stringify({ error: { code, message } }),
});

async function mockSession(page: Page) {
  await page.route("**/api/v1/auth/session", (route) => route.fulfill(envelope(session)));
}

test("admin user mutations send only the selected action and clear submitted password inputs", async ({
  page,
}) => {
  await mockSession(page);
  await page.route("**/api/v1/admin/users", (route) => route.fulfill(envelope(snapshot)));
  let passwordBody = "";
  await page.route("**/api/v1/admin/users/user-1", async (route) => {
    if (route.request().method() === "GET") return route.fulfill(envelope(snapshot));
    const body = route.request().postDataJSON();
    passwordBody = body.password;
    expect(Object.keys(body)).toEqual(["password"]);
    return route.fulfill(envelope(snapshot));
  });

  await page.goto("/admin/users");
  await expect(page.getByRole("heading", { name: "User accounts" })).toBeVisible();
  const password = page.getByLabel("Set new password for member");
  await password.fill("a-long-temporary-password");
  await page.getByRole("button", { name: "Reset password" }).click();
  await expect(page.getByText("Changes saved successfully.")).toBeVisible();
  expect(passwordBody).toBe("a-long-temporary-password");
  await expect(password).toHaveValue("");
  await expect(page.getByRole("link", { name: "Groups", exact: true })).toHaveAttribute(
    "href",
    "/admin/groups",
  );
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
});

test("stale authorization requires explicit refresh and does not resubmit a rejected mutation", async ({
  page,
}) => {
  await mockSession(page);
  let snapshotReads = 0;
  let writes = 0;
  await page.route("**/api/v1/admin/users", (route) => {
    snapshotReads++;
    return route.fulfill(envelope(snapshot));
  });
  await page.route("**/api/v1/admin/users/user-1", async (route) => {
    if (route.request().method() === "GET") return route.fulfill(envelope(snapshot));
    writes++;
    return route.fulfill(
      apiError("authorization_stale", "Your permissions changed. Refresh and try again.", 409),
    );
  });

  await page.goto("/admin/users");
  const name = page.getByLabel("Update username for member");
  await name.fill("unsaved rename");
  await page.getByRole("button", { name: "Save name" }).click();
  await expect(page.getByText("Your permissions changed. Refresh and try again.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Refresh session and data" })).toBeVisible();
  expect(writes).toBe(1);
  await page.getByRole("button", { name: "Refresh session and data" }).click();
  await expect.poll(() => snapshotReads).toBeGreaterThan(1);
  await expect(name).toHaveValue("unsaved rename");
  expect(writes).toBe(1);
});
