import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// Same override contract as auth.setup.ts / playwright.config.ts: session
// scripts point this at their private marker path; the default stays the
// historical shared path.
const markerFile =
  process.env.BANSOKO_E2E_AUTH_MARKER_PATH ||
  path.join(__dirname, "../playwright/.auth/skip-auth");

export function shouldSkipAuthTests(): boolean {
  return fs.existsSync(markerFile);
}
