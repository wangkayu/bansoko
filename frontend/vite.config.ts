import { defineConfig } from "vite";
import react from "@vitejs/plugin-react-swc";
import tailwindcss from "@tailwindcss/vite";
import path from "path";
import { VitePWA } from "vite-plugin-pwa";

// https://vitejs.dev/config/
export default defineConfig(() => ({
  server: {
    host: "::",
    port: 5173,
    // Fixture mode (VITE_BANSOKO_E2E_FIXTURES=1, set by the Playwright fixture
    // webServer command) has no backend: specs mock every API route via
    // page.route(). Omitting the proxy entirely guarantees that an unmocked
    // request fails on the local fixture server instead of tunneling to
    // whatever happens to run on 127.0.0.1:8080.
    proxy: process.env.VITE_BANSOKO_E2E_FIXTURES === "1"
      ? undefined
      : {
          '/api': {
            target: 'http://127.0.0.1:8080',
            changeOrigin: true,
            rewrite: (path) => path,
          },
          '/ws': {
            target: 'ws://127.0.0.1:8080',
            ws: true,
            changeOrigin: true,
          },
        },
  },
  plugins: [
    react(),
    tailwindcss(),
    VitePWA({
      registerType: 'autoUpdate',
      devOptions: {
        enabled: true,
      },
      workbox: {
        // API addresses are never the app shell: a browser download of a
        // backup (/api/v1/backup/download) is a navigation and must reach the
        // server, not the cached index.html.
        navigateFallbackDenylist: [/^\/api\//],
        globIgnores: [
          '**/QBManagerDialogEntry-*.js',
          '**/LogDialogEntry-*.js',
          '**/SettingsDialogEntry-*.js',
          '**/CodeEditor-*.js',
        ],
      },
    }),
  ],
  build: {
    // The document CSP is font-src 'self' (no data:), so fonts are always emitted
    // as files, never inlined as data: URIs.
    assetsInlineLimit: (file) => (/\.(woff2?|ttf|otf)$/.test(file) ? false : undefined),
    rollupOptions: {
      output: {
        manualChunks(id) {
          const normalizedID = id.replaceAll("\\\\", "/");
          // The code editor and its parser load with the lazy editor dialog only;
          // leaving them to Rollup keeps them out of the always-loaded vendor chunk.
          if (/\/node_modules\/(\.pnpm\/)?(@codemirror|@lezer|crelt|style-mod|w3c-keyname)[+/@]/.test(normalizedID)) {
            return undefined;
          }
          if (normalizedID.includes("/node_modules/")) {
            return "vendor";
          }
        },
      },
    },
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
}));
