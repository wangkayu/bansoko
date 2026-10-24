import { useDirtyConfirm } from "../../ui";
import { Button, Dialog } from "../../ui/base";
import { t } from "../../lib/i18n";

// The settings edit's shared Dialog: the M size (title / scrolling form / fixed action bar provided
//   by the ui/base Dialog). When opened from the settings dialog (the F size) it lands on the
//   secondary layer (1100), with the dirty confirmation above it (1200).

export function SettingsEditDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  dirty = false,
  onDiscard,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: React.ReactNode;
  footer?: React.ReactNode | ((requestClose: () => void) => React.ReactNode);
  dirty?: boolean | (() => boolean);
  onDiscard?: () => void;
}) {
  const { requestClose, confirmDialog } = useDirtyConfirm({
    dirty,
    onClose: () => {
      onDiscard?.();
      onOpenChange(false);
    },
    description: t("settings.edit.discardConfirm"),
    confirmLabel: t("common.discardChanges"),
    danger: true,
  });

  return (
    <>
      <Dialog
        open={open}
        tier="M"
        onRequestClose={requestClose}
        title={title}
        description={description}
        footer={
          typeof footer === "function" ? footer(requestClose) : footer ?? (
            <Button variant="ghost" onClick={requestClose}>
              {t("settings.edit.close")}
            </Button>
          )
        }
      >
        {children}
      </Dialog>
      {confirmDialog}
    </>
  );
}
