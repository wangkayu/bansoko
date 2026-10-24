import type { ReleaseRetryResult } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { reasonLabel } from "../../lib/i18n/reasons";
import { toast } from "../../ui/toast";

/** A one-line note of the retry result: a started download gets the success notice, other outcomes a neutral one. */
export function showReleaseRetryResult(result: ReleaseRetryResult) {
  const reason = result.reason_code ? reasonLabel(result.reason_code) : undefined;
  switch (result.outcome) {
    case "accepted":
      toast.success(t("rss.releaseRetry.accepted"));
      return;
    case "present":
      toast.info(t("rss.releaseRetry.present"));
      return;
    case "held":
      toast.info(reason ? t("rss.releaseRetry.heldBecause", { reason }) : t("rss.releaseRetry.held"));
      return;
    case "filtered":
      toast.info(reason ? t("rss.releaseRetry.filteredBecause", { reason }) : t("rss.releaseRetry.filtered"));
      return;
    default:
      toast.warning(reason ? t("rss.releaseRetry.notStartedBecause", { reason }) : t("rss.releaseRetry.notStarted"));
  }
}
