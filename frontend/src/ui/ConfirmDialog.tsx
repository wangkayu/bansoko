import { useRef } from "react";
import { Button, Dialog } from "./base";
import { t } from "../lib/i18n";

// ConfirmDialog — the single blocking-confirmation surface: dirty-close guards,
// guards, deletions and destructive actions. window.confirm is banned.
// Esc and the cancel button dismiss without confirming; backdrop press does not.
// Initial focus: the cancel button for danger confirms, else the primary one.

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = t("ui.confirm"),
  cancelLabel = t("ui.cancel"),
  danger,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
}) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  return (
    <Dialog
      open={open}
      onRequestClose={() => onOpenChange(false)}
      tier="S"
      blocking
      initialFocus={danger ? cancelRef : confirmRef}
      title={title}
      footer={
        <>
          <Button ref={cancelRef} onClick={() => onOpenChange(false)}>{cancelLabel}</Button>
          <Button
            ref={confirmRef}
            variant={danger ? "danger" : "primary"}
            onClick={() => {
              onOpenChange(false);
              onConfirm();
            }}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {description ? <div className="tw:text-sm">{description}</div> : null}
    </Dialog>
  );
}
