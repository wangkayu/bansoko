import { useRef } from "react";
import { ApiError, getFileManagerEntry, newUUID } from "../../lib/api/client";

// Batch helpers for the browse layer's selection actions. A selection acts on
// several files at once, while the write API stays single-file: each file is
// read (for the revision its write must confirm) and submitted on its own, and
// the batch stops for nothing — one file's typed refusal is a row in the
// summary, not a rollback of the others.

/** One selected file with the revision its next write must confirm. */
export type SourceFile = { path: string; name: string; revision: string; sizeBytes?: number };

export async function readSource(path: string): Promise<SourceFile> {
  const detail = await getFileManagerEntry(path);
  return { path, name: detail.entry.name, revision: detail.revision, sizeBytes: detail.entry.size_bytes };
}

/** What occupies a target path: nothing, a plain file (replaceable), or a file the product manages or holds. */
export type TargetOccupancy = "free" | "file" | "locked";

/** Reads a target path's occupancy; a 404 (path absent) means free. */
export async function targetOccupancy(path: string): Promise<TargetOccupancy> {
  try {
    const detail = await getFileManagerEntry(path);
    return detail.entry.locked ? "locked" : "file";
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 404 && cause.code === "FILE_MANAGER_PATH_NOT_FOUND") return "free";
    throw cause;
  }
}

/** Per-key idempotency keys: a retry of the same file reuses its key, so the server replays instead of duplicating. */
export function useIdempotencyKeys() {
  const keys = useRef(new Map<string, string>());
  return (key: string) => {
    let value = keys.current.get(key);
    if (!value) {
      value = newUUID();
      keys.current.set(key, value);
    }
    return value;
  };
}
