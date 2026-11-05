import { expect, test, type Page } from "@playwright/test";

function session(id = "one") {
  return {
    user: { id, username: id, is_platform_admin: false },
    csrf_token: `csrf-${id}`,
    expires_at: "2099-01-01",
    databases: [{ id: "db-1", name: `Database for ${id}`, permission: "view" }],
    needs_refresh: false,
  };
}
const expired = { error: { code: "session_expired", message: "Expired" } };
async function challenge(page: Page) {
  await page.route("**/api/v1/auth/login-challenge", (route) =>
    route.fulfill({ json: { data: { csrf_token: "challenge", expires_at: "2099-01-01" } } }),
  );
}

test("pending gate hides private UI, network failure offers retry rather than redirect", async ({
  page,
}) => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let failing = true;
  await page.route("**/api/v1/auth/session", async (route) => {
    if (failing) {
      await pending;
      await route.abort("failed");
    } else await route.fulfill({ json: { data: session() } });
  });
  await page.goto("/account");
  await expect(page.getByRole("status")).toHaveText("Checking your session…");
  await expect(page.getByRole("heading", { name: "Your account", exact: true })).toHaveCount(0);
  release();
  await expect(page.getByRole("alert")).toContainText("Unable to check your session");
  await expect(page).toHaveURL(/\/account$/);
  failing = false;
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Your account", exact: true })).toBeVisible();
});

test("session401 unmounts dirty private DOM and never checks the surviving cookie again", async ({
  page,
}) => {
  let calls = 0;
  let invalid = false;
  await page.route("**/api/v1/auth/session", (route) => {
    calls++;
    return invalid
      ? route.fulfill({ status: 401, json: expired })
      : route.fulfill({ json: { data: session() } });
  });
  await challenge(page);
  await page.goto("/account");
  await expect(page.getByRole("heading", { name: "Your account", exact: true })).toBeVisible();
  const initialChecks = calls;
  invalid = true;
  await page.locator("main").evaluate((main) => {
    const input = document.createElement("input");
    input.id = "dirty-private";
    input.value = "unsaved private SQL";
    main.append(input);
  });
  await page.getByRole("button", { name: "Refresh session", exact: true }).click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.locator("#dirty-private")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeEnabled();
  expect(calls).toBe(initialChecks + 1);
});

test("same-URL identity replacement resets the private subtree but normal refresh preserves it", async ({
  page,
}) => {
  let identity = "one";
  await page.route("**/api/v1/auth/session", (route) =>
    route.fulfill({ json: { data: session(identity) } }),
  );
  await page.goto("/account");
  await expect(page.getByText("Database for one", { exact: true })).toBeVisible();
  await page.locator("main").evaluate((main) => {
    const input = document.createElement("input");
    input.id = "dirty-private";
    input.value = "draft";
    main.append(input);
  });
  await page.getByRole("button", { name: "Refresh session", exact: true }).click();
  await expect(page.getByRole("button", { name: "Refresh session", exact: true })).toBeEnabled();
  await expect(page.locator("#dirty-private")).toHaveValue("draft");
  identity = "two";
  await page.getByRole("button", { name: "Refresh session", exact: true }).click();
  await expect(page.getByText("Database for two", { exact: true })).toBeVisible();
  await expect(page.locator("#dirty-private")).toHaveCount(0);
  await expect(page).toHaveURL(/\/account$/);
});

test("failed logout hides private UI and reports unconfirmed server signout without autologin", async ({
  page,
}) => {
  let checks = 0;
  await page.route("**/api/v1/auth/session", (route) => {
    checks++;
    return route.fulfill({ json: { data: session() } });
  });
  await page.route("**/api/v1/auth/logout", (route) =>
    route.fulfill({
      status: 503,
      json: { error: { code: "unavailable", message: "Unavailable" } },
    }),
  );
  await challenge(page);
  await page.goto("/account");
  await expect(page.getByRole("heading", { name: "Your account", exact: true })).toBeVisible();
  const initialChecks = checks;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("alert")).toContainText("server signout is unconfirmed");
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeEnabled();
  await expect(page.getByText("Database for one", { exact: true })).toHaveCount(0);
  expect(checks).toBe(initialChecks);
});

