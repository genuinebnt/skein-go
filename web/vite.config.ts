/// <reference types="vitest/config" />
import { createReadStream, cpSync, existsSync, statSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig, loadEnv, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

const MOCK_SEED = resolve(import.meta.dirname, ".mock-seed");

/**
 * Serves web/.mock-seed at /mock-seed in mock mode (dev and preview) and
 * copies it into a mock build. Live builds never see it.
 */
function mockSeed(): Plugin {
  const serve = (req: { url?: string }, res: import("node:http").ServerResponse, next: () => void) => {
    if (!req.url?.startsWith("/mock-seed/")) return next();
    const file = resolve(MOCK_SEED, decodeURIComponent(req.url.slice("/mock-seed/".length).split("?")[0]!));
    if (!file.startsWith(MOCK_SEED) || !existsSync(file) || !statSync(file).isFile()) {
      res.statusCode = 404;
      return res.end();
    }
    res.setHeader("Content-Type", file.endsWith(".json") ? "application/json" : "application/octet-stream");
    createReadStream(file).pipe(res);
  };
  let outDir = "dist";
  return {
    name: "skein-mock-seed",
    configResolved: (config) => void (outDir = config.build.outDir),
    configureServer: (server) => void server.middlewares.use(serve),
    configurePreviewServer: (server) => void server.middlewares.use(serve),
    closeBundle: () => {
      if (existsSync(MOCK_SEED)) cpSync(MOCK_SEED, resolve(outDir, "mock-seed"), { recursive: true });
    },
  };
}

// Modes: `mock` serves testdata/seed/small through an in-browser fake gateway
// (src/mocks); `live` talks to the gateway. In live mode with no
// VITE_API_BASE, the dev server proxies /v1 (REST and WebSockets) to
// VITE_GATEWAY, so the browser stays same-origin and needs no CORS.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "VITE_");
  const gateway = env.VITE_GATEWAY || "http://localhost:8080";
  return {
    plugins: [react(), ...(mode === "mock" ? [mockSeed()] : [])],
    server: {
      port: 5173,
      strictPort: false,
      proxy: mode === "live" && !env.VITE_API_BASE ? { "/v1": { target: gateway, changeOrigin: true, ws: true } } : undefined,
    },
    build: {
      target: "es2023",
      sourcemap: true,
    },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test/setup.ts"],
      include: ["src/**/*.test.{ts,tsx}"],
      css: false,
      testTimeout: 20_000,
    },
  };
});
