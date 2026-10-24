import { useCallback, type ReactNode } from "react";
import { RecordLinkContext, type RecordLinkOpener } from "./recordLinkContext";
import { ROUTE_RECORD_LIST, useRouteDialogs } from "./routeDialogs";

// Opens the execution records dialog from anywhere under the application
// shell: an RSS row's view-processing entry and the anime dialog's execution-records button.
// Opening replaces the current top-level dialog params and pushes an entry, so
// browser Back returns to the dialog it came from.
export function RecordLinkProvider({ children }: { children: ReactNode }) {
  const { open } = useRouteDialogs();
  const openRecord = useCallback<RecordLinkOpener>(
    (target) =>
      "acquisitionID" in target
        ? open({ record: target.acquisitionID })
        : // A subscribed anime's records are subscription work (a one-shot gets its own temporary owner).
          open({ record: ROUTE_RECORD_LIST, record_anime: target.managedAnimeID, record_segment: "subscription" }),
    [open],
  );
  return <RecordLinkContext.Provider value={openRecord}>{children}</RecordLinkContext.Provider>;
}
