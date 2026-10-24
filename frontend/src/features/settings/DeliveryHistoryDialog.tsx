import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { History, RefreshCw } from "lucide-react";
import { listPluginDeliveries, toUserMessage, type PluginDeliveryList } from "@/lib/api/client";
import { formatDateTime, notificationTriggerKindLabel } from "@/lib/labels";
import { DeliveryDetailDialog } from "./DeliveryDetailDialog";
import { pluginDeliveryKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, StatePill, StateBlock } from "../../ui/base";
import { t } from "../../lib/i18n";

// The delivery history list: paginated newest first; clicking opens DeliveryDetailDialog for that delivery's attempts
const PAGE_LIMIT = 20;

type PluginDelivery = PluginDeliveryList["items"][number];

const stateLabels = (): Record<string, string> => ({
  pending: t("settings.delivery.state.pending"),
  delivering: t("settings.delivery.state.delivering"),
  waiting_retry: t("settings.delivery.state.waiting_retry"),
  delivered: t("settings.delivery.state.delivered"),
  failed: t("settings.delivery.state.failed"),
  unknown: t("settings.delivery.state.unknown"),
});

const stateTone: Record<string, "online" | "offline" | "warning" | "muted" | "info"> = {
  delivered: "online",
  failed: "offline",
  waiting_retry: "warning",
  unknown: "warning",
  pending: "muted",
  delivering: "info",
};

export function DeliveryHistoryDialog({ onClose }: { onClose: () => void }) {
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<PluginDelivery | null>(null);
  const deliveries = useQuery({
    queryKey: pluginDeliveryKeys.list(offset),
    queryFn: () => listPluginDeliveries({ limit: PAGE_LIMIT, offset }),
  });

  const items = deliveries.data?.items ?? [];
  const total = deliveries.data?.total ?? 0;

  return (
    <Dialog
      open
      tier="M"
      onRequestClose={onClose}
      title={
        <span className="tw:inline-flex tw:items-center tw:gap-2">
          <History size={16} className="tw:text-primary" aria-hidden="true" />
          <span>{t("settings.delivery.history")}</span>
          <span className="tw:text-sm tw:font-normal tw:text-muted-foreground">{t("settings.delivery.total", { n: total })}</span>
        </span>
      }
      footer={
        <div className="tw:flex tw:w-full tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
          <div className="tw:flex tw:items-center tw:gap-2">
            <span className="tw:text-sm tw:text-muted-foreground">
              {total === 0 ? "0" : `${offset + 1}–${Math.min(offset + PAGE_LIMIT, total)}`} / {total}
            </span>
            <Button size="sm" variant="ghost" loading={deliveries.isFetching} onClick={() => void deliveries.refetch()}>
              {deliveries.isFetching ? null : <RefreshCw size={14} aria-hidden="true" />}
              {t("settings.delivery.refresh")}
            </Button>
          </div>
          <div className="tw:flex tw:items-center tw:gap-2">
            <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_LIMIT))}>
              {t("settings.delivery.prev")}
            </Button>
            <Button size="sm" disabled={offset + PAGE_LIMIT >= total} onClick={() => setOffset(offset + PAGE_LIMIT)}>
              {t("settings.delivery.next")}
            </Button>
            <Button size="sm" variant="ghost" onClick={onClose}>
              {t("settings.delivery.close")}
            </Button>
          </div>
        </div>
      }
    >
      <div className="tw:grid tw:gap-2">
        {deliveries.isLoading && <StateBlock kind="busy" title={t("settings.delivery.loading")} />}
        {deliveries.isError && <Alert tone="error" title={toUserMessage(deliveries.error, t("settings.delivery.loadHistoryFailed"))} />}
        {!deliveries.isLoading && !deliveries.isError && items.length === 0 && <StateBlock kind="empty" title={t("settings.delivery.emptyHistory")} />}
        <ul className="tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0">
          {items.map((delivery) => (
            <li key={delivery.id}>
              <button
                type="button"
                onClick={() => setSelected(delivery)}
                className="tw:flex tw:w-full tw:flex-wrap tw:items-center tw:gap-2 tw:rounded-control tw:border tw:border-border tw:bg-card/50 tw:p-3 tw:text-left tw:text-sm tw:transition-colors tw:hover:border-accent/50 tw:focus-visible:border-accent/50 tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
              >
                <span>{delivery.target_display}</span>
                <span className="tw:text-muted-foreground">{notificationTriggerKindLabel(delivery.trigger_kind)}</span>
                <StatePill tone={stateTone[delivery.state] ?? "muted"}>{stateLabels()[delivery.state] ?? delivery.state}</StatePill>
                <span className="tw:text-muted-foreground">{t("settings.delivery.attemptsShort", { n: delivery.attempt_count })}</span>
                <span className="tw:flex-1" />
                <span className="tw:text-muted-foreground">{formatDateTime(delivery.created_at)}</span>
              </button>
            </li>
          ))}
        </ul>
      </div>
      {selected && <DeliveryDetailDialog deliveryID={selected.id} onClose={() => setSelected(null)} />}
    </Dialog>
  );
}
