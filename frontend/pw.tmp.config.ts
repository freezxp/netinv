import { defineConfig } from "@playwright/test";
export default defineConfig({ testDir: "./e2e", timeout: 60000,
  use: { baseURL: "http://localhost:8090", viewport: { width: 1440, height: 900 } } });
