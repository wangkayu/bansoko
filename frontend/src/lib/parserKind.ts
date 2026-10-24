import { t } from "./i18n";

// User-visible names and one-line notes for parser values. `regex` displays as "regex" and
// `reltitle` keeps its product name; the display_name a catalog gives is an English code
// description, and using it directly would leak internal naming into the UI. An unknown parser
// in the catalog uses the display name the catalog gives, falling back to the raw value when
// missing — no invented names. The instance card and the global default card share this one
// mapping.
export function parserKindLabel(kind: string, displayName?: string): string {
  if (kind === "regex") return t("policy.parser.kindRegex");
  if (kind === "reltitle") return t("policy.parser.kindReltitle");
  return displayName && displayName !== "" ? displayName : kind;
}

export function parserKindDescription(kind: string): string | undefined {
  if (kind === "regex") return t("policy.parser.kindRegexDescription");
  if (kind === "reltitle") return t("policy.parser.kindReltitleDescription");
  return undefined;
}

/** Episode recognition rules apply only to regex; other parsers ignore them (the server neither reads nor clears them). */
export function parserKindUsesPatterns(kind: string | undefined): boolean {
  return kind === undefined || kind === "regex";
}

export type ParserKindOption = { value: string; label: string; description?: string };

// Keeps only parsers that can recognize an episode number from an RSS title (the
// release_title capability), in catalog order; a current value absent from the catalog still
// shows as an option so a saved value never displays as an empty selection.
export function parserKindOptions(
  catalog: ReadonlyArray<{ id: string; display_name: string; capabilities: string[] }> | undefined,
  current: string | undefined,
): ParserKindOption[] {
  const options: ParserKindOption[] = [];
  const seen = new Set<string>();
  for (const entry of Array.isArray(catalog) ? catalog : []) {
    if (!entry.capabilities.includes("release_title") || seen.has(entry.id)) continue;
    seen.add(entry.id);
    options.push({ value: entry.id, label: parserKindLabel(entry.id, entry.display_name), description: parserKindDescription(entry.id) });
  }
  if (current !== undefined && current !== "" && !seen.has(current)) {
    options.push({ value: current, label: parserKindLabel(current), description: parserKindDescription(current) });
  }
  return options;
}
