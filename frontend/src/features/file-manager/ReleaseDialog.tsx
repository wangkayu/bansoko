import { useRef, useState } from "react";
import {
  releaseFileManagerEntry,
  type FileManagerEntryDetail,
  type FileManagerReleaseRequest,
  type FileManagerReleaseResult,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { Alert, Button, Checkbox, Dialog, Field, Input } from "../../ui/base";
import { ConflictActions, ConflictNotice } from "./ConflictChoice";
import { normalizePath } from "./paths";
import { useWriteFlow } from "./useWriteFlow";

// ReleaseDialog — releases a single file from management (design section 5). The file stays in
//   place; while the subscription remains valid the episode may be fetched again (Q1).
//   The optional "also move to a new path": within the same confirmation, release first and then
//   move the file to the entered absolute path; when the target is taken, choose "cancel / add a
//   suffix / rename" in the dialog, exactly like move.

export function ReleaseDialog({
  detail,
  onClose,
  onDone,
}: {
  detail: FileManagerEntryDetail;
  onClose: () => void;
  onDone: (result: FileManagerReleaseResult) => void;
}) {
  const source = detail.entry.path;
  const [moveOn, setMoveOn] = useState(false);
  const [value, setValue] = useState(source);
  const flow = useWriteFlow();
  const inputRef = useRef<HTMLInputElement>(null);

  const text = value.trim();
  // The target is likewise an absolute path, normalized first under the server's same admission rules.
  const target = normalizePath(text);
  const invalid = !moveOn
    ? null
    : text === ""
      ? t("fileManager.dialog.targetEmpty")
      : target === undefined
        ? t("fileManager.dialog.targetAbsolute")
        : target === source
          ? t("fileManager.dialog.unchanged")
          : null;
  const reacquire = detail.anime ? !detail.anime.ended : false;

  const send = async (conflict: "block" | "version") => {
    if (moveOn && target === undefined) return;
    const body: FileManagerReleaseRequest = {
      path: source,
      expected_revision: detail.revision,
      ...(moveOn ? { move_to: { path: target!, conflict } } : {}),
    };
    const result = await flow.submit(body, (key) => releaseFileManagerEntry(body, key));
    if (result.ok) onDone(result.value);
  };

  const submit = async () => {
    if (moveOn) {
      if (target === undefined) return;
      const check = await flow.checkTarget(target);
      if (check === "error") return;
      if (check === "occupied") {
        flow.markConflict();
        return;
      }
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
      title={t("fileManager.dialog.title.release")}
      footer={
        flow.conflict ? (
          <ConflictActions busy={flow.busy} onCancel={onClose} onVersion={() => void send("version")} onRename={rename} />
        ) : (
          <>
            <Button onClick={onClose} disabled={flow.busy}>
              {t("act.cancel")}
            </Button>
            <Button variant="primary" onClick={() => void submit()} loading={pending} disabled={invalid !== null}>
              {moveOn ? t("fileManager.dialog.submit.releaseAndMove") : t("fileManager.dialog.submit.release")}
            </Button>
          </>
        )
      }
    >
      <div className="tw:grid tw:gap-3 tw:text-sm">
        <span>{t("fileManager.dialog.line1.release")}</span>
        <span className="tw:text-muted-foreground">{t("fileManager.dialog.line2.release")}</span>
        {reacquire ? <Alert tone="warning" title={t("fileManager.dialog.releaseReacquire")} /> : null}
        <Checkbox
          checked={moveOn}
          onCheckedChange={(next) => {
            setMoveOn(next);
            flow.reset();
          }}
          disabled={flow.busy}
        >
          {t("fileManager.dialog.moveAlso")}
        </Checkbox>
        {moveOn ? (
          <Field label={t("fileManager.dialog.moveTo")}>
            <Input
              ref={inputRef}
              value={value}
              onChange={(event) => {
                setValue(event.target.value);
                flow.reset();
              }}
              disabled={flow.busy}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
        ) : null}
        {moveOn && invalid && text !== "" ? <span className="tw:text-xs tw:text-muted-foreground">{invalid}</span> : null}
        {flow.conflict ? <ConflictNotice /> : null}
        {flow.error ? <Alert tone="error" title={flow.error} /> : null}
      </div>
    </Dialog>
  );
}
