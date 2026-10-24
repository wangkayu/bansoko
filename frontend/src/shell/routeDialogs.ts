import { useCallback, useMemo, useRef } from "react";
import { useLocation, useNavigate } from "react-router-dom";

// Route-hosted top-level dialogs.
// Every top-level dialog lives in the URL so it deep-links, survives refresh
// and closes on browser Back. They are query parameters on the catalog base
// route `/`, next to the catalog's own `?source=`:
//
//   ?anime=<catalog row key>&tab=<metadata|subgroups|rules|rss>   anime config
//   ?log=<records|diagnostics>                                     global log
//   ?tool=<qbittorrent|search>[&qb_segment=<subscription|one_shot|other>]
//                                                                  topbar tools; the downloader
//                                                                  manager's segment
//   (?tool=blacklist is retired: hidden anime live in Settings, /settings/hidden;
//   old links fall back to the base address.)
//   (?tool=files[&path=][&file=][&op=match] opened the file manager; it is
//   hidden in this version, so those links fall back to the base address.
//   Restore steps: see the comment at the top of TopNav.tsx.)
//   ?record=<list|acquisition id>[&record_anime=<managed anime id>]
//     [&record_segment=<subscription|one_shot>]                    execution records and the
//                                                                  list's segment
//
// Settings keeps its own path route (`/settings/:category`). Nested dialogs and
// S-tier confirmations stay component state.

export const ROUTE_DIALOG_PARAMS = [
  "anime",
  "tab",
  "log",
  "tool",
  "record",
  "record_anime",
  "record_segment",
  "qb_segment",
  "path",
  "file",
  "op",
] as const;
export type RouteDialogParam = (typeof ROUTE_DIALOG_PARAMS)[number];

// "files" (file manager) is hidden in this version; add it back to restore.
export const ROUTE_TOOLS = ["qbittorrent", "search"] as const;
export type RouteTool = (typeof ROUTE_TOOLS)[number];

/** File manager entry dialogs a link may open (`&op=`). Unused while the file manager is hidden. */
export const ROUTE_FILE_OPS = ["match"] as const;
export type RouteFileOp = (typeof ROUTE_FILE_OPS)[number];

export const ROUTE_LOG_VIEWS = ["records", "diagnostics"] as const;
export type RouteLogView = (typeof ROUTE_LOG_VIEWS)[number];

export const ROUTE_ANIME_TABS = ["metadata", "subgroups", "rules", "rss"] as const;
export type RouteAnimeTab = (typeof ROUTE_ANIME_TABS)[number];

/** `?record=list` is the record list; any other value names one acquisition. */
export const ROUTE_RECORD_LIST = "list";

/** Record list segments (`&record_segment=`), subscription first. */
export const ROUTE_RECORD_SEGMENTS = ["subscription", "one_shot"] as const;
export type RouteRecordSegment = (typeof ROUTE_RECORD_SEGMENTS)[number];

/** Downloader manager segments (`&qb_segment=`), subscription first; other = tasks bansoko did not create. */
export const ROUTE_QB_SEGMENTS = ["subscription", "one_shot", "other"] as const;
export type RouteQBSegment = (typeof ROUTE_QB_SEGMENTS)[number];

export function isOneOf<T extends string>(values: readonly T[], value: string | null | undefined): value is T {
  return value != null && (values as readonly string[]).includes(value);
}

// History entries pushed by `open` carry this marker, so `close` can step back
// to the base entry instead of stacking a second one. A deep link (or a pasted
// URL) has no marker and closes by replacing the entry with the base address.
type RouteDialogHistoryState = { routeDialog?: boolean } | null;

/** Link/navigate state for entries that open a top-level dialog over the base entry. */
export const ROUTE_DIALOG_STATE = { routeDialog: true } as const;

export function isRouteDialogEntry(state: unknown): boolean {
  return (state as RouteDialogHistoryState)?.routeDialog === true;
}

function withoutDialogParams(search: string): URLSearchParams {
  const next = new URLSearchParams(search);
  for (const key of ROUTE_DIALOG_PARAMS) next.delete(key);
  return next;
}

function toSearch(params: URLSearchParams): string {
  const text = params.toString();
  return text ? `?${text}` : "";
}

export function useRouteDialogs() {
  const location = useLocation();
  const navigate = useNavigate();
  const onBase = location.pathname === "/";
  const params = useMemo(() => new URLSearchParams(location.search), [location.search]);
  // A second close from the same location (e.g. two close paths firing in one
  // tick) must not step back twice.
  const closedAtKeyRef = useRef<string | null>(null);

  const get = useCallback((key: RouteDialogParam) => (onBase ? params.get(key) : null), [onBase, params]);

  /** Opens a top-level dialog: pushes `/` + the base params + `updates`. */
  const open = useCallback(
    (updates: Partial<Record<RouteDialogParam, string>>) => {
      const next = withoutDialogParams(onBase ? location.search : "");
      for (const [key, value] of Object.entries(updates)) if (value) next.set(key, value);
      navigate({ pathname: "/", search: toSearch(next) }, { state: { routeDialog: true } });
    },
    [location.search, navigate, onBase],
  );

  /** Updates the open dialog's params in place (tab switches): no history entry. */
  const update = useCallback(
    (updates: Partial<Record<RouteDialogParam, string>>) => {
      if (!onBase) return;
      const next = new URLSearchParams(location.search);
      let changed = false;
      for (const [key, value] of Object.entries(updates)) {
        if (next.get(key) === (value ?? null)) continue;
        changed = true;
        if (value) next.set(key, value);
        else next.delete(key);
      }
      if (!changed) return;
      navigate({ pathname: "/", search: toSearch(next) }, { replace: true, state: location.state });
    },
    [location.search, location.state, navigate, onBase],
  );

  /** Drops the dialog params by replacing the entry (invalid or unresolvable deep links). */
  const dismiss = useCallback(() => {
    navigate({ pathname: "/", search: toSearch(withoutDialogParams(location.search)) }, { replace: true });
  }, [location.search, navigate]);

  /** Closes the dialog and returns to the base address, keeping `?source=`. */
  const close = useCallback(() => {
    if (closedAtKeyRef.current === location.key) return;
    closedAtKeyRef.current = location.key;
    if ((location.state as RouteDialogHistoryState)?.routeDialog) {
      navigate(-1);
      return;
    }
    dismiss();
  }, [dismiss, location.key, location.state, navigate]);

  return { onBase, get, open, update, dismiss, close };
}
