import { useRef, useState } from "react";
import {
  submitFileManagerOperation,
  type FileManagerEntryDetail,
  type FileManagerOperationRequest,
  type FileManagerOperationView,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { Alert, Button, Dialog, Field, Input } from "../../ui/base";
import { ConflictActions, ConflictNotice } from "./ConflictChoice";
import { baseName, joinPath, normalizePath, parentOf } from "./paths";
import { useWriteFlow } from "./useWriteFlow";

// FileOperationDialog — the small confirm dialog for move / rename / copy (design sections 4,
//   4.1 and 10). Move and copy take an absolute target path; rename changes only the file name,
//   leaving the folder alone. The server only requires the target to be an absolute path, not the
//   same file as the original, with an existing parent directory — no location restriction. Before
//   submitting, check the target: when taken, choose "cancel / add a suffix / rename" in the same
//   dialog; "overwrite" is never offered.

export type CopyMoveRename = "move" | "rename" | "copy";

export function FileOperationDialog({
  detail,
  operation,
  onClose,
  onDone,
}: {
  detail: FileManagerEntryDetail;
  operation: CopyMoveRename;
  onClose: () => void;
  onDone: (view: FileManagerOperationView) => void;
}) {
  const source = detail.entry.path;
  const [value, setValue] = useState(operation === "rename" ? baseName(source) : source);
  const flow = useWriteFlow();
  const inputRef = useRef<HTMLInputElement>(null);

  const text = value.trim();
  // Rename changes only the file name (the folder stays); move and copy take an absolute target path, normalized here under the server's same admission rules.
  const targetPath = operation === "rename" ? joinPath(parentOf(source), text) : normalizePath(text);
  const invalid =
    text === ""
      ? operation === "rename"
        ? t("fileManager.dialog.nameEmpty")
        : t("fileManager.dialog.targetEmpty")
      : operation === "rename" && text.includes("/")
        ? t("fileManager.dialog.nameInvalid")
        : targetPath === undefined
          ? t("fileManager.dialog.targetAbsolute")
          : targetPath === source
            ? t("fileManager.dialog.unchanged")
            : null;

  const send = async (conflict: "block" | "version") => {
    if (targetPath === undefined) return;
    const body: FileManagerOperationRequest = {
      operation,
      path: source,
      expected_revision: detail.revision,
      target: { path: targetPath, conflict },
    };
    const result = await flow.submit(body, (key) => submitFileManagerOperation(body, key));
    if (result.ok) onDone(result.value);
  };

  const submit = async () => {
    if (targetPath === undefined) return;
    const check = await flow.checkTarget(targetPath);
    if (check === "error") return;
    if (check === "occupied") {
      flow.markConflict();
      return;
    }
    await send("block");
  };

  const rename = () => {
    flow.reset();
    inputRef.current?.focus();
    inputRef.current?.select();
  };

  const pending = flow.busy || flow.checking;

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="S"
      blocking
      title={t(`fileManager.dialog.title.${operation}`)}
      footer={
        flow.conflict ? (
          <ConflictActions busy={flow.busy} onCancel={onClose} onVersion={() => void send("version")} onRename={rename} />
        ) : (
          <>
            <Button onClick={onClose} disabled={flow.busy}>
              {t("act.cancel")}
            </Button>
            <Button variant="primary" onClick={() => void submit()} loading={pending} disabled={invalid !== null}>
              {t(`fileManager.dialog.submit.${operation}`)}
            </Button>
          </>
        )
      }
    >
      <form
        className="tw:grid tw:gap-3 tw:text-sm"
        onSubmit={(event) => {
          event.preventDefault();
          if (!flow.conflict && invalid === null && !pending) void submit();
        }}
      >
        <span>{t(`fileManager.dialog.line1.${operation}`)}</span>
        <span className="tw:text-muted-foreground">{t(`fileManager.dialog.line2.${operation}`)}</span>
        <Field label={operation === "rename" ? t("fileManager.dialog.newName") : t("fileManager.dialog.targetPath")}>
          <Input
            ref={inputRef}
            value={value}
            onChange={(event) => {
              setValue(event.target.value);
              flow.reset();
            }}
            disabled={flow.busy}
            aria-invalid={invalid !== null && text !== "" ? true : undefined}
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
        {invalid && text !== "" ? <span className="tw:text-xs tw:text-muted-foreground">{invalid}</span> : null}
        {flow.conflict ? <ConflictNotice /> : null}
        {flow.error ? <Alert tone="error" title={flow.error} /> : null}
      </form>
    </Dialog>
  );
}
