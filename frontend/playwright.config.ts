import { defineConfig } from "@playwright/test";

// E2E against the dev server proxying to a running netinv-api (doc 24 §3).
// NETINV_ADMIN_PASSWORD must match the api's bootstrap admin password.
export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  use: {
    baseURL: "http://localhost:5173",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: {
    command: "npm run dev",
    url: "http://localhost:5173",
    reuseExistingServer: true,
    timeout: 60_000,
  },
});
