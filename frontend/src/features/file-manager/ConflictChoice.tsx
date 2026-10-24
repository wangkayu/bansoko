import { t } from "../../lib/i18n";
import { Alert, Button } from "../../ui/base";

// Target-busy choices (design section 4.1): cancel / add a suffix / rename. User-initiated actions never offer "overwrite".

export function ConflictNotice() {
  return <Alert tone="warning" title={t("fileManager.conflict.title")} description={t("fileManager.conflict.hint")} />;
}

export function ConflictActions({
  busy,
  onCancel,
  onVersion,
  onRename,
}: {
  busy: boolean;
  onCancel: () => void;
  onVersion: () => void;
  onRename: () => void;
}) {
  return (
    <>
      <Button onClick={onCancel} disabled={busy}>
        {t("fileManager.conflict.cancel")}
      </Button>
      <Button onClick={onRename} disabled={busy}>
        {t("fileManager.conflict.rename")}
      </Button>
      <Button variant="primary" onClick={onVersion} loading={busy}>
        {t("fileManager.conflict.version")}
      </Button>
    </>
  );
}
