import type { AcquisitionConfigHold } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { joinList } from "../../lib/i18n/format";

/** Why an import waits for configuration: what the anime's current configuration lacks, in the server's order. */
export function configHoldText(hold: AcquisitionConfigHold): string {
  const items = hold.map((item) => {
    switch (item.missing) {
      case "bgm_binding":
        return t("records.configHold.bgm_binding");
      case "tmdb_binding":
        return t("records.configHold.tmdb_binding");
      case "template_variable":
        return t("records.configHold.template_variable", { vars: joinList(item.variables ?? []) });
    }
  });
  return t("records.configHold.reason", { items: items.join(t("records.configHold.separator")) });
}
