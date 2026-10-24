import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  alignFileManagerEntry,
  submitFileManagerOperation,
  submitReconciliationScan,
  toUserMessage,
  type FileManagerEntryDetail,
  type FileManagerOperation,
  type FileManagerOperationView,
} from "../../lib/api/client";
import { joinList } from "../../lib/i18n/format";
import { formatDateTime } from "../../lib/labels";
import { t, tryT } from "../../lib/i18n";
import { fileManagerKeys } from "../../lib/queryKeys";
import { Button, StatePill, Tooltip } from "../../ui/base";
import { toast } from "../../ui/toast";
import { ConfirmWriteDialog } from "./ConfirmWriteDialog";
import { FileOperationDialog } from "./FileOperationDialog";
import { MatchDialog } from "./MatchDialog";
import { ReleaseDialog } from "./ReleaseDialog";

// EntryOperations — the operation buttons in the details panel and the most recent user operation.
//   Button order and availability both come from `detail.operations` (derived by the server from
//   state); on submit the server re-judges the write action and answers 409 when the state moved;
//   after a refresh the buttons re-render from the new availability. Each write action stacks at
//   most one small confirm dialog.

type DialogKind = "move" | "rename" | "copy" | "delete" | "release" | "align" | "match" | null;

const SECTION_TITLE = "tw:m-0 tw:mb-2 tw:flex tw:items-center tw:gap-1 tw:text-sm tw:font-semibold";
const META = "tw:m-0 tw:text-xs tw:text-muted-foreground";

