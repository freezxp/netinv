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
  await request.delete(`/api/v1/maps/${id}`, {
    headers: await apiHeaders(request),
  });
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

test("bad password shows a generic failure (no enumeration)", async ({
  page,
}) => {
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
  await expect(
    page.getByRole("heading", { name: "Weathermaps" }),
  ).toBeVisible();
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

  const devs = await (
    await request.get("/api/v1/devices?limit=2", { headers })
  ).json();
  expect(devs.data.length).toBeGreaterThanOrEqual(2);
  // Bind one end so the link renders as a live band rather than the dashed
  // placeholder — the direction affordances only exist on a bound link.
  const ifs = await (
    await request.get(`/api/v1/devices/${devs.data[0].id}/interfaces`, {
      headers,
    })
  ).json();
  expect(ifs.data.length).toBeGreaterThan(0);
  const ifIndex = ifs.data[0].if_index;

  const name = `e2e-link-${Date.now()}`;
  const created = await (
    await request.post("/api/v1/maps", { headers, data: { name } })
  ).json();
  const draft = await request.put(`/api/v1/maps/${created.id}/draft`, {
    headers,
    data: {
      schema: "netinv.map/1",
      nodes: [
        {
          id: "a",
          kind: "device",
          device_id: devs.data[0].id,
          label: "A",
          x: 60,
          y: 60,
        },
        {
          id: "b",
          kind: "device",
          device_id: devs.data[1].id,
          label: "B",
          x: 380,
          y: 60,
        },
      ],
      links: [
        {
          id: "l1",
          from: "a",
          to: "b",
          from_handle: "r",
          to_handle: "l",
          a_endpoint: { device_id: devs.data[0].id, if_index: ifIndex },
        },
      ],
    },
  });
  expect(draft.ok()).toBeTruthy();
  expect(
    (
      await request.post(`/api/v1/maps/${created.id}/publish`, { headers })
    ).ok(),
  ).toBeTruthy();

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

  // Direction has to be readable without hovering: one arrowhead per
  // direction, and a rate figure for each.
  const edge = page.locator("g.netinv-cacti-edge");
  await expect(edge).toHaveCount(1);
  await expect(edge.locator("polygon")).toHaveCount(2);
  await expect(edge.locator("text")).toHaveCount(2);

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
  // Scoped to the sidebar: the phone topbar carries the brand too, and it is
  // in the DOM at every width, merely hidden by CSS on desktop.
  await expect(page.locator("aside").getByText("NetInv")).toHaveCount(0);
  await inventory.click();
  await expect(page.getByRole("heading", { name: "Inventory" })).toBeVisible();

  await page.reload();
  await expect(
    page.getByRole("button", { name: "Expand sidebar" }),
  ).toBeVisible();
  const still = (await aside.boundingBox())!.width;
  expect(still).toBeLessThan(wide);

  await page.getByRole("button", { name: "Expand sidebar" }).click();
  await expect(page.locator("aside").getByText("NetInv")).toBeVisible();
});

