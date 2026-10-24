import { defineConfig } from "vitest/config";

// Isolated Node-only safety fixtures; no React suite or browser runtime.
export default defineConfig({
  test: {
    include: ["scripts/disposable-db-snapshot.test.ts"],
    environment: "node",
    globals: false,
    poolOptions: { forks: { minForks: 1, maxForks: 1 } },
  },
});
