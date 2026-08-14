import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import pkg from "./package.json" with { type: "json" };

// The footer's version string is stamped in here at build time rather than
// fetched, so it describes the bundle the browser is actually running. A
// release build overrides it with the tag (NETINV_VERSION=$(git describe));
// package.json is the fallback so a plain `npm run build` still says something
// true. Keep package.json's version in step with the released tag.
const version = process.env.NETINV_VERSION || `v${pkg.version}`;

// API calls proxy to the local netinv-api during development (doc 14).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  define: {
    __APP_VERSION__: JSON.stringify(version),
  },
  server: {
    port: 5173,
    proxy: { "/api": "http://localhost:8080" },
  },
});
