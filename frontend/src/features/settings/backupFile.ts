// The backup file header: a signature line, a JSON metadata line, then the database archive.
//   The browser reads only the first few KB — enough to show the backup time before upload and to
//   reject a file whose version mismatches early; the server still verifies in full.

import { ApiError, getBackupInfo } from "../../lib/api/client";

const MAGIC = "BANSOKO-BACKUP/1\n";
const MAX_HEAD_BYTES = MAGIC.length + 4096 + 1;

export type BackupFileHeader = {
  /** The data version, e.g. "038". */
  schemaNumber: string;
  createdAt: string;
};

export async function readBackupFileHeader(file: Blob): Promise<BackupFileHeader | null> {
  let bytes: Uint8Array;
  try {
    bytes = new Uint8Array(await file.slice(0, MAX_HEAD_BYTES).arrayBuffer());
  } catch {
    return null;
  }
  const magic = new TextEncoder().encode(MAGIC);
  if (bytes.length <= magic.length || magic.some((byte, index) => bytes[index] !== byte)) return null;
  const end = bytes.indexOf(0x0a, magic.length);
  if (end < 0) return null;
  let header: unknown;
  try {
    header = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(magic.length, end)));
  } catch {
    return null;
  }
  if (!header || typeof header !== "object") return null;
  const record = header as Record<string, unknown>;
  if (record.app !== "bansoko" || record.format !== 1) return null;
  const version = typeof record.schema_version === "string" ? /^([0-9]{3,6})_/.exec(record.schema_version) : null;
  if (!version || typeof record.created_at !== "string") return null;
  return { schemaNumber: version[1], createdAt: record.created_at };
}

/** Lets the browser download directly (streamed to disk, never through page memory). A same-origin
 *  link with the download attribute only downloads and never leaves the page. */
export function startDownload(url: string, fileName: string): void {
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = fileName;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
}

// After a restore is accepted the service switches its data and restarts; poll until the outcome can be judged.
const RESTART_POLL_MS = 2000;
const RESTART_TIMEOUT_MS = 10 * 60 * 1000;

export type RestartOutcome = "restored" | "unchanged" | "timeout";

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/** Waits for the restart. A 401 means the restored data is live (its sessions
 *  were cleared); the old service answering again after it went away means the
 *  switch did not happen. */
export async function waitForRestart(pollMs = RESTART_POLL_MS, timeoutMs = RESTART_TIMEOUT_MS): Promise<RestartOutcome> {
  let sawDown = false;
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await sleep(pollMs);
    try {
      await getBackupInfo();
      if (sawDown) return "unchanged";
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 401) return "restored";
      sawDown = true;
    }
  }
  return "timeout";
}
