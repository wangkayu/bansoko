import { useCallback, useState, type ReactNode } from "react";
import { ConfirmDialog } from "./ConfirmDialog";
import { t } from "../lib/i18n";

// useDirtyConfirm — intercepts leaving while dirty: requestClose() first opens
// a ConfirmDialog ("discard unsaved edits?") when dirty and only calls onClose
// after confirmation; clean closes go straight through. Dialog close, Esc,
// section switches and every other leave path go through this hook.

export function useDirtyConfirm({
  dirty,
  onClose,
  title = t("common.discardEditsTitle"),
  description,
  confirmLabel = t("common.discardChanges"),
  danger,
}: {
  /** Dirty check; pass a getter to read the latest value when several sources (a ref rollup) feed it. */
  dirty: boolean | (() => boolean);
  onClose: () => void;
  title?: string;
  description?: string;
  confirmLabel?: string;
  danger?: boolean;
}): { requestClose: () => void; confirmDialog: ReactNode; confirmOpen: boolean } {
  const [confirmOpen, setConfirmOpen] = useState(false);

  const requestClose = useCallback(() => {
    const isDirty = typeof dirty === "function" ? dirty() : dirty;
    if (isDirty) {
      setConfirmOpen(true);
      return;
    }
    onClose();
  }, [dirty, onClose]);

  const confirmDialog = (
    <ConfirmDialog
      open={confirmOpen}
      onOpenChange={setConfirmOpen}
      title={title}
      description={description}
      confirmLabel={confirmLabel}
      danger={danger}
      onConfirm={() => {
        setConfirmOpen(false);
        onClose();
      }}
    />
  );

  return { requestClose, confirmDialog, confirmOpen };
}
