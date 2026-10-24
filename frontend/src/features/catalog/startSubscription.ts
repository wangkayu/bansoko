import { isVersionConflict, patchAnimeSubscription, toUserMessage, type SubscribeResult } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { toast } from "../../ui/toast";

/** Starts a not-yet-started subscription: exact instance + version CAS (pending -> active).
 *  Both success and failure call refresh so the list reflects the latest state/version. */
export async function startSubscription(
  managedAnimeRef: string,
  subscriptionVersion: number,
  refresh: () => Promise<unknown> | void,
): Promise<boolean> {
  try {
    await patchAnimeSubscription(managedAnimeRef, { expected_version: subscriptionVersion, state: "active" });
    await refresh();
    toast.success(t("anime.started"));
    return true;
  } catch (cause) {
    await refresh();
    toast.error(isVersionConflict(cause) ? t("catalog.changed") : toUserMessage(cause, t("anime.startFailed")));
    return false;
  }
}

/** The subscription-success notice: a new subscription starts not-yet-started, and the notice offers a start action. */
export function announceSubscribed(result: SubscribeResult, title: string, refresh: () => Promise<unknown> | void) {
  if (result.subscription.state === "pending") {
    toast.success(t("grid.subscribedPending", { title }), {
      action: {
        label: t("anime.start"),
        onClick: () => void startSubscription(result.managed_anime.id, result.subscription.version, refresh),
      },
    });
    return;
  }
  toast.success(t("grid.subscribed", { title }));
}
