import type { CatalogEntryView } from "../../lib/providers/viewmodels";

export function catalogListMatches(query: string): (entry: CatalogEntryView) => boolean {
  const terms = [...query.toLowerCase().matchAll(/(-?)"([^"]+)"|(-?)([^\s"]+)/g)].map((term) => ({
    exclude: (term[1] ?? term[3]) === "-", text: term[2] ?? term[4],
  }));
  return (entry) => {
    const fields = [entry.title, entry.originalTitle, entry.source === "custom" ? entry.instanceLabel : undefined]
      .map((field) => field?.toLowerCase());
    return terms.every(({ exclude, text }) => fields.some((field) => field?.includes(text)) !== exclude);
  };
}