test("an initial active cookie redirects login to account", async ({ page }) => {
  await page.route("**/api/v1/auth/session", (route) =>
    route.fulfill({ json: { data: session() } }),
  );
  await page.goto("/login");
  await expect(page).toHaveURL(/\/account$/);
  await expect(page.getByRole("heading", { name: "Your account", exact: true })).toBeVisible();
});

test("expired CSRF refreshes challenge, clears password, and completes partial login with full session", async ({
  page,
}) => {
  let signedIn = false;
  let challenges = 0;
  let logins = 0;
  await page.route("**/api/v1/auth/session", (route) =>
    signedIn
      ? route.fulfill({ json: { data: session() } })
      : route.fulfill({ status: 401, json: expired }),
  );
  await page.route("**/api/v1/auth/login-challenge", (route) => {
    challenges++;
    return route.fulfill({
      json: { data: { csrf_token: `challenge-${challenges}`, expires_at: "2099-01-01" } },
    });
  });
  await page.route("**/api/v1/auth/login", (route) => {
    if (++logins === 1)
      return route.fulfill({
        status: 403,
        json: { error: { code: "csrf_invalid", message: "Request verification failed." } },
      });
    expect(route.request().headers()["x-csrf-token"]).toBe("challenge-2");
    signedIn = true;
    const full = session();
    return route.fulfill({
      json: { data: { user: full.user, csrf_token: full.csrf_token, expires_at: full.expires_at } },
    });
  });
  await page.goto("/login");
  await page.getByLabel("Username", { exact: true }).fill("one");
  await page.getByLabel("Password", { exact: true }).fill("password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText("Request verification failed.");
  await expect(page.getByLabel("Password", { exact: true })).toHaveValue("");
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeEnabled();
  await page.getByLabel("Password", { exact: true }).fill("password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByText("Database for one", { exact: true })).toBeVisible();
  expect(challenges).toBe(2);
});

test("failed challenge can be retried explicitly", async ({ page }) => {
  await page.route("**/api/v1/auth/session", (route) =>
    route.fulfill({ status: 401, json: expired }),
  );
  let calls = 0;
  await page.route("**/api/v1/auth/login-challenge", (route) =>
    ++calls === 1
      ? route.abort("failed")
      : route.fulfill({ json: { data: { csrf_token: "challenge", expires_at: "2099-01-01" } } }),
  );
  await page.goto("/login");
  await expect(page.getByRole("alert")).toContainText("Unable to prepare sign in");
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeEnabled();
});

test("real StrictMode replay settles the gate without stale errors", async ({ page }) => {
  await page.route("**/api/v1/auth/session", (route) =>
    route.fulfill({ json: { data: session() } }),
  );
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "sqlwise", exact: true })).toBeVisible();
  await page.evaluate(async () => {
    const path = "/tests/auth-gate-harness.tsx";
    const harness = await import(/* @vite-ignore */ path);
    Reflect.set(window, "authGateTest", await harness.mount());
  });
  await expect(
    page.getByRole("heading", { name: "Private form for one", exact: true }),
  ).toBeVisible();
  await page.getByLabel("Private draft", { exact: true }).fill("unsaved SQL");
  await page.evaluate(() => Reflect.get(window, "authGateTest").clear());
  await expect(page.getByLabel("Private draft", { exact: true })).toHaveCount(0);
  expect(errors).toEqual([]);
});

test("intent preloading protected routes does not bootstrap auth", async ({ page }) => {
  const requests: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/api/v1/")) requests.push(request.url());
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "sqlwise", exact: true })).toBeVisible();
  await page.evaluate(async () => {
    const path = "/tests/auth-gate-harness.tsx";
    const harness = await import(/* @vite-ignore */ path);
    Reflect.set(window, "authGateTest", await harness.mount(true));
  });
  await page.getByRole("link", { name: "Private account", exact: true }).hover();
  await page.waitForLoadState("networkidle");
  expect(requests).toEqual([]);
});
