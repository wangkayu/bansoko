import { useRef, useState } from "react";
import { ApiError, cancelAcquisitionDownload, toUserMessage } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { Alert, Button, Dialog } from "../../ui/base";

// DownloadCancelConfirmDialog — the "cancel download" confirm dialog on a running execution record
//   whose download has not reached import (the server reports cancel_available). Confirming deletes
//   the qBittorrent task with its downloaded data; a subscription episode moves to another release
//   and can be retried from the RSS list, a one-shot download ends for good. The dialog only calls
//   the official API and derives no state; the caller refreshes records and RSS lists on success.

export function DownloadCancelConfirmDialog({
  acquisitionID,
  version,
  oneShot,
  onClose,
  onAccepted,
}: {
  acquisitionID: string;
  /** The record version from the details, sent as If-Match; a changed record makes the server answer 409. */
  version: number;
  /** A one-shot record has no retry after the cancel. */
  oneShot: boolean;
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
      await cancelAcquisitionDownload(acquisitionID, version, idempotencyKeyRef.current);
      setAccepted(true);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status >= 400 && cause.status < 500) {
        idempotencyKeyRef.current = newIdempotencyKey();
      }
      // A stale version: the record changed since it was read; the generic edit wording does not fit.
      if (cause instanceof ApiError && cause.code === "OWNER_VERSION_CONFLICT") {
        setError(t("records.downloadCancel.stale"));
      } else {
        setError(toUserMessage(cause, t("common.operationFailed")));
      }
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
        title={t("records.downloadCancel.title")}
        footer={<Button onClick={finish}>{t("records.downloadCancel.ok")}</Button>}
      >
        <Alert tone="success" title={t("records.downloadCancel.accepted")} />
      </Dialog>
    );
  }

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="S"
      blocking
      title={t("records.downloadCancel.title")}
      footer={
        <>
          <Button onClick={onClose} disabled={submitting}>
            {t("act.close")}
          </Button>
          <Button variant="danger" onClick={() => void submit()} disabled={submitting} data-testid="download-cancel-confirm">
            {t("records.downloadCancel.submit")}
          </Button>
        </>
      }
    >
      <div className="tw:grid tw:gap-3 tw:text-sm">
        <span>{t(oneShot ? "records.downloadCancel.oneShotLine1" : "records.downloadCancel.line1")}</span>
        <span className="tw:text-muted-foreground">
          {t(oneShot ? "records.downloadCancel.oneShotLine2" : "records.downloadCancel.line2")}
        </span>
        {error ? <Alert tone="error" title={error} /> : null}
      </div>
    </Dialog>
  );
}

function newIdempotencyKey(): string {
  const random = Math.random().toString(36).slice(2, 10);
  return `download-cancel-${Date.now().toString(36)}-${random}`;
}
