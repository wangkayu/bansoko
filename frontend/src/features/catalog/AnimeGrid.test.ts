import { describe, expect, it } from "vitest";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { catalogRowKey } from "./visibleRows";

function entry(overrides: Partial<CatalogEntryView> = {}): CatalogEntryView {
  return {
    providerEntryRef: "mikan:entry-1",
    source: "mikan",
    title: "节目",
    metadataMatch: { matched: false },
    ...overrides,
  };
}

describe("catalogRowKey", () => {
  it("gives two owner associations sharing one provider ref distinct React keys", () => {
    // Two existing owners of the same external provider entry: identical browse attributes, distinct
//     formal identities. The keys must be each one's managed_anime.id, or React treats them as one
//     entry.
    const first = entry({ managedAnimeRef: "anime-a", subscriptionState: "active" });
    const second = entry({ managedAnimeRef: "anime-b", subscriptionState: "stopped" });
    expect(catalogRowKey(first)).toBe("anime-a");
    expect(catalogRowKey(second)).toBe("anime-b");
    expect(catalogRowKey(first)).not.toBe(catalogRowKey(second));
  });

  it("keeps an unsubscribed browse row keyed by its provider ref", () => {
    expect(catalogRowKey(entry())).toBe("mikan:entry-1");
  });
});
