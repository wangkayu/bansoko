import { useQuery } from "@tanstack/react-query";
import { getPluginDelivery, listPluginTargets, type PluginDeliveryDetail, type PluginTargetConfig } from "@/lib/api/client";
import { deliveryStateLabel, formatDateTime, notificationTriggerKindLabel } from "@/lib/labels";
import { configKeys, pluginDeliveryKeys } from "@/lib/queryKeys";
import { Alert, Dialog, StatePill, StateBlock } from "../../ui/base";
import { t } from "../../lib/i18n";
import { knownReasonLabel, reasonLabel } from "../../lib/i18n/reasons";

// Delivery detail read model: the Delivery is the business delivery truth,
// Attempts are the per-network-attempt evidence. `unknown` is never shown as
// success and never auto-equal to terminal failure — the label explains that
// a retry may still happen.

function adapterLabel(adapter: PluginDeliveryDetail["delivery"]["adapter_kind"]): string {
  switch (adapter) {
    case "telegram":
      return t("settings.notifications.adapter.telegram");
    case "feishu":
      return t("settings.notifications.adapter.feishu");
    case "webhook":
      return t("settings.notifications.adapter.webhook");
    default:
      return adapter;
  }
}

function targetText(delivery: PluginDeliveryDetail["delivery"], target: PluginTargetConfig | undefined): string {
  const adapter = adapterLabel(delivery.adapter_kind);
  return target ? t("log.delivery.targetWithAdapter", { name: target.name, adapter }) : `${adapter} · ${delivery.target_display}`;
}

function attemptTag(state: string): { tone: "online" | "offline"; label: string } {
  if (state === "failed") return { tone: "offline", label: t("settings.delivery.detail.noResponse") };
  return { tone: "online", label: t("settings.delivery.detail.gotResponse") };
}

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="tw:grid tw:grid-cols-[5.5rem_minmax(0,1fr)] tw:items-start tw:gap-2 tw:py-0.5 tw:text-sm">
      <dt className="tw:text-muted-foreground">{label}</dt>
      <dd className="tw:m-0 tw:min-w-0 tw:break-words tw:text-foreground">{children}</dd>
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
  const targets = useQuery({
    queryKey: configKeys.pluginTargets,
    queryFn: listPluginTargets,
  });

  const delivery = detail.data?.delivery;
  const attempts = detail.data?.attempts ?? [];
  const target = delivery ? targets.data?.targets?.find((item) => item.id === delivery.plugin_target_config_id) : undefined;
  const renderedTitle = delivery?.rendered_title?.trim();
  const renderedBody = delivery?.rendered_body?.trim();

  return (
    <Dialog open tier="S" onRequestClose={onClose} title={t("settings.delivery.detail.title")}>
      {detail.isLoading ? (
        <StateBlock kind="busy" title={t("settings.delivery.loading")} />
      ) : detail.isError ? (
        <Alert tone="error" title={t("settings.delivery.detail.loadFailed")} />
      ) : delivery ? (
        <div className="tw:grid tw:gap-2.5">
          <dl className="tw:m-0 tw:grid tw:gap-0.5">
            <DetailRow label={t("settings.delivery.detail.status")}>
              <StatePill tone={delivery.state === "delivered" ? "online" : delivery.state === "failed" ? "offline" : "info"}>
                {deliveryStateLabel(delivery.state)}
              </StatePill>
            </DetailRow>
            <DetailRow label={t("settings.delivery.detail.trigger")}>{notificationTriggerKindLabel(delivery.trigger_kind)}</DetailRow>
            <DetailRow label={t("settings.delivery.detail.target")}>{targetText(delivery, target)}</DetailRow>
            {delivery.next_attempt_at ? (
              <DetailRow label={t("settings.delivery.detail.nextAttempt")}>{formatDateTime(delivery.next_attempt_at)}</DetailRow>
            ) : null}
            {delivery.last_error || delivery.last_error_code ? (
              <DetailRow label={t("settings.delivery.detail.lastError")}>
                <span className="tw:break-words tw:text-destructive">
                  {knownReasonLabel(delivery.last_error_code) ?? delivery.last_error ?? reasonLabel(delivery.last_error_code)}
                </span>
              </DetailRow>
            ) : null}
          </dl>

          {renderedTitle || renderedBody ? (
            <section aria-label={t("settings.delivery.detail.message")} className="tw:grid tw:gap-1 tw:rounded-control tw:border tw:border-border/60 tw:bg-muted/25 tw:p-2">
              {renderedTitle ? <strong className="tw:break-words tw:text-sm tw:font-semibold">{renderedTitle}</strong> : null}
              {renderedBody ? (
                <p className="tw:m-0 tw:max-h-52 tw:overflow-y-auto tw:whitespace-pre-wrap tw:break-words tw:text-sm tw:text-muted-foreground">
                  {renderedBody}
                </p>
              ) : null}
            </section>
          ) : null}

          {attempts.length > 0 ? (
            <details className="tw:grid tw:gap-1">
              <summary className="tw:cursor-pointer tw:text-sm tw:text-muted-foreground">
                {t("settings.delivery.detail.attemptsSummary", { n: delivery.attempt_count })}
              </summary>
              <ol aria-label={t("settings.delivery.detail.attemptList")} className="tw:m-0 tw:mt-1 tw:grid tw:list-none tw:gap-1.5 tw:p-0">
                {attempts.map((attempt) => {
                  const result = attemptTag(attempt.response_status ? "delivered" : "failed");
                  return (
                    <li
                      key={`${attempt.plugin_delivery_id}-${attempt.attempt_number}`}
                      className="tw:rounded-control tw:border tw:border-border/60 tw:bg-card/40 tw:px-2 tw:py-1.5"
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
              <p className="tw:m-0 tw:mt-1 tw:font-mono tw:text-xs tw:text-muted-foreground">
                {t("settings.delivery.detail.deliveryId")} {delivery.id}
              </p>
            </details>
          ) : null}
        </div>
      ) : (
        <StateBlock kind="empty" title={t("settings.delivery.detail.empty")} />
      )}
    </Dialog>
  );
}
