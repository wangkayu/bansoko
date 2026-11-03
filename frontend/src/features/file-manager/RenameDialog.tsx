import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { submitFileManagerOperation, toUserMessage } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { fileManagerKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, Field, Input } from "../../ui/base";
import { readSource, targetOccupancy, useIdempotencyKeys } from "./batch";
import { ConflictDialog } from "./ConflictDialog";
import { joinPath } from "./paths";
import type { BatchOutcome } from "./TransferDialog";

// RenameDialog — the plain rename: an ordinary single-file mv that edits the
// whole filename (extension included) inside the file's own folder. No
// recognition, no metadata: what is typed is the target name. The target is
// checked before submitting; an occupied name becomes the skip / version /
// replace / cancel choice (replace only for a plain file on disk), and cancel
// leaves the file untouched.
export function RenameDialog({
  path,
  directory,
  name,
  isDirectory,
  siblingNames,
  onClose,
  onFinished,
}: {
  path: string;
  directory: string;
  name: string;
  /** A folder never overwrites an occupied target. */
  isDirectory?: boolean;
  /** Names already in the folder, so an equal-but-different target is caught before the server. */
  siblingNames: readonly string[];
  onClose: () => void;
  onFinished: (outcome: BatchOutcome) => void;
}) {
  const queryClient = useQueryClient();
  const idempotency = useIdempotencyKeys();
  const inputRef = useRef<HTMLInputElement>(null);
  const [value, setValue] = useState(name);
  const [edited, setEdited] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<{ path: string; overwritable: boolean } | null>(null);

  useEffect(() => {
    const input = inputRef.current;
    if (input) {
      input.focus();
      input.select();
    }
  }, []);

  const trimmed = value.trim();
  const problem: "empty" | "invalid" | "same" | "taken" | null = !trimmed
    ? "empty"
    : /[\\/]/.test(trimmed) || trimmed === "." || trimmed === ".."
      ? "invalid"
      : trimmed === name
        ? "same"
        : siblingNames.includes(trimmed)
          ? "taken"
          : null;
  const message =
    problem === null
      ? null
      : t(
          problem === "empty"
            ? "fileManager.renameDialog.nameEmpty"
            : problem === "invalid"
              ? "fileManager.renameDialog.nameInvalid"
              : problem === "same"
                ? "fileManager.renameDialog.nameSame"
                : "fileManager.renameDialog.nameTaken",
        );
  // The field opens on the current name, so "same as the current filename" is
  // the truth before the owner has typed anything and is not worth showing yet.
  const showMessage = problem !== null && (problem !== "same" || edited);
  // A name a sibling already uses is not a dead end: submitting checks the
  // target again and offers skip, version or replace.
  const canSubmit = (problem === null || problem === "taken") && !busy;

  async function run(choice: "block" | "version" | "overwrite") {
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    try {
      const source = await readSource(path);
      await submitFileManagerOperation(
        {
          operation: "rename",
          path,
          expected_revision: source.revision,
          target: { path: joinPath(directory, trimmed), conflict: choice },
        },
        idempotency(path),
      );
      void queryClient.invalidateQueries({ queryKey: fileManagerKeys.all });
      onFinished({ submitted: 1, skipped: 0, failed: 0 });
    } catch (cause) {
      setError(toUserMessage(cause, t("common.operationFailed")));
    } finally {
      setBusy(false);
    }
  }

  async function submit() {
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    try {
      const target = joinPath(directory, trimmed);
      const occupancy = target === path ? "free" : await targetOccupancy(target);
      setBusy(false);
      if (occupancy === "free") await run("block");
      else setConflict({ path: target, overwritable: occupancy === "file" && !isDirectory });
    } catch (cause) {
      setBusy(false);
      setError(toUserMessage(cause, t("common.operationFailed")));
    }
  }

  return (
    <>
      <Dialog
        open
        tier="S"
        blocking
        title={t("fileManager.renameDialog.title")}
        description={t("fileManager.renameDialog.sub")}
        onRequestClose={busy ? () => {} : onClose}
        footer={
          <>
            <Button onClick={onClose} disabled={busy}>
              {t("act.cancel")}
            </Button>
            <Button variant="primary" loading={busy} disabled={!canSubmit} onClick={() => void submit()}>
              {t("fileManager.renameDialog.submit")}
            </Button>
          </>
        }
      >
        <div className="tw:grid tw:gap-3">
          <Field label={t("fileManager.renameDialog.currentFile")}>
            <span className="tw:break-all tw:text-sm">{name}</span>
          </Field>
          <Field label={t("fileManager.renameDialog.newName")} hint={t("fileManager.renameDialog.hint")}>
            <Input
              ref={inputRef}
              aria-label={t("fileManager.renameDialog.newName")}
              value={value}
              onChange={(event) => {
                setValue(event.target.value);
                setEdited(true);
              }}
              aria-invalid={showMessage ? true : undefined}
              autoComplete="off"
              spellCheck={false}
              disabled={busy}
            />
          </Field>
          {showMessage && message ? <p className="tw:m-0 tw:text-xs tw:text-destructive">{message}</p> : null}
          {error ? <Alert tone="error" title={error} /> : null}
        </div>
      </Dialog>
      {conflict !== null ? (
        <ConflictDialog
          targets={[conflict.path]}
          busy={busy}
          onCancel={() => setConflict(null)}
          onSkip={() => {
            setConflict(null);
            onFinished({ submitted: 0, skipped: 1, failed: 0 });
          }}
          onVersion={() => {
            setConflict(null);
            void run("version");
          }}
          onOverwrite={
            conflict.overwritable
              ? () => {
                  setConflict(null);
                  void run("overwrite");
                }
              : undefined
          }
        />
      ) : null}
    </>
  );
}
