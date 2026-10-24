import { providerLabel } from "./providers/registry";
import type { ProviderKey } from "./providers/viewmodels";
import { t } from "./i18n";

// The UI never shows a subgroup's group_ref. Only when two same-named subgroups appear under
// one anime is the name disambiguated with the site name (different sites) or a short tag
// (same site); a unique name displays as written.

export type GroupNameInput = { provider: string; ref: string; name: string };

export function groupKey(provider: string, ref: string): string {
  return `${provider}:${ref}`;
}

function shortId(ref: string): string {
  return ref.length > 6 ? ref.slice(-6) : ref;
}

function siteName(provider: string): string {
  return provider === "anibt" || provider === "mikan" || provider === "custom"
    ? providerLabel(provider as ProviderKey)
    : provider;
}

/** Returns `provider:ref` -> display name. Duplicate-named groups get a disambiguating suffix; the rest keep their name. */
export function disambiguateGroupNames(groups: readonly GroupNameInput[]): Map<string, string> {
  const byName = new Map<string, GroupNameInput[]>();
  for (const group of groups) {
    const list = byName.get(group.name) ?? [];
    list.push(group);
    byName.set(group.name, list);
  }
  const labels = new Map<string, string>();
  for (const [name, list] of byName) {
    const unique = new Map(list.map((group) => [groupKey(group.provider, group.ref), group] as const));
    if (unique.size <= 1) {
      for (const key of unique.keys()) labels.set(key, name);
      continue;
    }
    const providers = new Set([...unique.values()].map((group) => group.provider));
    const bySite = providers.size === unique.size;
    for (const [key, group] of unique) {
      const tag = bySite ? siteName(group.provider) : t("anime.sources.groupShortId", { id: shortId(group.ref) });
      labels.set(key, t("anime.sources.groupNameWithTag", { name, tag }));
    }
  }
  return labels;
}

export function groupLabel(labels: ReadonlyMap<string, string>, provider: string, ref: string, fallback: string): string {
  return labels.get(groupKey(provider, ref)) ?? fallback;
}
