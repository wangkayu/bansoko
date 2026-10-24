import { toast } from "../../ui/toast";
import { SaveBar } from "../../ui/SaveFooter";
import { t } from "../../lib/i18n";
import type { SettingsSaveParts } from "./settingsSave";

/** The settings dialog footer's single save; not rendered when the current section registered no part. */
export function SettingsSaveBar({ parts }: { parts: SettingsSaveParts }) {
  if (Object.keys(parts.parts).length === 0) return null;
  async function save() {
    const run = await parts.save();
    if (run.ok) toast.success(run.messages[0] ?? t("act.saved"));
  }
  return <SaveBar count={parts.count()} busy={parts.busy} failed={parts.failed} onSave={() => void save()} />;
}
