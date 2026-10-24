import { useRef, useState } from "react";
import { ApiError, getFileManagerEntry, newUUID, toUserMessage } from "../../lib/api/client";
import { t } from "../../lib/i18n";

// useWriteFlow — the submit flow shared by the file manager's confirm dialogs.
// Idempotency key: identical request content (a retry) keeps the same key and the server replays
//   under the original key when the outcome is unknown (network break, 5xx); a changed request or
//   an explicit server rejection (4xx) takes a new key. Every confirm dialog remounts when opened,
//   so the key is fresh each time.
// Target check: first look at whether the target path is taken via the details endpoint (404 means
//   free); when taken, do not submit — hand the conflict choice to the confirm dialog.

export type TargetCheck = "free" | "occupied" | "error";
export type SubmitResult<T> = { ok: true; value: T } | { ok: false; code?: string };

export function useWriteFlow() {
  const keyRef = useRef<{ key: string; fingerprint: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);

  function keyFor(body: unknown): string {
    const fingerprint = JSON.stringify(body);
    if (!keyRef.current || keyRef.current.fingerprint !== fingerprint) {
      keyRef.current = { key: newUUID(), fingerprint };
    }
    return keyRef.current.key;
  }

  /** Whether the target is taken; a 404 (path absent) means free, any other failure shows an error. */
  async function checkTarget(path: string): Promise<TargetCheck> {
    setChecking(true);
    setError(null);
    try {
      await getFileManagerEntry(path);
      return "occupied";
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404 && cause.code === "FILE_MANAGER_PATH_NOT_FOUND") return "free";
      setError(toUserMessage(cause, t("common.operationFailed")));
      return "error";
    } finally {
      setChecking(false);
    }
  }

  async function submit<T>(body: unknown, send: (idempotencyKey: string) => Promise<T>): Promise<SubmitResult<T>> {
    const key = keyFor(body);
    setBusy(true);
    setError(null);
    try {
      return { ok: true, value: await send(key) };
    } catch (cause) {
      const code = cause instanceof ApiError ? cause.code : undefined;
      if (cause instanceof ApiError && cause.status >= 400 && cause.status < 500) keyRef.current = null;
      if (code === "FILE_MANAGER_TARGET_EXISTS") setConflict(true);
      else setError(toUserMessage(cause, t("common.operationFailed")));
      return { ok: false, code };
    } finally {
      setBusy(false);
    }
  }

  /** After editing the target, clears conflict and error so the next submit re-checks. */
  function reset() {
    setConflict(false);
    setError(null);
  }

  /** The target is taken: show the conflict choice. */
  function markConflict() {
    setError(null);
    setConflict(true);
  }

  return { busy, checking, error, conflict, checkTarget, submit, reset, markConflict };
}
