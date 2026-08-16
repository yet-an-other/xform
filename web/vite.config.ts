import path from "node:path";
import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const root = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(root, "./src") },
  },
  build: {
    // The built dashboard lives next to the Go embed adapter that serves it.
    outDir: path.resolve(root, "../internal/cmd/xform/dist"),
    emptyOutDir: true,
  },
  server: {
    // Same-origin dev: the Vite dev server proxies the API from the Go process.
    proxy: { "/api": "http://127.0.0.1:9090" },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
  },
});
