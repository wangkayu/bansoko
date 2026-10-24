import { ConfirmDialogEntry } from "../../ui/ConfirmDialogEntry";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { t } from "../../lib/i18n";
import { ConfirmLines } from "./ConfirmLines";

// LocalDataCleanupDialog — the dedicated confirmation for the destructive
// physical local-record cleanup of a retired instance. It is deliberately a
// separate dialog (not the unsubscribe confirm) because the wording and the
// blast radius are different: local rows are deleted, but qB torrents/data and
// media-library files are NEVER touched.
export function LocalDataCleanupDialog({
  entry,
  onOpenChange,
  onConfirm,
}: {
  entry: CatalogEntryView;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}) {
  return (
    <ConfirmDialogEntry
      open
      onOpenChange={onOpenChange}
      title={t("catalog.deleteLocalTitle", { title: entry.title })}
      description={
        <ConfirmLines lines={[t("grid.confirm.deleteLocalLine1"), t("grid.confirm.deleteLocalLine2")]} />
      }
      confirmLabel={t("catalog.deleteLocal")}
      danger
      onConfirm={onConfirm}
    />
  );
}
