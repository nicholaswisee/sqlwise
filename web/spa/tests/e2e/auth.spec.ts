import { expect, test } from "@playwright/test";

test("login, account refresh and logout stay in one document", async ({ page }) => {
  const documents: string[] = [];
  page.on("request", (request) => {
    if (request.resourceType() === "document") documents.push(request.url());
  });
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Sign in", exact: true })).toBeVisible();
  await page.evaluate(() => Object.assign(window, { sqlwiseDocumentMarker: "same document" }));
  await page
    .getByLabel("Username", { exact: true })
    .fill(process.env.SQLWISE_E2E_USERNAME ?? "admin");
  await page.getByLabel("Password", { exact: true }).fill("incorrect-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText("Unable to sign in with those credentials.");
  await expect(page.getByLabel("Password", { exact: true })).toHaveValue("");
  await page
    .getByLabel("Password", { exact: true })
    .fill(process.env.SQLWISE_E2E_PASSWORD ?? "SqlwiseAdminLocal2026!");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/account$/);
  await expect(page.getByRole("heading", { name: "Your account", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Refresh session", exact: true }).click();
  await expect(page.getByText("Session active", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("heading", { name: "Sign in", exact: true })).toBeVisible();
  expect(await page.evaluate(() => Reflect.get(window, "sqlwiseDocumentMarker"))).toBe(
    "same document",
  );
  expect(documents).toHaveLength(1);
});

test("anonymous protected deep links go to the SPA login", async ({ page }) => {
  await page.goto("/account");
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("heading", { name: "Sign in", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
