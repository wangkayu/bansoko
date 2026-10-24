import { useRef, useState } from "react";
import { ApiError, retryAcquisitionImport, toUserMessage } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { Alert, Button, Dialog } from "../../ui/base";

// ImportRetryConfirmDialog — the "retry" confirm dialog for a failed import stage on an execution
//   record. It is the only repair on the read-only record surface: re-runs every failed file of
//   this record, leaves imported files untouched, re-does no earlier stage and never re-downloads.
//   The dialog only calls the official API and derives no state; the caller refreshes records on
//   success.

export function ImportRetryConfirmDialog({
  acquisitionID,
  version,
  onClose,
  onAccepted,
}: {
  acquisitionID: string;
  /** The record version from the details, sent as If-Match; a changed record makes the server answer 409. */
  version: number;
  onClose: () => void;
  onAccepted: () => void;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [accepted, setAccepted] = useState(false);

  // While the outcome is unknown (network break/timeout) the idempotency key stays unchanged and a
  //   second click makes the server replay the original result under the same key; only an explicit
  //   server answer (4xx — the outcome is known and did not take effect) takes a new key. The dialog
  //   remounts on every open.
  const idempotencyKeyRef = useRef<string>(newIdempotencyKey());

  const submit = async () => {
    setSubmitting(true);
    setError(null);
    try {
      await retryAcquisitionImport(acquisitionID, version, idempotencyKeyRef.current);
      setAccepted(true);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status >= 400 && cause.status < 500) {
        idempotencyKeyRef.current = newIdempotencyKey();
      }
      setError(toUserMessage(cause, t("common.operationFailed")));
    } finally {
      setSubmitting(false);
    }
  };

  const finish = () => {
    onAccepted();
    onClose();
  };

  if (accepted) {
    return (
      <Dialog
        open
        onRequestClose={finish}
        tier="S"
        blocking
        title={t("records.importRetry.title")}
        footer={<Button onClick={finish}>{t("records.importRetry.ok")}</Button>}
      >
        <Alert tone="success" title={t("records.importRetry.accepted")} />
      </Dialog>
    );
  }

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="S"
      blocking
      title={t("records.importRetry.title")}
      footer={
        <>
          <Button onClick={onClose} disabled={submitting}>
            {t("act.cancel")}
          </Button>
          <Button onClick={() => void submit()} disabled={submitting} data-testid="import-retry-confirm">
            {t("records.importRetry.submit")}
          </Button>
        </>
      }
    >
      <div className="tw:grid tw:gap-3 tw:text-sm">
        <span>{t("records.importRetry.line1")}</span>
        <span className="tw:text-muted-foreground">{t("records.importRetry.line2")}</span>
        {error ? <Alert tone="error" title={error} /> : null}
      </div>
    </Dialog>
  );
}

function newIdempotencyKey(): string {
  const random = Math.random().toString(36).slice(2, 10);
  return `import-retry-${Date.now().toString(36)}-${random}`;
}
