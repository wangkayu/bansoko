import { StateBlock } from "./StateBlock";
import { Button } from "./base/controls";
import { t } from "../lib/i18n";

// ConflictBlock — the single surface for CAS save conflicts (409): the server's
// current version + a note that local edits are kept + a "refresh to the latest"
// button (mirroring the refresh interaction of MetadataMatchDialog). The caller
// re-reads the data and clears the conflict state in onRefresh.

export function ConflictBlock({
  currentVersion,
  message,
  onRefresh,
}: {
  currentVersion: number;
  message?: string;
  /** When given, shows the "refresh to the latest" button; the caller re-reads and clears the conflict state on click. */
  onRefresh?: () => void;
}) {
  return (
    <StateBlock
      kind="error"
      title={t("common.versionConflict")}
      description={
        <span>
          {message && <span style={{ display: "block" }}>{message}</span>}
          {t("common.conflictHint")}
        </span>
      }
      action={
        onRefresh && (
          <Button size="sm" onClick={onRefresh}>
            {t("common.refreshLatest")}
          </Button>
        )
      }
    />
  );
}
