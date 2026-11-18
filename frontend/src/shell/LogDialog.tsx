import { useEffect, useState } from "react";
import { Dialog } from "../ui/base/Dialog";
import { TabsList, TabsPanel, TabsRoot } from "../ui/base/controls";
import { HelpTip } from "../ui/base/HelpTip";
import { DeliveryDetailDialog } from "@/features/settings/DeliveryDetailDialog";
import { useLogDialogStore } from "./logDialogStore";
import { EMPTY_FILTERS, LogEvents, type EventFilters } from "./LogEvents";
import { LogRawView, RUNTIME_LOG_LINE_LIMIT } from "./LogRawView";
import { useBoundedRefresh } from "../lib/events/useBoundedRefresh";
import { observabilityKeys } from "../lib/queryKeys";
import { t } from "../lib/i18n";

// LogDialog — the single global observability entry: a top-bar dialog with two tabs, events
// and the runtime log. Events carry the business mainline (business history + notification
// delivery results); routine execution evidence stays internal. The runtime log is the
// retained on-disk history, read incrementally while its tab is visible.

type LogDialogProps = {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Controlled view (`records` | `raw`); the route-hosted log keeps it in `?log=`. */
  view?: string;
  onViewChange?: (view: string) => void;
  /** Opens an anime's config on the metadata tab (fix action on a blocked-on-metadata event). */
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
};

export function LogDialog({ open: controlledOpen, onOpenChange, view, onViewChange, onOpenAnimeMetadata }: LogDialogProps = {}) {
  const storeOpen = useLogDialogStore((state) => state.open);
  const open = controlledOpen ?? storeOpen;
  const presetAnimeId = useLogDialogStore((state) => state.managedAnimeId);
  const closeDialog = useLogDialogStore((state) => state.closeDialog);
  const [localTab, setLocalTab] = useState("records");
  const tab = view ?? localTab;
  const setTab = onViewChange ?? setLocalTab;
  const [filters, setFilters] = useState<EventFilters>(EMPTY_FILTERS);
  const [deliveryID, setDeliveryID] = useState<string>();
  // Follow realtime events while open (rss/records invalidations refresh the records); low-frequency polling when the stream is down.
  // The runtime log keeps its own 5s cadence and is deliberately not part of this fallback.
  useBoundedRefresh(open ? [observabilityKeys.records, observabilityKeys.releaseBatches] : []);

  // Every opening starts from clean filters, with the card entry's preselected anime poured in; the top-bar entry means all anime.
  useEffect(() => {
    if (open) setFilters({ ...EMPTY_FILTERS, animeId: presetAnimeId ?? "" });
  }, [open, presetAnimeId]);

  const handleOpenChange = (next: boolean) => {
    onOpenChange?.(next);
    if (!next) {
      setDeliveryID(undefined);
      closeDialog();
    }
  };

  const recordsContent = (
    <LogEvents
      filters={filters}
      onFiltersChange={setFilters}
      onOpenDelivery={setDeliveryID}
      onOpenAnimeMetadata={onOpenAnimeMetadata}
    />
  );

  const items = [
    { key: "records", label: t("log.tabRecords"), content: recordsContent },
    { key: "raw", label: t("log.tabRaw"), content: <LogRawView active={open && tab === "raw"} /> },
  ];

  const footer =
    tab === "raw" ? (
      <div className="tw:flex tw:w-full tw:items-center tw:justify-between tw:gap-2">
        <span className="tw:text-xs tw:text-muted-foreground">{t("log.rawlog.refreshEvery")}</span>
        <HelpTip label={t("log.rawlog.helpLabel")} className="tw:size-8">
          {t("log.rawlog.note", { n: RUNTIME_LOG_LINE_LIMIT })}
        </HelpTip>
      </div>
    ) : undefined;

  return (
    <Dialog
      open={open}
      onRequestClose={() => handleOpenChange(false)}
      tier="M"
      fixedHeight
      focusTitle
      title={t("log.title")}
      bodyLayout="fill"
      bodyClassName="tw:px-6 tw:pt-2 tw:pb-4 tw:phone:px-4 tw:[@media(max-height:500px)]:pb-2"
      footer={footer}
    >
      <TabsRoot value={tab} onChange={setTab} className="tw:flex-1">
        <TabsList items={items} />
        {items.map(({ key, content }) => (
          <TabsPanel key={key} value={key} className="tw:flex tw:min-h-0 tw:flex-1 tw:flex-col tw:pt-3 tw:[@media(max-height:500px)]:pt-2 tw:[&[hidden]]:hidden">
            {content}
          </TabsPanel>
        ))}
      </TabsRoot>
      {/* Rendered inside the log dialog so they stack as the nested layer and Esc closes only them. */}
      {deliveryID ? <DeliveryDetailDialog deliveryID={deliveryID} onClose={() => setDeliveryID(undefined)} /> : null}
    </Dialog>
  );
}
