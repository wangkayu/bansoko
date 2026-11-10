import type { Plugin } from "vite";

/** Fetch the first-render App chunk alongside the locale, without evaluating it.
 * main.tsx still waits for the selected catalog before importing the module. */
export function preloadApp(): Plugin {
  let base = "/";
  return {
    name: "preload-app",
    apply: "build",
    configResolved(config) { base = config.base; },
    transformIndexHtml: {
      order: "post",
      handler(_html, { bundle }) {
        if (!bundle) return;
        const app = Object.values(bundle).find(item =>
          item.type === "chunk" && item.moduleIds.some(id => id.replaceAll("\\", "/").endsWith("/src/App.tsx")),
        );
        if (!app) throw new Error("App chunk missing from the frontend build");
        return [{
          tag: "link",
          attrs: { rel: "modulepreload", crossorigin: true, href: `${base}${app.fileName}` },
          injectTo: "head",
        }];
      },
    },
  };
}
