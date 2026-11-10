import { useQuery } from "@tanstack/react-query";
import { getCatalogProvider, getLibraryProvider } from "../../lib/providers/registry";
import { listCatalogSubscriptions, mapAnimeListItem } from "../../lib/providers/catalogProvider";
import type { CatalogProvider } from "../../lib/providers/port";
import type {
  CatalogEntryView,
  CatalogSnapshotView,
} from "../../lib/providers/viewmodels";
import { animeKeys, catalogKeys as centralizedCatalogKeys, type CatalogTabKey } from "../../lib/queryKeys";
import type { AnimeListItem } from "../../lib/api/client";

export type { CatalogTabKey } from "../../lib/queryKeys";
// The catalog's query layer, where the four columns' data sources converge.
//   - all: created subscription instances (aggregated via catalogProvider, across providers)
//   - mikan/anibt: the server's catalog read model (via catalogProvider)
//   - custom: the server's custom presets (via catalogProvider)
//   Subscription reads share animeKeys.list; provider reads use catalogKeys.

export interface GridRow {
  provider: CatalogProvider;
  entry: CatalogEntryView;
}

export interface CatalogResult {
  rows: GridRow[];
  snapshot?: CatalogSnapshotView;
}

export const catalogKeys = centralizedCatalogKeys;

function subscriptionCatalog(items: AnimeListItem[]): CatalogResult {
  const provider = getLibraryProvider();
  return { rows: items.map((item) => ({ provider, entry: mapAnimeListItem(item) })) };
}

async function fetchCatalog(tab: Exclude<CatalogTabKey, "all">, season: string): Promise<CatalogResult> {
  const provider = getCatalogProvider(tab);
  const view = await provider.getCatalog(season);
  return {
    rows: view.entries.map((entry) => ({ provider, entry })),
    snapshot: view.snapshot,
  };
}

export function useCatalog(tab: CatalogTabKey, season: string, options: { enabled?: boolean } = {}) {
  const enabled = options.enabled ?? true;
  // The wall, log filters and management dialog observe the same raw list.
  // Card projection is local to this observer, never stored over the raw data.
  const subscriptions = useQuery({
    queryKey: animeKeys.list,
    queryFn: listCatalogSubscriptions,
    select: subscriptionCatalog,
    enabled: enabled && tab === "all",
  });
  const catalog = useQuery({
    queryKey: catalogKeys.list(tab, season),
    queryFn: () => fetchCatalog(tab as Exclude<CatalogTabKey, "all">, season),
    enabled: enabled && tab !== "all",
  });
  return tab === "all" ? subscriptions : catalog;
}
