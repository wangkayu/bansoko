import { getLocale, t } from "./index";

// Text assembled in code uses the current language's punctuation and date formats; components never hardcode one language's punctuation.

/** "Group: value" / "Label: value". */
export function labelValue(label: string, value: string | number): string {
  return t("common.format.labelValue", { label, value });
}

/** "Text (note)" / "Text (note)". */
export function withNote(text: string, note: string): string {
  return t("common.format.withNote", { text, note });
}

/** "(text)" / "(text)". */
export function parenthesized(text: string): string {
  return t("common.format.parenthesized", { text });
}

/** "Item A and item B" / "A, B". */
export function joinList(items: readonly string[]): string {
  return items.join(t("common.format.listSeparator"));
}

/** Multi-sentence descriptions joined (no space between Chinese sentences, one space in English). */
export function joinSentences(...sentences: string[]): string {
  return sentences.join(t("common.format.sentenceGap"));
}

/** Date and time (24-hour clock). */
export function formatLocaleDateTime(date: Date): string {
  return date.toLocaleString(getLocale(), { hour12: false });
}

/** Date only. */
export function formatLocaleDate(date: Date): string {
  return date.toLocaleDateString(getLocale());
}

/** Month-day and hour-minute for compact rows (24-hour clock); the full time goes into title. */
export function formatLocaleShortDateTime(date: Date): string {
  return date.toLocaleString(getLocale(), { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
}
