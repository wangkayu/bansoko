import type {
  NamingVarsDiagnostic,
  NamingVarsDryRunTarget,
  NamingVarsInputFact,
  NamingVarsVersion,
} from "../../lib/api/client";
import { t, tryT } from "../../lib/i18n";
import { labelValue } from "../../lib/i18n/format";
import type { CatalogFact } from "../templateFacts/facts";

// Pure logic for the variables-program editor: offset conversion, diagnostic placement, insertion
//   spelling, draft state. The server's start/end are UTF-8 byte offsets into the normalized source
//   (LF line endings); the editor indexes by UTF-16.

export type EditorRange = { from: number; to: number };

const encoder = new TextEncoder();

/** Normalizes CRLF/CR to LF, matching the server's normalized source. */
export function normalizeSource(text: string): string {
  return text.replace(/\r\n?/g, "\n");
}

/** Converts a UTF-8 byte offset to a UTF-16 index; mid-character lands at the character's start, past the end clamps to the end of the text. */
export function byteOffsetToIndex(text: string, byteOffset: number): number {
  if (byteOffset <= 0) return 0;
  let bytes = 0;
  let index = 0;
  while (index < text.length) {
    const codePoint = text.codePointAt(index) ?? 0;
    const width = encoder.encode(String.fromCodePoint(codePoint)).length;
    if (bytes + width > byteOffset) return index;
    bytes += width;
    index += codePoint > 0xffff ? 2 : 1;
  }
  return text.length;
}

/** The server diagnostic's range in the text; undefined when there is no position or it is invalid. Always covers at least one character. */
export function diagnosticRange(text: string, diagnostic: Pick<NamingVarsDiagnostic, "start" | "end">): EditorRange | undefined {
  if (diagnostic.start === undefined || diagnostic.end === undefined || diagnostic.end < diagnostic.start) return undefined;
  const from = byteOffsetToIndex(text, diagnostic.start);
  let to = byteOffsetToIndex(text, diagnostic.end);
  if (to <= from) to = Math.min(text.length, from + 1);
  if (to <= from) return text.length > 0 ? { from: text.length - 1, to: text.length } : undefined;
  return { from, to };
}

/** The line number of an index, starting at 1. */
export function lineOf(text: string, index: number): number {
  let line = 1;
  const end = Math.min(Math.max(index, 0), text.length);
  for (let i = 0; i < end; i += 1) if (text.charCodeAt(i) === 10) line += 1;
  return line;
}

export type EditorMarker = {
  from: number;
  to: number;
  message: string;
  severity: "error" | "warning";
};

/** Maps a diagnostic onto an editor marker; diagnostics without a position produce no marker (they live only in the problem list). */
export function markersFor(
  text: string,
  diagnostics: readonly Pick<NamingVarsDiagnostic, "start" | "end" | "message">[],
  severity: EditorMarker["severity"],
): EditorMarker[] {
  const out: EditorMarker[] = [];
  for (const diagnostic of diagnostics) {
    const range = diagnosticRange(text, diagnostic);
    if (range) out.push({ ...range, message: diagnostic.message, severity });
  }
  return out;
}

/** The spelling inserted into the program. */
export function inputToken(path: string): string {
  return `input.${path}`;
}

/** The catalog's release title and file listing: always readable by programs, and not part of the facts registry. */
export const BASE_INPUTS: readonly CatalogFact[] = [
  { path: "release_title", nullable: false, label_key: "varsEditor.base.release_title.label", description_key: "varsEditor.base.release_title.description" },
  { path: "files", nullable: false, label_key: "varsEditor.base.files.label", description_key: "varsEditor.base.files.description" },
];

/** Converts the API's input facts into fact-catalog entries. */
export function catalogFacts(facts: readonly NamingVarsInputFact[]): CatalogFact[] {
  return [
    ...BASE_INPUTS,
    ...facts.map((fact) => ({
      path: fact.path,
      nullable: fact.nullable,
      sample_value: fact.sample_value ?? null,
      label_key: fact.label_key,
      description_key: fact.description_key,
    })),
  ];
}

/** The dry-run subject: a built-in sample, the most recent real release, or a hand-typed title. */
export type DryRunSelection = {
  samples: readonly string[];
  releaseId: string;
  customTitle: string;
};

export function dryRunTargets(selection: DryRunSelection): NamingVarsDryRunTarget[] {
  const targets: NamingVarsDryRunTarget[] = selection.samples.map((sample) => ({ kind: "sample", sample }));
  if (selection.releaseId) targets.push({ kind: "release", release_id: selection.releaseId });
  const title = selection.customTitle.trim();
  if (title) targets.push({ kind: "custom", title });
  return targets;
}

/** The ordering used by versions' short labels in the history: newest first. */
export function newestFirst(versions: readonly NamingVarsVersion[]): NamingVarsVersion[] {
  return [...versions].sort((a, b) => b.revision - a.revision);
}

export type SaveBlocker = "unchanged" | "empty" | "tooLarge";

/** The local check before saving; passing returns undefined. Size counted in UTF-8 bytes. */
export function saveBlocker(source: string, activeSource: string, maxBytes: number): SaveBlocker | undefined {
  const normalized = normalizeSource(source);
  if (normalized.trim() === "") return "empty";
  if (encoder.encode(normalized).length > maxBytes) return "tooLarge";
  if (normalized === activeSource) return "unchanged";
  return undefined;
}

type ProblemLike = { code: string; message: string; start?: number };

/** A problem's display text: the category, the engine's original message, prefixed with a line number when a position exists. */
export function problemText(text: string, problem: ProblemLike): string {
  const kind = tryT(`varsEditor.diagnostic.${problem.code}`);
  const message = kind ? labelValue(kind, problem.message) : problem.message;
  if (problem.start === undefined) return message;
  const line = lineOf(text, byteOffsetToIndex(text, problem.start));
  return t("varsEditor.editor.problemAt", { place: t("varsEditor.editor.line", { n: line }), message });
}
