import { useCallback } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { CATALOG_FILTER_QUERY_KEY, CATALOG_SOURCE_QUERY_KEY } from "../../shell/catalogTabs";
import { ROUTE_DIALOG_PARAMS, useRouteDialogs } from "../../shell/routeDialogs";

// A provider entry holds at most one live subscription (a second instance comes only from
//   duplicate), so a click on a browse or search card goes to what already exists:
//   - one non-retired instance: its subscription configuration, the same route-carried dialog the
//     subscribed wall opens (`?anime=<managed anime id>`, resolved by AnimeGrid);
//   - several (older duplicates): the subscription column with its filter set to the entry title;
//   - none, or only retired ones: not handled here; the caller opens the candidate draft.
export function useOpenLiveSubscription() {
  const routeDialogs = useRouteDialogs();
  const location = useLocation();
  const navigate = useNavigate();
  const { open } = routeDialogs;

  /** Returns true when the click was handled; false means open the candidate draft. */
  const openLiveSubscription = useCallback(
    (entry: CatalogEntryView): boolean => {
      const refs = entry.liveInstanceRefs ?? [];
      if (refs.length === 0) return false;
      if (refs.length === 1) {
        open({ anime: refs[0] });
        return true;
      }
      // Replace the dialog params (a search dialog closes with them) and keep the season.
      const next = new URLSearchParams(location.pathname === "/" ? location.search : "");
      for (const key of ROUTE_DIALOG_PARAMS) next.delete(key);
      next.set(CATALOG_SOURCE_QUERY_KEY, "all");
      next.set(CATALOG_FILTER_QUERY_KEY, entry.title);
      navigate({ pathname: "/", search: `?${next.toString()}` });
      return true;
    },
    [location.pathname, location.search, navigate, open],
  );

  /** Opens one subscription's configuration by managed anime id (a stale draft's 409 names it). */
  const openSubscriptionById = useCallback((managedAnimeID: string) => open({ anime: managedAnimeID }), [open]);

  return { openLiveSubscription, openSubscriptionById };
}
