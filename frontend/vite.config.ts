import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// API calls proxy to the local netinv-api during development (doc 14).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": "http://localhost:8080" },
  },
});
