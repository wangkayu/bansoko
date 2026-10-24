import { useQuery } from "@tanstack/react-query";
import { getCatalogProvider, getLibraryProvider } from "../../lib/providers/registry";
import { listCatalogSubscriptions, mapAnimeListItem } from "../../lib/providers/catalogProvider";
import type { CatalogProvider } from "../../lib/providers/port";
import type {
  CatalogEntryView,
  CatalogSnapshotView,
} from "../../lib/providers/viewmodels";
import { catalogKeys as centralizedCatalogKeys, type CatalogTabKey } from "../../lib/queryKeys";

export type { CatalogTabKey } from "../../lib/queryKeys";
// The catalog's query layer, where the four columns' data sources converge.
//   - all: created subscription instances (aggregated via catalogProvider, across providers)
//   - mikan/anibt: the server's catalog read model (via catalogProvider)
//   - custom: the server's custom presets (via catalogProvider)
//   Query keys are built centrally; after a mutation invalidation targets catalogKeys.all precisely.

export interface GridRow {
  provider: CatalogProvider;
  entry: CatalogEntryView;
}

export interface CatalogResult {
  rows: GridRow[];
  snapshot?: CatalogSnapshotView;
}

export const catalogKeys = centralizedCatalogKeys;

async function fetchCatalog(tab: CatalogTabKey, season: string): Promise<CatalogResult> {
  if (tab === "all") {
    const provider = getLibraryProvider();
    const items = await listCatalogSubscriptions();
    return { rows: items.map((item) => ({ provider, entry: mapAnimeListItem(item) })) };
  }
  const provider = getCatalogProvider(tab);
  const view = await provider.getCatalog(season);
  return {
    rows: view.entries.map((entry) => ({ provider, entry })),
    snapshot: view.snapshot,
  };
}

export function useCatalog(tab: CatalogTabKey, season: string, options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: catalogKeys.list(tab, season),
    queryFn: () => fetchCatalog(tab, season),
    enabled: options.enabled ?? true,
  });
}