export function EntryOperations({
  detail,
  onOperation,
  initialDialog,
  onDialogClosed,
}: {
  detail: FileManagerEntryDetail;
  /** A user operation was accepted (move, rename, …); the panel uses this to follow the file to its new location after it is moved away. */
  onOperation?: (view: FileManagerOperationView) => void;
  /** The operation dialog a link asked to open directly; it does not open when that operation is currently unavailable. */
  initialDialog?: "match";
  /** The dialog closed (including by an operation being accepted). */
  onDialogClosed?: () => void;
}) {
  const queryClient = useQueryClient();
  const [dialog, setDialog] = useState<DialogKind>(() =>
    initialDialog && detail.operations.some((rule) => rule.operation === initialDialog && rule.allowed) ? initialDialog : null,
  );
  const [scanning, setScanning] = useState(false);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: fileManagerKeys.all });
  const close = () => {
    setDialog(null);
    onDialogClosed?.();
  };
  const accepted = (view: FileManagerOperationView) => {
    close();
    refresh();
    onOperation?.(view);
    toast.success(
      view.state === "completed"
        ? t("fileManager.operationCompleted")
        : view.operation === "match"
          ? t("fileManager.matchAccepted")
          : t("fileManager.operationAccepted"),
    );
  };

  const scan = async () => {
    if (!detail.anime) return;
    setScanning(true);
    try {
      await submitReconciliationScan(detail.anime.managed_anime_id);
      toast.success(t("fileManager.scanStarted"));
      refresh();
    } catch (cause) {
      toast.error(toUserMessage(cause, t("common.operationFailed")));
    } finally {
      setScanning(false);
    }
  };

  const act = (operation: FileManagerOperation) => {
    if (operation === "scan") void scan();
    else setDialog(operation);
  };

  const deleteBody = { operation: "delete", path: detail.entry.path, expected_revision: detail.revision } as const;
  const alignBody = { path: detail.entry.path, expected_revision: detail.revision };

  return (
    <>
      {detail.operations.length > 0 ? (
        <section aria-label={t("fileManager.operationsHeading")}>
          <h4 className={SECTION_TITLE}>{t("fileManager.operationsHeading")}</h4>
          <div className="tw:flex tw:flex-wrap tw:gap-2">
            {detail.operations.map((rule) => {
              const label = t(`fileManager.operation.${rule.operation}`);
              if (rule.allowed) {
                return (
                  <Button
                    key={rule.operation}
                    size="sm"
                    variant={rule.operation === "delete" ? "danger" : "secondary"}
                    loading={rule.operation === "scan" && scanning}
                    disabled={rule.operation === "scan" && !detail.anime}
                    onClick={() => act(rule.operation)}
                  >
                    {label}
                  </Button>
                );
              }
              const reason = rule.blocked_reason ? tryT(`fileManager.operationBlock.${rule.blocked_reason}`) : undefined;
              // Disabled buttons receive neither hover nor focus, so the surrounding span carries the tooltip.
              const button = (
                <Button size="sm" disabled className="tw:pointer-events-none">
                  {label}
                </Button>
              );
              return reason ? (
                <Tooltip key={rule.operation} content={reason}>
                  <span tabIndex={0} className="tw:inline-flex">
                    {button}
                  </span>
                </Tooltip>
              ) : (
                <span key={rule.operation} className="tw:inline-flex">
                  {button}
                </span>
              );
            })}
          </div>
        </section>
      ) : null}
      <LastOperation view={detail.last_operation} />

      {dialog === "move" || dialog === "rename" || dialog === "copy" ? (
        <FileOperationDialog detail={detail} operation={dialog} onClose={close} onDone={accepted} />
      ) : null}
      {dialog === "match" ? <MatchDialog detail={detail} onClose={close} onDone={accepted} /> : null}
      {dialog === "delete" ? (
        <ConfirmWriteDialog
          title={t("fileManager.dialog.title.delete")}
          line1={t("fileManager.dialog.line1.delete")}
          line2={t("fileManager.dialog.line2.delete")}
          submitLabel={t("fileManager.dialog.submit.delete")}
          danger
          body={deleteBody}
          send={(key) => submitFileManagerOperation(deleteBody, key)}
          onClose={close}
          onDone={accepted}
        />
      ) : null}
      {dialog === "align" ? (
        <ConfirmWriteDialog
          title={t("fileManager.dialog.title.align")}
          line1={t("fileManager.dialog.line1.align")}
          line2={t("fileManager.dialog.line2.align")}
          submitLabel={t("fileManager.dialog.submit.align")}
          body={alignBody}
          send={(key) => alignFileManagerEntry(alignBody, key)}
          onClose={close}
          onDone={() => {
            close();
            refresh();
            toast.success(t("fileManager.alignAccepted"));
          }}
        />
      ) : null}
      {dialog === "release" ? (
        <ReleaseDialog
          detail={detail}
          onClose={close}
          onDone={(result) => {
            close();
            refresh();
            if (result.operation) {
              onOperation?.(result.operation);
              toast.success(t("fileManager.releasedMoving"));
            } else {
              toast.success(t("fileManager.released"));
            }
          }}
        />
      ) : null}
    </>
  );
}

function LastOperation({ view }: { view?: FileManagerOperationView }) {
  if (!view) return null;
  const bad = view.state === "blocked" || view.state === "failed";
  const failure = (view.failure ? tryT(`fileManager.operationFailure.${view.failure}`) : undefined) ?? t("fileManager.operationFailedToast");
  return (
    <section aria-label={t("fileManager.lastOperation.label")}>
      <h4 className={SECTION_TITLE}>{t("fileManager.lastOperation.label")}</h4>
      <div className="tw:grid tw:gap-1 tw:text-sm">
        <span className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1">
          <span>{t(`fileManager.operation.${view.operation}`)}</span>
          <StatePill tone={bad ? "warning" : view.state === "completed" ? "online" : "info"}>{t(`fileManager.operationState.${view.state}`)}</StatePill>
          <span className={META}>{formatDateTime(view.completed_at ?? view.created_at)}</span>
        </span>
        {bad ? <span>{failure}</span> : null}
        {view.state === "completed" && view.result_path ? (
          <span className={`${META} tw:break-all`}>{t("fileManager.lastOperation.result", { path: view.result_path })}</span>
        ) : null}
        {view.created_directories && view.created_directories.length > 0 ? (
          <span className={`${META} tw:break-all`}>{t("fileManager.lastOperation.createdFolders", { paths: joinList(view.created_directories) })}</span>
        ) : null}
      </div>
    </section>
  );
}
