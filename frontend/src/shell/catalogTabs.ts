import { useCallback, useEffect } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";
import type { CopyKey } from "../lib/i18n";

// Catalog section tabs: the four sections are the only page-level
// navigation. The desktop top bar and the phone tab bar both drive this shared
// state; AnimeGrid consumes it. The URL query (`?source=`) is authoritative
// (deep link / refresh / Back); without it we fall back to the session memory
// so opening settings (which drops ?source=) does not bounce the catalog back
// to the subscribed section.

export type CatalogTabKey = "all" | "mikan" | "anibt" | "custom";

export const CATALOG_SOURCE_QUERY_KEY = "source";

const TAB_MEMORY_KEY = "bansoko.catalog-tab";
const BROWSE_TAB_MEMORY_KEY = "bansoko.catalog-browse-tab";

export const CATALOG_TABS: { key: CatalogTabKey; label?: string; labelKey?: CopyKey }[] = [
  { key: "all", labelKey: "grid.tabAll" },
  { key: "anibt", label: "AniBT" },
  { key: "mikan", label: "Mikan" },
  { key: "custom", labelKey: "grid.tabCustom" },
];

/** Tabs reachable from the phone tab bar's Discover entry. */
export const CATALOG_BROWSE_TABS: CatalogTabKey[] = ["anibt", "mikan", "custom"];

export function isCatalogTab(value: string | null | undefined): value is CatalogTabKey {
  return value != null && CATALOG_TABS.some((tab) => tab.key === value);
}

export function readRememberedTab(): CatalogTabKey | null {
  try {
    const stored = window.sessionStorage.getItem(TAB_MEMORY_KEY);
    return isCatalogTab(stored) ? stored : null;
  } catch {
    return null;
  }
}

/** Last browse section, for the phone tab bar's Discover entry. */
export function readRememberedBrowseTab(): CatalogTabKey {
  try {
    const stored = window.sessionStorage.getItem(BROWSE_TAB_MEMORY_KEY);
    return stored && CATALOG_BROWSE_TABS.includes(stored as CatalogTabKey) ? (stored as CatalogTabKey) : "anibt";
  } catch {
    return "anibt";
  }
}

export function useCatalogTab() {
  const [searchParams, setSearchParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const onBase = location.pathname === "/";
  const tabParam = searchParams.get(CATALOG_SOURCE_QUERY_KEY);
  const tab: CatalogTabKey = isCatalogTab(tabParam) ? tabParam : (readRememberedTab() ?? "all");

  // A browse tab opened by URL (deep link / Back) also refreshes the Discover memory.
  useEffect(() => {
    if (!CATALOG_BROWSE_TABS.includes(tab)) return;
    try {
      window.sessionStorage.setItem(BROWSE_TAB_MEMORY_KEY, tab);
    } catch {
      // A failed write to the memory must not block the switch; the URL query stays authoritative.
    }
  }, [tab]);

  const setTab = useCallback(
    (next: CatalogTabKey) => {
      try {
        window.sessionStorage.setItem(TAB_MEMORY_KEY, next);
      } catch {
        // Same as above: a failed write to the memory must not block.
      }
      if (onBase) {
        setSearchParams(
          (prev) => {
            prev.set(CATALOG_SOURCE_QUERY_KEY, next);
            return prev;
          },
          { replace: true },
        );
      } else {
        // Switching the section while an overlay (settings etc.) is open means
        // leaving the overlay for the base; push history so browser Back
        // returns to the overlay (dirty forms are still guarded by their own
        // useBlocker).
        navigate({ pathname: "/", search: `?${CATALOG_SOURCE_QUERY_KEY}=${next}` });
      }
    },
    [navigate, onBase, setSearchParams],
  );

  return { tab, setTab, onBase };
}
