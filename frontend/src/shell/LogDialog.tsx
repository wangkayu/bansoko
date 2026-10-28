import { useEffect, useState } from "react";
import { Dialog } from "../ui/base/Dialog";
import { Tabs } from "../ui/base/controls";
import { DeliveryDetailDialog } from "@/features/settings/DeliveryDetailDialog";
import { useLogDialogStore } from "./logDialogStore";
import { readLogSeen } from "./logSeen";
import { EMPTY_FILTERS, LogEvents, type EventFilters } from "./LogEvents";
import { LogDiagnostics } from "./LogDiagnostics";
import { LogRawView } from "./LogRawView";
import { useBoundedRefresh } from "../lib/events/useBoundedRefresh";
import { observabilityKeys } from "../lib/queryKeys";
import { t } from "../lib/i18n";

// LogDialog — the single global observability entry: a top-bar dialog with two tabs, events
// and diagnostics. Events carry the business mainline (business history + notification
// delivery results); routine execution evidence stays internal. Diagnostics list conditions
// (one row per episode); the raw runtime log is a sub-view of that tab, not a third tab.
//
// Seen markers (this browser only, see logSeen.ts): an unfiltered events list moves the events
// mark, opening the diagnostics tab moves the diagnostics mark. Failures newer than the mark
// from before this opening carry a "new" tag.

type LogDialogProps = {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Controlled view (`records` | `diagnostics`); the route-hosted log keeps it in `?log=`. */
  view?: string;
  onViewChange?: (view: string) => void;
  /** Opens an anime's config on the metadata tab (fix action on a blocked-on-metadata event). */
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
  /** Opens the settings area (a router path) that fixes a diagnostic condition. */
  onOpenSettings?: (path: string) => void;
};

export function LogDialog({ open: controlledOpen, onOpenChange, view, onViewChange, onOpenAnimeMetadata, onOpenSettings }: LogDialogProps = {}) {
  const storeOpen = useLogDialogStore((state) => state.open);
  const open = controlledOpen ?? storeOpen;
  const presetAnimeId = useLogDialogStore((state) => state.managedAnimeId);
  const closeDialog = useLogDialogStore((state) => state.closeDialog);
  const [localTab, setLocalTab] = useState("records");
  const tab = view ?? localTab;
  const setTab = onViewChange ?? setLocalTab;
  const [filters, setFilters] = useState<EventFilters>(EMPTY_FILTERS);
  const [rawOpen, setRawOpen] = useState(false);
  const [deliveryID, setDeliveryID] = useState<string>();
  // The "seen" marks from before this opening: snapshot them on open to tag entries as "new"; cleared on close.
  const [openedMarks, setOpenedMarks] = useState<{ records: string | null; diagnostics: string | null } | null>(null);
  if (open && openedMarks === null) setOpenedMarks({ records: readLogSeen("records"), diagnostics: readLogSeen("diagnostics") });
  if (!open && openedMarks !== null) setOpenedMarks(null);
  // Follow realtime events while open (rss/records invalidations refresh the records); low-frequency polling when the stream is down.
  useBoundedRefresh(open ? [observabilityKeys.all] : []);

  // Every opening starts from clean filters, with the card entry's preselected anime poured in; the top-bar entry means all anime.
  useEffect(() => {
    if (open) {
      setFilters({ ...EMPTY_FILTERS, animeId: presetAnimeId ?? "" });
      setRawOpen(false);
    }
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
      presetAnime={Boolean(presetAnimeId)}
      active={open && tab === "records"}
      newSince={openedMarks?.records ?? null}
      onOpenDelivery={setDeliveryID}
      onOpenAnimeMetadata={onOpenAnimeMetadata}
    />
  );

  const diagnosticsContent = rawOpen ? (
    <LogRawView onBack={() => setRawOpen(false)} />
  ) : (
    <LogDiagnostics
      active={open && tab === "diagnostics"}
      newSince={openedMarks?.diagnostics ?? null}
      onOpenSettings={onOpenSettings}
      onOpenRaw={() => setRawOpen(true)}
    />
  );

  return (
    <Dialog open={open} onRequestClose={() => handleOpenChange(false)} tier="M" fixedHeight focusTitle title={t("log.title")} bodyClassName="tw:pt-2">
      <Tabs
        value={tab}
        onChange={setTab}
        items={[
          { key: "records", label: t("log.tabRecords"), children: recordsContent },
          { key: "diagnostics", label: t("log.tabDiagnostics"), children: diagnosticsContent },
        ]}
      />
      {/* Rendered inside the log dialog so they stack as the nested layer and Esc closes only them. */}
      {deliveryID ? <DeliveryDetailDialog deliveryID={deliveryID} onClose={() => setDeliveryID(undefined)} /> : null}
    </Dialog>
  );
}
