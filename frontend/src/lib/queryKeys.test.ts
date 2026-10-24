import { describe, expect, it } from "vitest";
import {
  animeKeys,
  apiTokenKeys,
  automationKeys,
  backupKeys,
  catalogKeys,
  configKeys,
  fileMaintenanceKeys,
  observabilityKeys,
  pluginDeliveryKeys,
  qbManagementKeys,
  queryKeys,
  reconciliationKeys,
  rssListKeys,
  templateCatalogKeys,
  updateKeys,
} from "./queryKeys";

function expectPrefix(prefix: readonly unknown[], key: readonly unknown[]) {
  expect(key.slice(0, prefix.length)).toEqual([...prefix]);
}

describe("query key contracts", () => {
  it("keeps the aggregate view aligned with every domain export", () => {
    expect(Object.keys(queryKeys)).toEqual([
      "catalog",
      "anime",
      "rssList",
      "config",
      "automation",
      "observability",
      "fileMaintenance",
      "reconciliation",
      "qbManagement",
      "pluginDeliveries",
      "apiTokens",
      "templateCatalog",
      "update",
      "backup",
    ]);
    expect(queryKeys.catalog).toBe(catalogKeys);
    expect(queryKeys.anime).toBe(animeKeys);
    expect(queryKeys.rssList).toBe(rssListKeys);
    expect(queryKeys.config).toBe(configKeys);
    expect(queryKeys.automation).toBe(automationKeys);
    expect(queryKeys.observability).toBe(observabilityKeys);
    expect(queryKeys.fileMaintenance).toBe(fileMaintenanceKeys);
    expect(queryKeys.reconciliation).toBe(reconciliationKeys);
    expect(queryKeys.qbManagement).toBe(qbManagementKeys);
    expect(queryKeys.pluginDeliveries).toBe(pluginDeliveryKeys);
    expect(queryKeys.apiTokens).toBe(apiTokenKeys);
    expect(queryKeys.templateCatalog).toBe(templateCatalogKeys);
    expect(queryKeys.update).toBe(updateKeys);
    expect(queryKeys.backup).toBe(backupKeys);
  });

  it("emits the exact catalog tuples", () => {
    expect(catalogKeys.all).toEqual(["catalog"]);
    expect(catalogKeys.list("mikan", "2025-w01")).toEqual(["catalog", "list", "mikan", "2025-w01"]);
    expect(catalogKeys.preference("mikan", "2025-w01")).toEqual(["catalog", "preference", "mikan", "2025-w01"]);
    expect(catalogKeys.suppressions).toEqual(["catalog", "suppressions"]);
  });

  it("scopes the RSS list view per anime", () => {
    expect(rssListKeys.all).toEqual(["rss-list-view"]);
    expect(rssListKeys.owner("anime-1")).toEqual(["rss-list-view", "owner", "anime-1"]);
    expectPrefix(rssListKeys.all, rssListKeys.owner("anime-1"));
    expectPrefix(rssListKeys.owner("anime-1"), rssListKeys.ownerView("anime-1", 2, 3, ""));
    expect(rssListKeys.ownerView("anime-1", 2, 3, "").slice(0, 3)).not.toEqual(rssListKeys.owner("anime-2"));
  });

  it("emits the exact anime and configuration tuples", () => {
    expect(animeKeys.list).toEqual(["anime", "list"]);
    expect(animeKeys.workspacePrefix).toEqual(["anime", "workspace"]);
    expect(animeKeys.workspace("anime-42")).toEqual(["anime", "workspace", "anime-42"]);

    expect(configKeys.prefix).toEqual(["config"]);
    expect(configKeys.connections).toEqual(["config", "connections"]);
    expect(configKeys.globalSections).toEqual(["config", "global-sections"]);
    expect(configKeys.networkRoutes).toEqual(["config", "network-routes"]);
    expect(configKeys.pluginTargets).toEqual(["config", "plugin-targets"]);
  });

  it("emits the exact automation and observability tuples", () => {
    expect(automationKeys.schedules).toEqual(["automation", "schedules"]);

    expect(observabilityKeys.all).toEqual(["observability"]);
    expect(observabilityKeys.records).toEqual(["observability", "records"]);
    expect(observabilityKeys.diagnostics).toEqual(["observability", "diagnostics"]);
    expect(observabilityKeys.businessLine("success", "anime-42")).toEqual([
      "observability",
      "records",
      "business-line",
      "success",
      "anime-42",
      undefined,
      undefined,
    ]);
    expect(observabilityKeys.businessLine("failure", "anime-42", "2025-01-01", "2025-01-31")).toEqual([
      "observability",
      "records",
      "business-line",
      "failure",
      "anime-42",
      "2025-01-01",
      "2025-01-31",
    ]);
    expect(observabilityKeys.diagnosticsFiltered("error", "rss")).toEqual([
      "observability",
      "diagnostics",
      "error",
      "rss",
      undefined,
      undefined,
    ]);
    expect(observabilityKeys.diagnosticsFiltered("warning", "catalog", "2025-02-01", "2025-02-28")).toEqual([
      "observability",
      "diagnostics",
      "warning",
      "catalog",
      "2025-02-01",
      "2025-02-28",
    ]);
  });

  it("emits the exact file-maintenance and reconciliation tuples", () => {
    const animeID = "anime-42";

    expect(fileMaintenanceKeys.policy).toEqual(["file-maintenance", "policy"]);
    expect(fileMaintenanceKeys.retentionDefault).toEqual(["file-maintenance", "retention", "default"]);
    expect(fileMaintenanceKeys.fileEffectDefault).toEqual(["file-maintenance", "file-effect", "default"]);
    expect(fileMaintenanceKeys.files(animeID)).toEqual(["file-maintenance", "files", animeID]);
    expect(fileMaintenanceKeys.fileEffect(animeID)).toEqual(["file-maintenance", "file-effect", animeID]);
    expect(fileMaintenanceKeys.parserPolicy(animeID)).toEqual(["file-maintenance", "parser-policy", animeID]);
    expect(fileMaintenanceKeys.offsetMigrations(animeID)).toEqual(["file-maintenance", "offset-migrations", animeID]);
    expect(fileMaintenanceKeys.offsetMigrationGenerations(animeID)).toEqual([
      "file-maintenance",
      "offset-migration-generations",
      animeID,
    ]);
    expect(reconciliationKeys.pathDiffs(animeID, 7)).toEqual(["reconciliation", "path-diffs", animeID, 7]);
  });

  it("emits the exact qBittorrent and plugin-delivery tuples", () => {
    expect(qbManagementKeys.all).toEqual(["qb-management"]);
    expect(qbManagementKeys.torrents("connection-1")).toEqual(["qb-management", "torrents", "connection-1"]);
    expect(qbManagementKeys.torrents(null)).toEqual(["qb-management", "torrents", null]);
    expect(qbManagementKeys.status("connection-1")).toEqual(["qb-management", "status", "connection-1"]);
    expect(qbManagementKeys.status(null)).toEqual(["qb-management", "status", null]);
    expect(qbManagementKeys.files("connection-1", "hash-1")).toEqual([
      "qb-management",
      "files",
      "connection-1",
      "hash-1",
    ]);
    expect(qbManagementKeys.files(null, null)).toEqual(["qb-management", "files", null, null]);

    expect(pluginDeliveryKeys.list(20)).toEqual(["plugin-deliveries", 20]);
    expect(pluginDeliveryKeys.detail("delivery-1")).toEqual(["plugin-deliveries", "delivery-1"]);
  });

  it("emits the exact token, template, and update tuples", () => {
    expect(apiTokenKeys.all).toEqual(["api-tokens"]);
    expect(apiTokenKeys.raw("token-1")).toEqual(["api-token-raw", "token-1"]);

    expect(templateCatalogKeys.naming("js-naming-1")).toEqual(["template-catalog", "naming", "js-naming-1"]);
    expect(templateCatalogKeys.notification("release", "notification-catalog-v1")).toEqual(["template-catalog", "notification", "release", "notification-catalog-v1"]);

    expect(updateKeys.status).toEqual(["update", "status"]);
    expect(backupKeys.info).toEqual(["backup", "info"]);
  });

  it("preserves the prefixes used by realtime invalidation", () => {
    expectPrefix(catalogKeys.all, catalogKeys.list("all", "2025-w01"));
    expectPrefix(catalogKeys.all, catalogKeys.preference("mikan", "2025-w01"));
    expectPrefix(catalogKeys.all, catalogKeys.suppressions);

    expectPrefix(animeKeys.workspacePrefix, animeKeys.workspace("anime-42"));

    expectPrefix(observabilityKeys.all, observabilityKeys.records);
    expectPrefix(observabilityKeys.all, observabilityKeys.diagnostics);
    expectPrefix(observabilityKeys.records, observabilityKeys.businessLine("success", "anime-42"));
    expectPrefix(observabilityKeys.diagnostics, observabilityKeys.diagnosticsFiltered("error", "rss"));
  });

  it("preserves the shared prefixes for grouped query refreshes", () => {
    expectPrefix(configKeys.prefix, configKeys.connections);
    expectPrefix(configKeys.prefix, configKeys.globalSections);
    expectPrefix(configKeys.prefix, configKeys.networkRoutes);
    expectPrefix(configKeys.prefix, configKeys.pluginTargets);

    expectPrefix(qbManagementKeys.all, qbManagementKeys.torrents("connection-1"));
    expectPrefix(qbManagementKeys.all, qbManagementKeys.status("connection-1"));
    expectPrefix(qbManagementKeys.all, qbManagementKeys.files("connection-1", "hash-1"));
  });
});
