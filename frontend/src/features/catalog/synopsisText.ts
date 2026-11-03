/** Splits only on a marker that has content on both sides; everything else stays one block. */
export function splitSynopsis(text: string | null | undefined): { summary: string; original: string } | null {
  if (!text) return null;
  const match = /\[简介原文\]|【简介原文】/.exec(text);
  if (!match) return null;
  const summary = text.slice(0, match.index).trim();
  const original = text.slice(match.index + match[0].length).trim();
  if (!summary || !original) return null;
  return { summary, original };
}
