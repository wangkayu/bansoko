import { useQuery } from "@tanstack/react-query";
import { getPluginDelivery, type PluginDeliveryDetail } from "@/lib/api/client";
import { formatDateTime, notificationTriggerKindLabel } from "@/lib/labels";
import { pluginDeliveryKeys } from "@/lib/queryKeys";
import { Alert, Dialog, StatePill, StateBlock } from "../../ui/base";
import { t } from "../../lib/i18n";
import { knownReasonLabel, reasonLabel } from "../../lib/i18n/reasons";

// Delivery detail read model: the Delivery is the business delivery truth,
// Attempts are the per-network-attempt evidence. `unknown` is never shown as
// success and never auto-equal to terminal failure — the label explains that
// a retry may still happen.
export function deliveryStateLabel(state: PluginDeliveryDetail["delivery"]["state"]): string {
  switch (state) {
    case "delivered":
      return t("settings.delivery.state.delivered");
    case "failed":
      return t("settings.delivery.state.failedFinal");
    case "waiting_retry":
      return t("settings.delivery.state.waiting_retry");
    case "delivering":
      return t("settings.delivery.state.delivering");
    case "pending":
      return t("settings.delivery.state.pending");
    case "unknown":
      return t("settings.delivery.state.unknown");
    default:
      return t("settings.delivery.state.other");
  }
}

function attemptTag(state: string): { tone: "online" | "offline"; label: string } {
  if (state === "failed") return { tone: "offline", label: t("settings.delivery.detail.noResponse") };
  return { tone: "online", label: t("settings.delivery.detail.gotResponse") };
}
function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="tw:grid tw:gap-1 tw:rounded-control tw:border tw:border-border/60 tw:bg-card/40 tw:px-3 tw:py-2 tw:sm:grid-cols-[8rem_minmax(0,1fr)] tw:sm:items-start tw:sm:gap-3">
      <dt className="tw:text-muted-foreground">{label}</dt>
      <dd className="tw:m-0 tw:break-words tw:text-foreground">{children}</dd>
    </div>
  );
}

export function DeliveryDetailDialog({
  deliveryID,
  onClose,
}: {
  deliveryID: string;
  onClose: () => void;
}) {
  const detail = useQuery({
    queryKey: pluginDeliveryKeys.detail(deliveryID),
    queryFn: () => getPluginDelivery(deliveryID),
  });

  const delivery = detail.data?.delivery;
  const attempts = detail.data?.attempts ?? [];

  return (
    <Dialog open tier="M" onRequestClose={onClose} title={t("settings.delivery.detail.title")}>
      {detail.isLoading ? (
        <StateBlock kind="busy" title={t("settings.delivery.loading")} />
      ) : detail.isError ? (
        <Alert tone="error" title={t("settings.delivery.detail.loadFailed")} />
      ) : delivery ? (
        <div className="tw:grid tw:gap-4">
          <dl className="tw:m-0 tw:grid tw:gap-2 tw:text-sm">
            <DetailRow label={t("settings.delivery.detail.status")}>
              <StatePill tone={delivery.state === "delivered" ? "online" : delivery.state === "failed" ? "offline" : "info"}>
                {deliveryStateLabel(delivery.state)}
              </StatePill>
            </DetailRow>
            <DetailRow label={t("settings.delivery.detail.trigger")}>{notificationTriggerKindLabel(delivery.trigger_kind)}</DetailRow>
            <DetailRow label={t("settings.delivery.detail.target")}>{delivery.target_display}</DetailRow>
            <DetailRow label={t("settings.delivery.detail.attemptCount")}>{delivery.attempt_count}</DetailRow>
            {delivery.next_attempt_at ? (
              <DetailRow label={t("settings.delivery.detail.nextAttempt")}>{formatDateTime(delivery.next_attempt_at)}</DetailRow>
            ) : null}
            <DetailRow label={t("settings.delivery.detail.deliveryId")}>
              <span className="tw:break-all tw:font-mono tw:text-xs">{delivery.id}</span>
            </DetailRow>
            {delivery.last_error || delivery.last_error_code ? (
              <DetailRow label={t("settings.delivery.detail.lastError")}>
                <span className="tw:break-words tw:text-destructive">
                  {knownReasonLabel(delivery.last_error_code) ?? delivery.last_error ?? reasonLabel(delivery.last_error_code)}
                </span>
              </DetailRow>
            ) : null}
          </dl>

          <div>
            <h5 className="tw:m-0 tw:mb-2 tw:text-sm tw:font-semibold">{t("settings.delivery.detail.attempts")}</h5>
            {attempts.length === 0 ? (
              <StateBlock kind="empty" title={t("settings.delivery.detail.noAttempts")} />
            ) : (
              <ol aria-label={t("settings.delivery.detail.attemptList")} className="tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0">
                {attempts.map((attempt) => {
                  const result = attemptTag(attempt.response_status ? "delivered" : "failed");
                  return (
                    <li
                      key={`${attempt.plugin_delivery_id}-${attempt.attempt_number}`}
                      className="tw:rounded-control tw:border tw:border-border/60 tw:bg-card/40 tw:p-2"
                    >
                      <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1 tw:text-xs">
                        <strong>{t("settings.delivery.detail.attemptNo", { n: attempt.attempt_number })}</strong>
                        <StatePill tone={result.tone}>{attempt.response_status ? t("settings.delivery.detail.http", { code: attempt.response_status }) : result.label}</StatePill>
                        <span className="tw:text-muted-foreground">{formatDateTime(attempt.started_at)}</span>
                      </div>
                      {attempt.error ? (
                        <p className="tw:m-0 tw:mt-1 tw:break-words tw:text-xs tw:text-destructive">{attempt.error}</p>
                      ) : null}
                    </li>
                  );
                })}
              </ol>
            )}
          </div>
        </div>
      ) : (
        <StateBlock kind="empty" title={t("settings.delivery.detail.empty")} />
      )}
    </Dialog>
  );
}
