import { test, expect, type Page } from "@playwright/test";

const PASSWORD = process.env.NETINV_ADMIN_PASSWORD ?? "ChangeMe-Sprint3-Demo";

// Data-dependent tests need a seeded+polled fleet (west-sw-1, cisco-rtr-1 with
// synced interfaces). CI runs a bare api, so gate them on NETINV_E2E_SEEDED;
// local/staging runs against the demo fleet set it.
const seeded = process.env.NETINV_E2E_SEEDED === "1";

async function login(page: Page) {
  await page.goto("/login");
  await page.getByPlaceholder("Username").fill("admin");
  await page.getByPlaceholder("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("link", { name: "Dashboard" })).toBeVisible();
}

test("login lands on the dashboard with the status strip", async ({ page }) => {
  await login(page);
  await expect(page.getByText("Devices up")).toBeVisible();
  await expect(page.getByText("Availability (24h)")).toBeVisible();
});

test("bad password shows a generic failure (no enumeration)", async ({ page }) => {
  await page.goto("/login");
  await page.getByPlaceholder("Username").fill("admin");
  await page.getByPlaceholder("Password").fill("definitely-wrong");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText(/Sign-in failed/)).toBeVisible();
});

test("inventory lists devices and filters by search", async ({ page }) => {
  test.skip(!seeded, "needs seeded fleet");
  await login(page);
  await page.getByRole("link", { name: "Inventory" }).click();
  await expect(page.getByRole("cell", { name: "west-sw-1" })).toBeVisible();
  await page.getByPlaceholder(/Search name/).fill("cisco");
  await page.getByPlaceholder(/Search name/).blur();
  await expect(page.getByRole("cell", { name: "cisco-rtr-1" })).toBeVisible();
  // URL carries the filter state (FR-DEV-04).
  await expect(page).toHaveURL(/q=cisco/);
});

test("device detail deep-links and shows interfaces", async ({ page }) => {
  test.skip(!seeded, "needs seeded fleet");
  await login(page);
  await page.goto("/inventory");
  await page.getByRole("link", { name: "west-sw-1" }).click();
  await expect(page.getByRole("heading", { name: "west-sw-1" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "eth0" })).toBeVisible();
});

test("weathermap list is reachable", async ({ page }) => {
  await login(page);
  await page.getByRole("link", { name: "Weathermaps" }).click();
  await expect(page.getByRole("heading", { name: "Weathermaps" })).toBeVisible();
});

test("admin sees role-gated nav (Users, Audit, Settings)", async ({ page }) => {
  await login(page);
  await expect(page.getByRole("link", { name: "Users" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Audit" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Settings" })).toBeVisible();
});
