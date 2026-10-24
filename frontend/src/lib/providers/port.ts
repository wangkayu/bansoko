import type {
  AnimeDraft,
  SubscribeResult,
  Subscription,
  SubscriptionPatch,
} from "../api/client";
import type { CatalogProviderView, CatalogRefreshOutcome, ProviderKey } from "./viewmodels";

// CatalogProvider is the single data boundary of the catalog / anime-config UI: pages depend
// only on this interface and the ViewModels, never on a concrete adapter. The API
// implementation (catalogProvider.ts) calls only lib/api/client's typed functions.

export interface CatalogProvider {
  readonly key: ProviderKey;
  readonly label: string;
  /** true when the catalog comes from a fixture, not a real provider. */
  readonly isMock: boolean;

  /** Browses a season's provider catalog; no side effects. */
  getCatalog(season: string): Promise<CatalogProviderView>;

  /**
   * 显式刷新目录，只返回这次刷新的结果（用于提示）。列表由调用方失效
   * catalog 查询后重读一次，这里不再额外读取目录。
   */
  refreshCatalog(season: string): Promise<CatalogRefreshOutcome>;


  /** The final subscription: atomically creates the anime, subscription, source and configuration. */
  subscribe(draft: AnimeDraft): Promise<SubscribeResult>;

  /** Changes only the user-controlled Subscription state (expected-version CAS). */
  setSubscriptionState(managedAnimeRef: string, patch: SubscriptionPatch): Promise<Subscription>;
}