// Alert rules are operator-authored now, so the two things that must hold are
// that a bad expression is refused before it can be stored (a stored typo just
// never fires, silently), and that built-ins cannot be deleted.
test("alert rules can be created, edited and deleted", async ({ page }) => {
  await login(page);
  await page.getByRole("link", { name: "Alerts" }).click();
  await page.getByRole("button", { name: "Rules" }).click();
  await expect(page.getByText("Interface down")).toBeVisible();

  // Built-ins are tunable but not removable: no Delete on that row.
  const builtin = page
    .locator("tr")
    .filter({ hasText: "Interface down" })
    .first();
  await expect(builtin.getByRole("button", { name: "Delete" })).toHaveCount(0);
  await expect(builtin.getByRole("button", { name: "Edit" })).toBeVisible();

  const name = `e2e-rule-${Date.now()}`;
  await page.getByRole("button", { name: "New rule" }).click();
  await page.getByPlaceholder("Temperature above 75C").fill(name);
  const expr = page.getByPlaceholder(/max_over_time/);

  // A typo must be refused here rather than stored and never fire.
  await expr.fill("netinv_device_cpu_percent >");
  await page.getByRole("button", { name: "Create rule" }).click();
  await expect(page.getByText(/rejected this expression/)).toBeVisible();

  await expr.fill("netinv_device_cpu_percent > 99");
  await page.getByRole("button", { name: "Create rule" }).click();
  await expect(page.getByText(name)).toBeVisible();

  // Edit it: severity only, expression left alone.
  const row = page.locator("tr").filter({ hasText: name }).first();
  await row.getByRole("button", { name: "Edit" }).click();
  await page.getByRole("combobox").last().selectOption("critical");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(row.getByText("critical")).toBeVisible();

  // Delete needs the name typed out.
  await row.getByRole("button", { name: "Delete" }).click();
  const confirm = page.getByRole("button", { name: "Delete permanently" });
  await expect(confirm).toBeDisabled();
  await page.getByPlaceholder(name).fill(name);
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect(page.getByText(name)).toHaveCount(0);
});

