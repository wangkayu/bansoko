import { t, tryT } from "../../lib/i18n";
import type { TemplateFactPhase } from "../../lib/api/client";

/** A catalog entry of either template catalog (naming field or notification variable). */
export type CatalogFact = {
  path: string;
  nullable: boolean;
  phase?: TemplateFactPhase;
  sample_value?: string | null;
  label?: string;
  description?: string;
  label_key?: string;
  description_key?: string;
};

/** The variable name: the registry copy key first, then the name the API gives, finally the path itself. */
export function factLabel(fact: CatalogFact): string {
  return (fact.label_key && tryT(fact.label_key)) || fact.label || fact.path;
}

export function factDescription(fact: CatalogFact): string | undefined {
  return (fact.description_key && tryT(fact.description_key)) || fact.description || undefined;
}

/** The group title: the copy for the root name when present, otherwise the name as-is. */
export function factRootLabel(root: string): string {
  return tryT(`templateFacts.roots.${root}`) ?? root;
}

/** Groups by root, keeping the catalog order. */
export function groupFacts<T extends CatalogFact>(facts: readonly T[]): [string, T[]][] {
  const groups = new Map<string, T[]>();
  for (const fact of facts) {
    const root = fact.path.split(".")[0] ?? fact.path;
    const bucket = groups.get(root);
    if (bucket) bucket.push(fact);
    else groups.set(root, [fact]);
  }
  return Array.from(groups.entries());
}

/** Searches by path, name or group name (case-insensitive). */
export function filterFacts<T extends CatalogFact>(facts: readonly T[], query: string): T[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...facts];
  return facts.filter((fact) => {
    const root = fact.path.split(".")[0] ?? fact.path;
    return [fact.path, factLabel(fact), factRootLabel(root)].some((text) => text.toLowerCase().includes(needle));
  });
}

/** A one-line hover tip: description, example, availability timing, nullable. */
export function factTitle(fact: CatalogFact): string {
  const parts = [factDescription(fact) ?? factLabel(fact)];
  if (fact.sample_value != null) parts.push(t("templateFacts.example", { value: fact.sample_value }));
  if (fact.phase === "execution") parts.push(t("templateFacts.phaseExecutionHint"));
  if (fact.nullable) parts.push(t("templateFacts.nullable"));
  return parts.join(" · ");
}

const PLACEHOLDER = /⟨([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*){1,2})⟩/g;

/** The placeholder's display text; with no copy, the path is used. */
export function placeholderText(path: string): string {
  return t("templateFacts.placeholder", { label: tryT(`facts.${path}.label`) ?? path });
}

/** Splits preview text into plain fragments and execution-time placeholders. */
export function splitPlaceholders(text: string): { text: string; placeholder?: string }[] {
  const parts: { text: string; placeholder?: string }[] = [];
  let last = 0;
  for (const match of text.matchAll(PLACEHOLDER)) {
    const index = match.index ?? 0;
    if (index > last) parts.push({ text: text.slice(last, index) });
    parts.push({ text: match[0], placeholder: match[1] });
    last = index + match[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last) });
  return parts;
}
