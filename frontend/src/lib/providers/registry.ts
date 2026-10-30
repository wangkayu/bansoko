import type { CatalogProvider } from "./port";
import { createCatalogProvider } from "./catalogProvider";
import type { ProviderKey, SearchProviderKey } from "./viewmodels";
import { t } from "../i18n";

// The single entry point resolving a catalog provider by key. Pages depend only on the
// returned CatalogProvider interface; wiring a real provider in means swapping the adapter
// here and nothing else — the UI stays unchanged.

function providerLabelOf(key: ProviderKey): string {
  switch (key) {
    case "anibt":
      return "AniBT";
    case "mikan":
      return "Mikan";
    case "custom":
      return t("ui.provider.custom");
  }
}

// Adapters are built lazily so the label follows the active UI language.
function buildProvider(key: ProviderKey): CatalogProvider {
  return createCatalogProvider(key, providerLabelOf(key));
}

/** The "subscribed" management-view adapter: only write operations such as state changes are used; catalog reads go through listAnime. */
function buildLibrary(): CatalogProvider {
  return createCatalogProvider("custom", t("ui.provider.library"));
}

export function getCatalogProvider(key: ProviderKey): CatalogProvider {
  return buildProvider(key);
}

export function getLibraryProvider(): CatalogProvider {
  return buildLibrary();
}

export function providerLabel(source: ProviderKey): string {
  return providerLabelOf(source);
}

/** Display name of a keyword torrent search source (the discovery providers plus search-only sources). */
export function searchProviderLabel(source: ProviderKey | SearchProviderKey): string {
  if (source === "nyaa") return "Nyaa";
  if (source === "jackett") return "Jackett";
  return providerLabelOf(source);
}
