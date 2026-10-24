import type { ReactNode } from "react";
import { t } from "../../lib/i18n";
import { Alert, Button, Dialog } from "../../ui/base";
import { useWriteFlow, type SubmitResult } from "./useWriteFlow";

// ConfirmWriteDialog — write actions that need only confirmation, such as delete and align: two lines of explanation plus Cancel / action button.

export function ConfirmWriteDialog<T>({
  title,
  line1,
  line2,
  submitLabel,
  danger = false,
  body,
  send,
  onClose,
  onDone,
}: {
  title: string;
  line1: string;
  line2: string;
  submitLabel: string;
  danger?: boolean;
  /** The request content, deciding when the idempotency key is kept and when it is replaced. */
  body: unknown;
  send: (idempotencyKey: string) => Promise<T>;
  onClose: () => void;
  onDone: (value: T) => void;
}): ReactNode {
  const flow = useWriteFlow();
  const submit = async () => {
    const result: SubmitResult<T> = await flow.submit(body, send);
    if (result.ok) onDone(result.value);
  };
  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="S"
      blocking
      title={title}
      footer={
        <>
          <Button onClick={onClose} disabled={flow.busy}>
            {t("act.cancel")}
          </Button>
          <Button variant={danger ? "danger" : "primary"} onClick={() => void submit()} loading={flow.busy}>
            {submitLabel}
          </Button>
        </>
      }
    >
      <div className="tw:grid tw:gap-3 tw:text-sm">
        <span>{line1}</span>
        <span className="tw:text-muted-foreground">{line2}</span>
        {flow.error ? <Alert tone="error" title={flow.error} /> : null}
      </div>
    </Dialog>
  );
}
