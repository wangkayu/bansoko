import type { ProviderKey } from "./providers/viewmodels";

/** Catalog tabs represented by the catalog list query. */
export type CatalogTabKey = "all" | ProviderKey;

export const catalogKeys = {
  all: ["catalog"] as const,
  list: (tab: CatalogTabKey, season: string) => ["catalog", "list", tab, season] as const,
  preference: (provider: "anibt" | "mikan", season: string) => ["catalog", "preference", provider, season] as const,
  suppressions: ["catalog", "suppressions"] as const,
  feedSites: ["catalog", "feed-sites"] as const,
};

export const animeKeys = {
  list: ["anime", "list"] as const,
  workspacePrefix: ["anime", "workspace"] as const,
  workspace: (managedAnimeRef: string) => ["anime", "workspace", managedAnimeRef] as const,
};

/**
 * 单番 RSS 列表读模型（POST /anime/rss-list-view，纯读）。owner 视图按番剧分键，
 * 失效时只针对该番剧；草稿预览不进查询缓存。
 */
export const rssListKeys = {
  all: ["rss-list-view"] as const,
  owner: (managedAnimeID: string) => ["rss-list-view", "owner", managedAnimeID] as const,
  ownerView: (managedAnimeID: string, subscriptionVersion: number, rulesVersion: number, previewKey: string) =>
    ["rss-list-view", "owner", managedAnimeID, subscriptionVersion, rulesVersion, previewKey] as const,
};

export const configKeys = {
  prefix: ["config"] as const,
  connections: ["config", "connections"] as const,
  globalSections: ["config", "global-sections"] as const,
  /** The naming template library (the list + the default template). */
  namingTemplates: ["config", "naming-templates"] as const,
  /** The full Global business snapshot: the loading input for new drafts and one-shot tasks, shared as one copy per session. */
  businessPreset: ["config", "business-preset"] as const,
  titleTagVocabulary: ["config", "title-tag-vocabulary"] as const,
  /** The closed registry of notification trigger events (/config/notifications/trigger-kinds). */
  notificationTriggerKinds: ["config", "notification-trigger-kinds"] as const,
  parserCatalog: ["config", "parser-catalog"] as const,
  networkRoutes: ["config", "network-routes"] as const,
  pluginTargets: ["config", "plugin-targets"] as const,
  namingVars: ["config", "naming-vars"] as const,
};

/** The subgroup dictionary (the provider pool): read locally, keyed by provider. `search` is
 *  the type-to-filter dictionary query of the scoring card; `user` is the layer you added,
 *  feeding both the summary count and the delete list. */
export const subgroupDictionaryKeys = {
  prefix: ["config", "subgroup-dictionary"] as const,
  provider: (provider: string) => ["config", "subgroup-dictionary", provider] as const,
  user: (provider: string) => ["config", "subgroup-dictionary", provider, "user"] as const,
  search: (provider: string, q: string) => ["config", "subgroup-dictionary", provider, "search", q] as const,
  /** The dictionary dialog's paged list (an empty q means the whole dictionary, with stale harvested entries hidden). */
  list: (provider: string, q: string) => ["config", "subgroup-dictionary", provider, "list", q] as const,
};

export const automationKeys = {
  schedules: ["automation", "schedules"] as const,
};

export const observabilityKeys = {
  all: ["observability"] as const,
  records: ["observability", "records"] as const,
  diagnostics: ["observability", "diagnostics"] as const,
  businessLine: (outcome: string, animeId: string, from?: string, to?: string) =>
    ["observability", "records", "business-line", outcome, animeId, from, to] as const,
  diagnosticsFiltered: (severity: string, component: string, from?: string, to?: string) =>
    ["observability", "diagnostics", severity, component, from, to] as const,
  // The log-entry badge: hung under the records/diagnostics prefixes, following the same invalidation set.
  badgeRecords: (outcome: "failure" | "unknown") => ["observability", "records", "badge", outcome] as const,
  badgeDiagnostics: ["observability", "diagnostics", "badge"] as const,
};

