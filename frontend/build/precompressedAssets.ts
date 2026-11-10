import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { brotliCompressSync, gzipSync } from "node:zlib";
import type { Plugin } from "vite";

/** Compress final JS/CSS once at build time, after Vite rewrites chunk references. */
export function precompressedAssets(): Plugin {
  return {
    name: "precompressed-assets",
    apply: "build",
    writeBundle: {
      order: "post",
      async handler(options, bundle) {
        if (!options.dir) return;
        for (const item of Object.values(bundle)) {
          if (!/^assets\/.+\.(js|css)$/.test(item.fileName)) continue;
          const file = resolve(options.dir, item.fileName);
          const source = await readFile(file);
          if (source.length < 1024) continue;
          for (const [extension, compressed] of [["gz", gzipSync(source, { level: 9 })], ["br", brotliCompressSync(source)]] as const) {
            if (compressed.length < source.length) await writeFile(`${file}.${extension}`, compressed);
          }
        }
      },
    },
  };
}
