import { useRef, useState } from "react";
import { duplicateAnimeSubscription, newUUID, toUserMessage, type SubscribeResult } from "../../lib/api/client";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { Button, Dialog, Input } from "../../ui/base";
import { toast } from "../../ui/toast";
import { t } from "../../lib/i18n";

/** Duplicates a subscription: asks only for the subscription note (required) and creates a
 *  not-yet-started subscription from the source subscription's currently saved settings, with no
 *  overlap detection against the source. One open uses one request key, so repeated submissions
 *  yield the same single copy. On success the caller opens the new subscription's config dialog. */
export function DuplicateSubscriptionDialog({
  entry,
  onOpenChange,
  onDuplicated,
}: {
  entry: CatalogEntryView;
  onOpenChange: (open: boolean) => void;
  onDuplicated: (result: SubscribeResult) => void | Promise<void>;
}) {
  const [label, setLabel] = useState("");
  const [saving, setSaving] = useState(false);
  const [key] = useState(() => newUUID());
  const inputRef = useRef<HTMLInputElement>(null);
  const trimmed = label.trim();
  const ref = entry.managedAnimeRef;

  async function submit() {
    if (!ref || trimmed === "" || saving) return;
    setSaving(true);
    try {
      const result = await duplicateAnimeSubscription(ref, { instance_label: trimmed }, key);
      onOpenChange(false);
      await onDuplicated(result);
    } catch (cause) {
      toast.error(toUserMessage(cause, t("grid.duplicateFailed")));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open
      onRequestClose={() => onOpenChange(false)}
      tier="S"
      initialFocus={inputRef}
      title={t("grid.duplicateTitle", { title: entry.title })}
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>{t("act.cancel")}</Button>
          <Button variant="primary" disabled={!ref || trimmed === "" || saving} onClick={() => void submit()}>
            {saving ? t("act.submitting") : t("grid.duplicateConfirm")}
          </Button>
        </>
      }
    >
      <form
        className="candidate-form"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <p className="tw:text-sm">{t("grid.duplicateLine")}</p>
        <label className="candidate-field">
          {t("grid.duplicateLabel")}
          <Input
            ref={inputRef}
            value={label}
            required
            maxLength={256}
            onChange={(event) => setLabel(event.target.value)}
            placeholder={t("grid.duplicatePlaceholder")}
          />
        </label>
      </form>
    </Dialog>
  );
}