export const acquisitionRecordKeys = {
  all: ["acquisition-records"] as const,
  list: (filters: { managedAnimeID?: string; state?: string; entryKind?: string; origin?: string }) =>
    ["acquisition-records", "list", filters.managedAnimeID ?? "", filters.state ?? "", filters.entryKind ?? "", filters.origin ?? ""] as const,
  detail: (acquisitionID: string) => ["acquisition-records", "detail", acquisitionID] as const,
  files: (acquisitionID: string) => ["acquisition-records", "files", acquisitionID] as const,
};

export const fileManagerKeys = {
  all: ["file-manager"] as const,
  mounts: ["file-manager", "mounts"] as const,
  entries: (path: string) => ["file-manager", "entries", path] as const,
  entry: (path: string) => ["file-manager", "entry", path] as const,
  operation: (operationID: string) => ["file-manager", "operation", operationID] as const,
  location: (managedAnimeID: string) => ["file-manager", "location", managedAnimeID] as const,
  matchPreview: (path: string, animeID: string, season: number, episode: number) =>
    ["file-manager", "match-preview", path, animeID, season, episode] as const,
  tmdbSeasons: (animeID: string) => ["file-manager", "tmdb-seasons", animeID] as const,
  tmdbEpisodes: (animeID: string, season: number) => ["file-manager", "tmdb-episodes", animeID, season] as const,
};

export const fileMaintenanceKeys = {
  policy: ["file-maintenance", "policy"] as const,
  retentionDefault: ["file-maintenance", "retention", "default"] as const,
  compareVocabulary: (animeID: string) => ["file-maintenance", "retention", "compare-vocabulary", animeID] as const,
  fileEffectDefault: ["file-maintenance", "file-effect", "default"] as const,
  files: (animeID: string) => ["file-maintenance", "files", animeID] as const,
  fileEffect: (animeID: string) => ["file-maintenance", "file-effect", animeID] as const,
  parserPolicy: (animeID: string) => ["file-maintenance", "parser-policy", animeID] as const,
  parserPolicyDefault: ["file-maintenance", "parser-policy", "default"] as const,
  offsetMigrations: (animeID: string) => ["file-maintenance", "offset-migrations", animeID] as const,
  offsetMigrationGenerations: (animeID: string) =>
    ["file-maintenance", "offset-migration-generations", animeID] as const,
};

export const reconciliationKeys = {
  pathDiffs: (animeID: string, generation: number) => ["reconciliation", "path-diffs", animeID, generation] as const,
};

export const qbManagementKeys = {
  all: ["qb-management"] as const,
  torrents: (connectionId: string | null) => ["qb-management", "torrents", connectionId] as const,
  status: (connectionId: string | null) => ["qb-management", "status", connectionId] as const,
  files: (connectionId: string | null, expandedHash: string | null) =>
    ["qb-management", "files", connectionId, expandedHash] as const,
};

export const pluginDeliveryKeys = {
  list: (offset: number) => ["plugin-deliveries", offset] as const,
  detail: (deliveryID: string) => ["plugin-deliveries", deliveryID] as const,
};

export const apiTokenKeys = {
  all: ["api-tokens"] as const,
  raw: (tokenId: string) => ["api-token-raw", tokenId] as const,
};

export const templateCatalogKeys = {
  naming: (language: string) => ["template-catalog", "naming", language] as const,
  notification: (kind: string, catalogVersion: string) =>
    ["template-catalog", "notification", kind, catalogVersion] as const,
};

export const updateKeys = {
  status: ["update", "status"] as const,
};

export const backupKeys = {
  info: ["backup", "info"] as const,
};

/** Domain-grouped view for consumers that prefer one import. */
export const queryKeys = {
  catalog: catalogKeys,
  anime: animeKeys,
  rssList: rssListKeys,
  config: configKeys,
  automation: automationKeys,
  observability: observabilityKeys,
  fileMaintenance: fileMaintenanceKeys,
  reconciliation: reconciliationKeys,
  qbManagement: qbManagementKeys,
  pluginDeliveries: pluginDeliveryKeys,
  apiTokens: apiTokenKeys,
  templateCatalog: templateCatalogKeys,
  update: updateKeys,
  backup: backupKeys,
} as const;
