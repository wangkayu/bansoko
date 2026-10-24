import { defineConfig } from "vitest/config";
import path from "node:path";

// Vitest config for frontend tests. Pure-logic modules run in Node;
// component tests (*.test.tsx) run in jsdom with Testing Library.
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    globals: false,
    environment: "node",
    environmentMatchGlobs: [["**/*.test.tsx", "jsdom"]],
    setupFiles: ["./vitest.setup.ts"],
    poolOptions: { forks: { minForks: 1, maxForks: 4 } },
  },
});
