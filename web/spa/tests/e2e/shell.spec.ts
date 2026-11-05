import { expect, test } from "@playwright/test";

test("public home stays public and fits the viewport", async ({ page }) => {
  const apiRequests: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/api/v1/")) apiRequests.push(request.url());
  });
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "sqlwise", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Sign in", exact: true })).toHaveCount(2);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect(apiRequests).toEqual([]);
  expect(errors).toEqual([]);
});
