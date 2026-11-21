import { useState } from "react";
import type { AcquisitionScriptReceipt } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { formatDateTime } from "../../lib/labels";
import { Button, Dialog } from "../../ui/base";
import { DescriptionList } from "../../ui/DescriptionList";

export function ScriptReceiptLine({ receipt, context }: { receipt: AcquisitionScriptReceipt; context: string }) {
  const [open, setOpen] = useState(false);
  const { summary, exit_code, http_status } = receipt;
  return (
    <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-3 tw:gap-y-1">
      <span className="tw:text-xs tw:text-muted-foreground">{t("records.script.triggered")}</span>
      {summary && exit_code !== undefined ? (
        <>
          <Button size="sm" variant="link" onClick={() => setOpen(true)}>{t("records.script.view")}</Button>
          <Dialog open={open} onRequestClose={() => setOpen(false)} tier="S" focusTitle
            title={t("records.script.title")} description={<span className="tw:wrap-anywhere">{context}</span>}>
            <DescriptionList items={[
              { label: t("records.script.name"), value: <code className="tw:wrap-anywhere">{receipt.script}</code> },
              { label: t("records.script.startedAt"), value: formatDateTime(receipt.started_at) },
              { label: t("records.script.exitCode"), value: exit_code },
              ...(http_status !== undefined ? [{ label: t("records.script.httpStatus"), value: http_status }] : []),
              { label: t("records.script.summary"), value: <span className="tw:wrap-anywhere">{summary === "<redacted runtime message>" ? t("records.redacted") : summary}</span> },
            ]} />
          </Dialog>
        </>
      ) : null}
    </div>
  );
}
