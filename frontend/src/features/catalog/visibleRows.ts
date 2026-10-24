import type { CatalogEntryView } from "../../lib/providers/viewmodels";

/** The grid row key: subscriptions on the subscribed wall always use managed_anime.id, so several
 *  subscriptions under one external provider entry get distinct keys; provider browse/search
 *  candidate cards carry no subscription identity and key on the provider ref. A provider ref is
 *  not an owner identity. */
export function catalogRowKey(entry: CatalogEntryView): string {
  return entry.managedAnimeRef ?? entry.providerEntryRef;
}
