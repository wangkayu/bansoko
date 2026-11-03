import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { submitFileManagerOperation, toUserMessage } from "../../lib/api/client";
import { formatBytes } from "../../lib/labels";
import { t } from "../../lib/i18n";
import { fileManagerKeys } from "../../lib/queryKeys";
import { Button, Dialog } from "../../ui/base";
import { readSource, useIdempotencyKeys } from "./batch";
import type { BatchOutcome } from "./TransferDialog";

// DeleteDialog — physically deletes the selection, no recycle bin. The entries
// and their sizes are listed and confirmed once; a folder row names how many
// items it holds instead of a size. Each entry is deleted on its own, and one
// entry's refusal is a row in the summary, not a rollback.
export function DeleteDialog({
  files,
  onClose,
  onFinished,
}: {
  files: readonly { path: string; name: string; sizeBytes?: number; directory?: boolean; childCount?: number }[];
  onClose: () => void;
  onFinished: (outcome: BatchOutcome) => void;
}) {
  const queryClient = useQueryClient();
  const idempotency = useIdempotencyKeys();
  const [busy, setBusy] = useState(false);

  const total = files.reduce((sum, file) => sum + (file.directory ? 0 : (file.sizeBytes ?? 0)), 0);
  const hasDirectory = files.some((file) => file.directory);

  async function run() {
    if (busy) return;
    setBusy(true);
    const outcome: BatchOutcome = { submitted: 0, skipped: 0, failed: 0 };
    const failures: string[] = [];
    for (const file of files) {
      try {
        const source = await readSource(file.path);
        await submitFileManagerOperation(
          { operation: "delete", path: file.path, expected_revision: source.revision },
          idempotency(file.path),
        );
        outcome.submitted++;
      } catch (cause) {
        outcome.failed++;
        failures.push(toUserMessage(cause, t("fileManager.batch.fileFailed", { name: file.name })));
      }
    }
    void queryClient.invalidateQueries({ queryKey: fileManagerKeys.all });
    if (failures.length > 0) outcome.firstFailure = failures[0];
    setBusy(false);
    onFinished(outcome);
  }

  return (
    <Dialog
      open
      tier="M"
      blocking
      title={t("fileManager.deleteDialog.title")}
      description={t("fileManager.deleteDialog.sub")}
      onRequestClose={busy ? () => {} : onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t("act.cancel")}
          </Button>
          <Button variant="danger" loading={busy} onClick={() => void run()}>
            {t("fileManager.deleteDialog.submit")}
          </Button>
        </>
      }
    >
      <div className="tw:grid tw:gap-3">
        <p className="tw:m-0 tw:text-sm">
          {t(hasDirectory ? "fileManager.deleteDialog.lineEntries" : "fileManager.deleteDialog.line", { n: files.length })}
          {total > 0 ? ` · ${formatBytes(total)}` : ""}
        </p>
        <ul className="tw:m-0 tw:grid tw:gap-1 tw:pl-4 tw:text-sm">
          {files.map((file) => (
            <li key={file.path} className="tw:flex tw:items-baseline tw:gap-2">
              <span className="tw:min-w-0 tw:flex-1 tw:break-all">{file.name}</span>
              <span className="tw:flex-none tw:text-xs tw:text-muted-foreground">
                {file.directory
                  ? file.childCount !== undefined
                    ? t("fileManager.deleteDialog.folderItems", { n: file.childCount })
                    : ""
                  : file.sizeBytes !== undefined
                    ? formatBytes(file.sizeBytes)
                    : ""}
              </span>
            </li>
          ))}
        </ul>
        <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.deleteDialog.hint")}</p>
      </div>
    </Dialog>
  );
}