// FR-MAP-02 asks for site/cloud/label nodes alongside devices: the things a
// map needs to make sense that NetInv does not poll — an ISP, a customer site,
// a caption. Needs no fleet, so it runs anywhere.
test("plain nodes can be placed, renamed and removed", async ({
  page,
  request,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Weathermaps" }).click();
  const name = `e2e-nodes-${Date.now()}`;
  await page.getByPlaceholder("New map name").fill(name);
  await page.getByRole("button", { name: "Create" }).click();

  const card = page
    .locator("div")
    .filter({ hasText: name })
    .filter({ has: page.getByRole("button", { name: "Edit" }) })
    .last();
  await card.getByRole("button", { name: "Edit" }).click();
  await expect(page.getByText("Add device node")).toBeVisible();

  await page.getByRole("button", { name: "☁ Cloud" }).click();
  await page.getByRole("button", { name: "▣ Site" }).click();
  await page.getByRole("button", { name: "Label", exact: true }).click();
  await expect(page.locator(".react-flow__node")).toHaveCount(3);

  // Renaming is what makes a plain node useful — a cloud called "Label" is not.
  await page
    .locator(".react-flow__node")
    .filter({ hasText: "Internet" })
    .click();
  await expect(page.getByText("Cloud node")).toBeVisible();
  await page.locator('input[value="Internet"]').fill("ISP");
  await expect(
    page.locator(".react-flow__node").filter({ hasText: "ISP" }),
  ).toBeVisible();

  // The draft must survive the server's document validation.
  await expect(page.getByText("draft saved")).toBeVisible({ timeout: 10_000 });

  await page.getByRole("button", { name: "Remove from map" }).click();
  await expect(page.locator(".react-flow__node")).toHaveCount(2);

  const id = page.url().match(/\/maps\/([^/]+)/)?.[1];
  if (id) await deleteMap(request, id);
});

// The Wireless tab exists only for devices that actually report clients, and
// the counts behind it come from two metrics combined with `or` — which
// collapses them unless they are labelled apart, so "2 / 2" silently became
// "—". Skips when nothing wireless is being polled.
test("a wireless device gets a Wireless tab with its client count", async ({
  page,
  request,
}) => {
  const headers = await apiHeaders(request);
  const res = await request.get(
    "/api/v1/metrics/query_range?query=netinv_wireless_client_count" +
      `&start=${Math.floor(Date.now() / 1000) - 3600}` +
      `&end=${Math.floor(Date.now() / 1000)}&step=300s`,
    { headers },
  );
  const series = (await res.json()).data.result;
  test.skip(series.length === 0, "no wireless device is being polled");
  const wirelessID = series[0].metric.device_id;

  const devs = await (
    await request.get("/api/v1/devices?limit=50", { headers })
  ).json();
  const wireless = devs.data.find((d: { id: string }) => d.id === wirelessID);
  const other = devs.data.find((d: { id: string }) => d.id !== wirelessID);
  expect(wireless).toBeTruthy();

  await login(page);
  await page.goto(`/devices/${wirelessID}`);
  const tab = page.getByRole("button", { name: "Wireless", exact: true });
  await expect(tab).toBeVisible();
  await tab.click();
  await expect(
    page.getByText("Connected clients", { exact: false }).first(),
  ).toBeVisible();
  // Both halves of the AP count must survive the query, not just the first.
  await expect(page.getByText(/^\d+ \/ \d+$/)).toBeVisible();

  // A device with no wireless metrics must not grow the tab.
  if (other) {
    await page.goto(`/devices/${other.id}`);
    await expect(
      page.getByRole("button", { name: "Interfaces" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Wireless", exact: true }),
    ).toHaveCount(0);
  }
});

// Phone layout (NFR-60). The sidebar took 208px of a 393px screen, leaving
// content in a squeezed column, and several pages could not be scrolled to
// their right-hand columns at all.
test("the portal is usable at phone width", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);

  const aside = page.locator("aside");
  const menu = page.getByRole("button", { name: "Open menu" });
  await expect(menu).toBeVisible();

  // Off-canvas until asked for, then on screen, then gone again after
  // navigating — a drawer that stays open over the page it just opened is
  // worse than no drawer.
  expect((await aside.boundingBox())!.x).toBeLessThan(0);
  await menu.click();
  await expect
    .poll(async () => (await aside.boundingBox())!.x)
    .toBeGreaterThanOrEqual(0);
  await page.getByRole("link", { name: "Inventory" }).click();
  await expect.poll(async () => (await aside.boundingBox())!.x).toBeLessThan(0);

  // Nothing may scroll the page sideways; tables scroll inside their own card.
  for (const path of ["/", "/inventory", "/alerts", "/maps", "/platform"]) {
    await page.goto(path);
    await expect(page.getByRole("button", { name: "Open menu" })).toBeVisible();
    const over = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(over, `${path} overflows by ${over}px`).toBeLessThanOrEqual(1);
  }

  // A dialog has to fit the screen it opens on.
  await page.goto("/inventory");
  await page.getByRole("button", { name: "Add device" }).click();
  const dialog = page.getByText("Add device", { exact: true }).last();
  await expect(dialog).toBeVisible();
  const over = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(over, "the add-device dialog overflows").toBeLessThanOrEqual(1);
});

// SNMPv3 was API-only until now: the form offered v2c and a note saying to use
// the API for v3, which is not a usable answer for the credential type most
// production networks actually run.
test("an SNMPv3 credential can be created from the UI", async ({
  page,
  request,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Platform" }).click();
  await page.getByRole("button", { name: "Credentials" }).click();

  // v2c stays the default and keeps its single secret field.
  await expect(page.getByPlaceholder("Community (write-only)")).toBeVisible();

  await page.getByRole("combobox").first().selectOption("snmp_v3");
  const name = `e2e-v3-${Date.now()}`;
  await page.getByPlaceholder("Name", { exact: true }).fill(name);
  await page.getByPlaceholder("Username", { exact: true }).fill("netmon");

  const add = page.getByRole("button", { name: "Add credential" });
  // Privacy defaults to AES-128, so a passphrase is required — the backend
  // rejects a protocol without one, and the form must not let it get there.
  await page.locator('input[type="password"]').nth(0).fill("authpass-123");
  await expect(add).toBeDisabled();
  await page.locator('input[type="password"]').nth(1).fill("privpass-123");
  await expect(add).toBeEnabled();
  await add.click();

  // The row states the security level, which is the thing an operator checks.
  const row = page.locator("tr").filter({ hasText: name });
  await expect(row).toContainText("SNMPv3");
  await expect(row).toContainText("netmon");
  await expect(row).toContainText("sha256");
  await expect(row).toContainText("aes128");

  const headers = await apiHeaders(request);
  const creds = await (
    await request.get("/api/v1/credentials", { headers })
  ).json();
  const made = creds.data.find((c: { name: string }) => c.name === name);
  expect(made).toBeTruthy();
  // Secrets are write-only (FR-CRED-01): nothing secret may come back.
  expect(JSON.stringify(made)).not.toContain("authpass-123");
  expect(JSON.stringify(made)).not.toContain("privpass-123");
  await request.delete(`/api/v1/credentials/${made.id}`, { headers });
});

// The audit log showed a truncated ULID in its actor column, which answers
// "who did this?" for nobody — the whole point of the log.
test("the audit log names the user who acted", async ({ page, request }) => {
  await login(page); // writes an auth.login.success attributed to admin
  await page.getByRole("link", { name: "Audit" }).click();
  await expect(page.getByRole("heading", { name: "Audit log" })).toBeVisible();

  const row = page
    .locator("tr")
    .filter({ hasText: "auth.login.success" })
    .first();
  await expect(row).toContainText("admin");
  // The raw id must not be what a reader sees.
  await expect(row).not.toContainText("u_01");

  const headers = await apiHeaders(request);
  const res = await request.get("/api/v1/audit-events?limit=20", { headers });
  const events = (await res.json()).data;
  const loginEvent = events.find(
    (e: { action: string }) => e.action === "auth.login.success",
  );
  expect(loginEvent.actor).toBe("admin");
  expect(loginEvent.actor_id).toMatch(/^u_/); // id still there for correlation

  // Filtering accepts the username, not just the id nobody has to hand.
  const byName = await request.get("/api/v1/audit-events?actor=admin&limit=5", {
    headers,
  });
  const filtered = (await byName.json()).data;
  expect(filtered.length).toBeGreaterThan(0);
  for (const e of filtered) expect(e.actor).toBe("admin");
});

test("admin sees role-gated nav (Users, Audit, Settings)", async ({ page }) => {
  await login(page);
  await expect(page.getByRole("link", { name: "Users" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Audit" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Settings" })).toBeVisible();
});

test("the graph time range selector applies across pages", async ({
  page,
  request,
}) => {
  await login(page);

  const picker = page.getByLabel("Dashboard graph time range");
  await expect(picker).toBeVisible();

  // Presets are Cacti's, defaulting to Cacti's own "Last Day". Chart titles
  // state the window — a chart whose span is not stated cannot be reasoned
  // about, and two charts on different spans read as comparable when they are
  // not, which is what this replaced.
  await expect(picker).toHaveValue("1d");
  await expect(page.getByText("Bandwidth in, by site (1d)")).toBeVisible();
  await expect(page.getByText("Bandwidth out, by site (1d)")).toBeVisible();

  await picker.selectOption("1w");
  await expect(page.getByText("Bandwidth in, by site (1w)")).toBeVisible();
  await expect(page.getByText("Bandwidth out, by site (1w)")).toBeVisible();
  await expect(page.getByText("Latency — ICMP avg RTT (1w)")).toBeVisible();

  // The selection is one operator question ("what did last week look like?"),
  // so it must survive navigating into a device rather than resetting.
  //
  // The device is resolved through the API rather than by clicking the
  // inventory table: that table renders empty often enough that the guard on
  // it turned this whole test into a silent skip, which is close to not having
  // the test at all.
  const headers = await apiHeaders(request);
  const devs = await (
    await request.get("/api/v1/devices?limit=1", { headers })
  ).json();
  test.skip(!devs.data?.length, "no devices exist to open");
  await page.goto(`/devices/${devs.data[0].id}`);

  await page.getByRole("button", { name: "Health" }).click();
  await expect(page.getByText("CPU utilization (1w)")).toBeVisible();
  await expect(page.getByLabel("Graph time range")).toBeVisible();

  // ...and a reload, since it is a preference rather than page state.
  await page.reload();
  await page.getByRole("button", { name: "Health" }).click();
  await expect(page.getByText("CPU utilization (1w)")).toBeVisible();
});

test("the range selector is hidden on tabs that show no graphs", async ({
  page,
  request,
}) => {
  const headers = await apiHeaders(request);
  const devs = await (
    await request.get("/api/v1/devices?limit=1", { headers })
  ).json();
  test.skip(!devs.data?.length, "no devices exist to open");

  await login(page);
  await page.goto(`/devices/${devs.data[0].id}`);

  await expect(page.getByLabel("Graph time range")).toBeVisible();

  // History is a table of change events with their own timestamps. Showing a
  // range control over it would imply it filters those rows, which it does not.
  await page.getByRole("button", { name: "History" }).click();
  await expect(page.getByLabel("Graph time range")).toHaveCount(0);
});

// The weathermap's hover graph follows the shared range, but the card is a
// transient pointer-events-none overlay — so without a control on the page
// chrome the map would be the one place showing a window you cannot change.
test("the weathermap viewer can change the link graph range", async ({
  page,
  request,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Weathermaps" }).click();
  const name = `e2e-range-${Date.now()}`;
  await page.getByPlaceholder("New map name").fill(name);
  await page.getByRole("button", { name: "Create" }).click();

  const card = page
    .locator("div")
    .filter({ hasText: name })
    .filter({ has: page.getByRole("button", { name: "View" }) })
    .last();
  await expect(card).toBeVisible();
  await card.getByRole("button", { name: "View" }).click();
  await expect(page.getByRole("heading", { name: "Weathermap" })).toBeVisible();

  const picker = page.getByLabel("Link graph time range");
  await expect(picker).toBeVisible();
  await picker.selectOption("1h");
  await expect(picker).toHaveValue("1h");

  // The choice is global, so it must be the one the dashboard shows too.
  await page.getByRole("link", { name: "Dashboard" }).click();
  await expect(page.getByText("Bandwidth in, by site (1h)")).toBeVisible();

  await page.goBack();
  const id = page.url().match(/\/maps\/([^/]+)/)?.[1];
  if (id) await deleteMap(request, id);
});

// Capacity answers a question the retention default raises but cannot answer
// on its own: whether this disk can actually hold two years of samples.
test("platform capacity reports storage and what the disk sustains", async ({
  page,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Platform" }).click();
  await page.getByRole("button", { name: "Capacity", exact: true }).click();

  await expect(page.getByText("How long data can be kept")).toBeVisible();
  await expect(page.getByText("Retention setting")).toBeVisible();
  await expect(page.getByText("This volume sustains")).toBeVisible();

  // Numbers, not placeholders: a capacity page showing "—" is worse than none.
  await expect(page.getByText("NETINV_VM_RETENTION")).toBeVisible();
  await expect(
    page.getByText(/\d+(\.\d+)?\s+(years|months|days|hours)/).first(),
  ).toBeVisible();

  await expect(page.getByText("Metrics data")).toBeVisible();
  await expect(page.getByText("Per device, per year")).toBeVisible();
  await expect(page.getByText("Unexpected Application Error")).toHaveCount(0);
});

// Both directions matter on a WAN link: a site can be comfortable inbound and
// saturated outbound, and the summary API had been computing the outbound
// figure only for the UI to discard it.
test("the dashboard shows inbound and outbound throughput", async ({
  page,
}) => {
  await login(page);

  await expect(page.getByText("Throughput in")).toBeVisible();
  await expect(page.getByText("Throughput out")).toBeVisible();

  // Real numbers, not the loading ellipsis.
  const out = page
    .locator("div")
    .filter({ hasText: /^Throughput out/ })
    .last();
  await expect(out).not.toHaveText(/…/);

  await expect(page.getByText(/^Bandwidth in, by site/)).toBeVisible();
  await expect(page.getByText(/^Bandwidth out, by site/)).toBeVisible();
  await expect(page.getByText("Unexpected Application Error")).toHaveCount(0);
});

// A fleet-wide cadence change must reach polling_schedule, which is what the
// scheduler reads — updating only the profile would leave the UI reporting a
// cadence that collection was not using.
test("the polling interval can be changed for the whole fleet", async ({
  page,
}) => {
  await login(page);
  await page.getByRole("link", { name: "Platform" }).click();
  await page.getByRole("button", { name: "Pollers", exact: true }).click();

  const picker = page.getByLabel("Polling interval");
  await expect(picker).toBeVisible();
  const original = await picker.inputValue();

  await picker.selectOption("300");
  await expect(picker).toHaveValue("300");
  // Health follows traffic rather than out-polling it.
  await expect(page.getByText(/device health at 5 min/)).toBeVisible();
  // ICMP is deliberately left fast, and the page says so.
  await expect(page.getByText(/ICMP stays at 30s/)).toBeVisible();

  await picker.selectOption(original);
  await expect(picker).toHaveValue(original);
  await expect(page.getByText("Unexpected Application Error")).toHaveCount(0);
});

// The dashboard is assembled from a per-user layout. It must still look
// untouched for someone who never opens the editor, and a saved layout has to
// survive a reload — it lives on the account, not in the browser.
test("the dashboard can be customised and the layout persists", async ({
  page,
}) => {
  await login(page);

  // Default layout: the panels the dashboard had before it was customisable.
  await expect(page.getByText(/^Bandwidth in, by site/)).toBeVisible();
  await expect(page.getByText("Devices up")).toBeVisible();

  await page.getByRole("button", { name: "Customise" }).click();
  await expect(page.getByText("Customise dashboard")).toBeVisible();

  // Add a weathermap panel and choose a map for it.
  await page.getByLabel("Panel to add").selectOption("weathermap");
  await page.getByRole("button", { name: "Add panel" }).click();
  const mapPick = page.getByLabel("Weathermap to show");
  await expect(mapPick).toBeVisible();
  const opts = await mapPick.locator("option").count();
  if (opts > 1) {
    await mapPick.selectOption({ index: 1 });
  }

  // Removing a panel takes it off the dashboard.
  await page.getByRole("button", { name: "Remove Latency (ICMP RTT)" }).click();
  await page.getByRole("button", { name: "Done" }).first().click();
  await expect(page.getByText(/^Latency — ICMP avg RTT/)).toHaveCount(0);

  // Stored on the account, so a reload keeps it.
  await page.reload();
  await expect(page.getByText(/^Latency — ICMP avg RTT/)).toHaveCount(0);
  await expect(page.getByText(/^Bandwidth in, by site/)).toBeVisible();

  // Reset puts everything back, so a bad layout is never a trap.
  await page.getByRole("button", { name: "Customise" }).click();
  await page.getByRole("button", { name: "Reset to default" }).click();
  await page.getByRole("button", { name: "Done" }).first().click();
  await expect(page.getByText(/^Latency — ICMP avg RTT/)).toBeVisible();
  await expect(page.getByText("Unexpected Application Error")).toHaveCount(0);
});

// A link could only be removed by deleting one of the nodes it joined, which
// takes every other link on that node with it. Rebuilding a node to drop one
// link is not a workflow.
test("a weathermap link can be removed on its own", async ({
  page,
  request,
}) => {
  const headers = await apiHeaders(request);
  const name = `e2e-unlink-${Date.now()}`;
  const created = await (
    await request.post("/api/v1/maps", { headers, data: { name } })
  ).json();

  // Two non-device nodes and two links between them: removing one must leave the
  // other, and must leave both nodes alone.
  await request.put(`/api/v1/maps/${created.id}/draft`, {
    headers,
    data: {
      schema: "netinv.map/1",
      nodes: [
        { id: "a", kind: "cloud", label: "A", x: 60, y: 60 },
        { id: "b", kind: "cloud", label: "B", x: 380, y: 60 },
      ],
      links: [
        { id: "l1", from: "a", to: "b" },
        { id: "l2", from: "b", to: "a" },
      ],
    },
  });

  await login(page);
  await page.goto(`/maps/${created.id}/edit`);
  await expect(page.getByText("Add device node")).toBeVisible();

  const edges = page.locator(".react-flow__edge");
  await expect(edges).toHaveCount(2);

  // Selecting a link opens its panel, which now offers removal directly.
  await edges.first().click({ force: true });
  const remove = page.getByRole("button", { name: "Remove link" });
  await expect(remove).toBeVisible();
  await remove.click();

  await expect(edges).toHaveCount(1);
  // Both nodes survive — that is the whole point.
  await expect(page.locator(".react-flow__node")).toHaveCount(2);

  // The Delete key must work too, and did not until edge selection stopped
  // relying on React Flow's internal flag: toFlow rebuilds every edge object
  // from the definition on each render, wiping it, so the key had nothing
  // selected to act on while the panel button worked fine.
  await edges.first().click({ force: true });
  await expect(page.getByRole("button", { name: "Remove link" })).toBeVisible();
  await page.keyboard.press("Delete");
  await expect(edges).toHaveCount(0);
  await expect(page.locator(".react-flow__node")).toHaveCount(2);
  await expect(page.getByText("Unexpected Application Error")).toHaveCount(0);

  await deleteMap(request, created.id);
});

// Two links between the same pair of nodes drew the same path: a second WAN
// circuit, a LAG member or a second tunnel rendered as one line, and only the
// topmost could be clicked. The definition always allowed it — links carry
// their own ids and nothing dedupes them — so this is about geometry, and the
// assertion is that the paths land at distinct positions and select
// independently.
test("parallel links between the same nodes are drawn apart", async ({
  page,
  request,
}) => {
  test.skip(!seeded, "needs devices to put on the map");

  const headers = await apiHeaders(request);
  const devs = await (
    await request.get("/api/v1/devices?limit=2", { headers })
  ).json();
  expect(devs.data.length).toBeGreaterThanOrEqual(2);

  const name = `e2e-parallel-${Date.now()}`;
  const created = await (
    await request.post("/api/v1/maps", { headers, data: { name } })
  ).json();
  const link = (id: string) => ({
    id,
    from: "a",
    to: "b",
    from_handle: "r",
    to_handle: "l",
  });
  const saved = await request.put(`/api/v1/maps/${created.id}/draft`, {
    headers,
    data: {
      schema: "netinv.map/1",
      nodes: [
        {
          id: "a",
          kind: "device",
          device_id: devs.data[0].id,
          label: "A",
          x: 60,
          y: 200,
        },
        {
          id: "b",
          kind: "device",
          device_id: devs.data[1].id,
          label: "B",
          x: 460,
          y: 200,
        },
      ],
      links: [link("l1"), link("l2"), link("l3")],
    },
  });
  expect(saved.status()).toBe(204);
  await request.post(`/api/v1/maps/${created.id}/publish`, { headers });

  await login(page);
  await page.goto(`/maps/${created.id}`);
  await expect(page.getByRole("heading", { name: "Weathermap" })).toBeVisible();
  await page.waitForTimeout(1500);

  // Three edges, at three distinct vertical positions. Before the lane offset
  // these were one line: the count passed and the geometry did not.
  const ys = await page.$$eval(".react-flow__edge", (els) =>
    els.map((el) => Math.round(el.getBoundingClientRect().y)),
  );
  expect(ys).toHaveLength(3);
  expect(new Set(ys).size, `parallel links overlap at y=${ys.join(",")}`).toBe(
    3,
  );

  await deleteMap(request, created.id);
});
