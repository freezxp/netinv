import {
  test,
  expect,
  type APIRequestContext,
  type Page,
} from "@playwright/test";

const PASSWORD = process.env.NETINV_ADMIN_PASSWORD ?? "ChangeMe-Sprint3-Demo";

// Data-dependent tests need a seeded+polled fleet (west-sw-1, cisco-rtr-1 with
// synced interfaces). CI runs a bare api, so gate them on NETINV_E2E_SEEDED;
// local/staging runs against the demo fleet set it.
const seeded = process.env.NETINV_E2E_SEEDED === "1";

// Maps created by tests must be removed again, or every run leaves litter in
// the operator's map list.
async function apiHeaders(request: APIRequestContext) {
  const res = await request.post("/api/v1/auth/login", {
    data: { username: "admin", password: PASSWORD },
  });
  const { access_token: token } = await res.json();
  return { Authorization: `Bearer ${token}` };
}

async function deleteMap(request: APIRequestContext, id: string) {
  await request.delete(`/api/v1/maps/${id}`, { headers: await apiHeaders(request) });
}

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

// A map with no links made the live endpoint emit links:null, and the viewer
// crashed iterating it — so the flagship feature broke for exactly the map
// someone had just created. A fresh map reproduces it with no seeded fleet.
test("a brand-new map opens in the viewer and the editor", async ({
  page,
  request,
}) => {
  const crashes: string[] = [];
  page.on("pageerror", (e) => crashes.push(e.message));

  await login(page);
  await page.getByRole("link", { name: "Weathermaps" }).click();
  const name = `e2e-empty-${Date.now()}`;
  await page.getByPlaceholder("New map name").fill(name);
  await page.getByRole("button", { name: "Create" }).click();

  // Innermost div that holds both the map's name and its buttons.
  const card = page
    .locator("div")
    .filter({ hasText: name })
    .filter({ has: page.getByRole("button", { name: "View" }) })
    .last();
  await expect(card).toBeVisible();
  await card.getByRole("button", { name: "View" }).click();
  await expect(page.getByRole("heading", { name: "Weathermap" })).toBeVisible();
  await expect(page.getByText("Unexpected Application Error")).toHaveCount(0);

  await page.getByRole("button", { name: "Edit" }).click();
  await expect(page.getByText("Add device node")).toBeVisible();
  expect(crashes, `viewer/editor threw: ${crashes.join("; ")}`).toEqual([]);

  // /maps/{id}/edit — the id is the only handle the UI ever exposed.
  const id = page.url().match(/\/maps\/([^/]+)/)?.[1];
  if (id) await deleteMap(request, id);
});

// Links rendered in the editor but vanished from the published map: nodes
// declare every handle as a source, and the viewer's default Strict mode could
// not resolve a target handle, so it dropped every edge without a word. Built
// through the API rather than a mouse drag — the point is what the viewer does
// with a stored link, and dragging between auto-placed nodes is not reliable
// when a long device name makes two of them overlap.
test("a published link renders in the viewer", async ({ page, request }) => {
  test.skip(!seeded, "needs devices to bind the link to");

  const auth = await request.post("/api/v1/auth/login", {
    data: { username: "admin", password: PASSWORD },
  });
  const { access_token: token } = await auth.json();
  const headers = { Authorization: `Bearer ${token}` };

  const devs = await (await request.get("/api/v1/devices?limit=2", { headers })).json();
  expect(devs.data.length).toBeGreaterThanOrEqual(2);

  const name = `e2e-link-${Date.now()}`;
  const created = await (
    await request.post("/api/v1/maps", { headers, data: { name } })
  ).json();
  const draft = await request.put(`/api/v1/maps/${created.id}/draft`, {
    headers,
    data: {
      schema: "netinv.map/1",
      nodes: [
        { id: "a", kind: "device", device_id: devs.data[0].id, label: "A", x: 60, y: 60 },
        { id: "b", kind: "device", device_id: devs.data[1].id, label: "B", x: 380, y: 60 },
      ],
      links: [{ id: "l1", from: "a", to: "b", from_handle: "r", to_handle: "l" }],
    },
  });
  expect(draft.ok()).toBeTruthy();
  expect((await request.post(`/api/v1/maps/${created.id}/publish`, { headers })).ok())
    .toBeTruthy();

  await login(page);
  await page.getByRole("link", { name: "Weathermaps" }).click();
  const card = page
    .locator("div")
    .filter({ hasText: name })
    .filter({ has: page.getByRole("button", { name: "View" }) })
    .last();
  await card.getByRole("button", { name: "View" }).click();
  await expect(page.getByRole("heading", { name: "Weathermap" })).toBeVisible();
  await expect(page.locator(".react-flow__node")).toHaveCount(2);
  await expect(page.locator(".react-flow__edge")).toHaveCount(1);

  await deleteMap(request, created.id);
});

// Deleting a map is unrecoverable — every revision goes with it — so the
// confirm dialog must actually gate it, not just decorate the click.
test("deleting a map requires typing its name", async ({ page, request }) => {
  await login(page);
  await page.getByRole("link", { name: "Weathermaps" }).click();
  const name = `e2e-del-${Date.now()}`;
  await page.getByPlaceholder("New map name").fill(name);
  await page.getByRole("button", { name: "Create" }).click();

  const card = page
    .locator("div")
    .filter({ hasText: name })
    .filter({ has: page.getByRole("button", { name: "Delete" }) })
    .last();
  await expect(card).toBeVisible();
  await card.getByRole("button", { name: "Delete" }).click();

  const confirm = page.getByRole("button", { name: "Delete permanently" });
  await expect(confirm).toBeDisabled();

  // A near-miss must not arm it.
  await page.getByPlaceholder(name).fill(name.slice(0, -1));
  await expect(confirm).toBeDisabled();

  await page.getByPlaceholder(name).fill(name);
  await expect(confirm).toBeEnabled();
  await confirm.click();

  await expect(page.getByText(name)).toHaveCount(0);
  const list = await (
    await request.get("/api/v1/maps", { headers: await apiHeaders(request) })
  ).json();
  expect(list.data.some((m: { name: string }) => m.name === name)).toBe(false);
});

// Collapsing must not cost anything: the links still navigate, still carry an
// accessible name once the text is hidden, and the choice survives a reload —
// a NOC screen that reopens wide on every refresh defeats the point.
test("the sidebar collapses to icons and stays collapsed", async ({ page }) => {
  await login(page);
  const inventory = page.getByRole("link", { name: "Inventory" });
  await expect(inventory).toBeVisible();

  const aside = page.locator("aside");
  const wide = (await aside.boundingBox())!.width;

  await page.getByRole("button", { name: "Collapse sidebar" }).click();
  await expect
    .poll(async () => (await aside.boundingBox())!.width)
    .toBeLessThan(wide);

  // Name comes from the sr-only span now that the label is not painted.
  await expect(inventory).toBeVisible();
  await expect(page.getByText("NetInv")).toHaveCount(0);
  await inventory.click();
  await expect(page.getByRole("heading", { name: "Inventory" })).toBeVisible();

  await page.reload();
  await expect(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();
  const still = (await aside.boundingBox())!.width;
  expect(still).toBeLessThan(wide);

  await page.getByRole("button", { name: "Expand sidebar" }).click();
  await expect(page.getByText("NetInv")).toBeVisible();
});

test("admin sees role-gated nav (Users, Audit, Settings)", async ({ page }) => {
  await login(page);
  await expect(page.getByRole("link", { name: "Users" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Audit" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Settings" })).toBeVisible();
});
