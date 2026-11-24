import { t } from "./i18n";

// CJK bracket/episode markers mirrored from backend `DefaultEpisodePatterns` (not UI copy).
const OPEN_LENTICULAR = String.fromCharCode(0x3010);
const CLOSE_LENTICULAR = String.fromCharCode(0x3011);
const CJK_ORDINAL = String.fromCharCode(0x7b2c);
const CJK_EPISODE_UNIT = String.fromCharCode(0x8bdd, 0x8a71, 0x96c6);

/**
 * Mirrors backend `DefaultEpisodePatterns` in `internal/acquisition/domain/types.go`.
 * Used both as the one-click "fill built-in rules" preset and as the ordered fallback
 * when testing titles against an empty custom pattern list.
 */
export const DEFAULT_EPISODE_PATTERNS: readonly string[] = [
  `\\s-\\s*0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?(?:$|[\\s._\\-\\]${CLOSE_LENTICULAR}])`,
  "S[0-9]{1,2}\\s*E0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?",
  `(?:EP?|Episode|${CJK_ORDINAL})\\s*0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?(?:\\s*[${CJK_EPISODE_UNIT}])?`,
  `[\\[${OPEN_LENTICULAR}(]\\s*0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?\\s*[\\]${CLOSE_LENTICULAR})]`,
  `(?:^|[\\s._-])0*([0-9]{1,3}(?:\\.[0-9]+)?)(?:v[0-9]+)?(?:$|[\\s._\\-\\]${CLOSE_LENTICULAR})])`,
];

export const MAX_EPISODE_PATTERNS = 16;
export const MAX_PATTERN_BYTES = 512;

export type RegexDiagnostic = {
  severity: "error" | "warning";
  message: string;
};

/** Expands Go's `\Q...\E` quoted literals while respecting escaped backslashes (`\\Q`). */
function expandGoQuotedLiterals(raw: string): string {
  let out = "";
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] === "\\" && i + 1 < raw.length) {
      if (raw[i + 1] === "Q") {
        const start = i + 2;
        const end = raw.indexOf("\\E", start);
        const literal = end < 0 ? raw.slice(start) : raw.slice(start, end);
        out += literal.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
        if (end < 0) return out;
        i = end + 1;
        continue;
      }
      out += raw[i] + raw[i + 1];
      i++;
      continue;
    }
    out += raw[i];
  }
  return out;
}

/**
 * Strips escaped characters and `[...]` character classes so structural checks
 * (lookarounds, backreferences, capture groups) never false-positive on literals.
 */
function stripEscapesAndClasses(pattern: string): string {
  let out = "";
  let inClass = false;
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === "\\") {
      if (!inClass && i + 1 < pattern.length) {
        const next = pattern[i + 1];
        if (next >= "1" && next <= "9" && !(i + 2 < pattern.length && /\d/.test(pattern[i + 2]))) {
          out += "\\" + next;
        }
      }
      i++;
      continue;
    }
    if (!inClass && ch === "[") {
      inClass = true;
      continue;
    }
    if (inClass && ch === "]") {
      inClass = false;
      continue;
    }
    if (!inClass) out += ch;
  }
  return out;
}

const POSIX_CLASS_RE = /^\[:\^?(?:alnum|alpha|ascii|blank|cntrl|digit|graph|lower|print|punct|space|upper|word|xdigit):\]/;

/**
 * Detects valid Go RE2 constructs that JavaScript RegExp cannot evaluate faithfully:
 * - non-leading or scoped inline flags (`EP(?i)(\d+)`, `(?i:...)`)
 * - ungreedy flag `(?U)` (including in leading flags)
 * - text-boundary anchors `\A` / `\z`
 * - octal escapes (`\0`, `\12`, `\123`), bell (`\a`) and `\x{...}` code points
 * - POSIX character classes (`[[:digit:]]`, etc.)
 */
function hasPreviewUnsupportedGoSyntax(raw: string): boolean {
  const expanded = expandGoQuotedLiterals(raw);
  const leading = /^\(\?([imsU-]+)\)/.exec(expanded);
  if (leading && leading[1].split("-")[0].includes("U")) {
    return true;
  }
  const afterLeading = leading ? expanded.slice(leading[0].length) : expanded;
  if (/\(\?[imsU-]+(?:\)|:)/.test(stripEscapesAndClasses(afterLeading))) {
    return true;
  }
  let inClass = false;
  for (let i = 0; i < afterLeading.length; i++) {
    const ch = afterLeading[i];
    if (ch === "\\" && i + 1 < afterLeading.length) {
      const next = afterLeading[i + 1];
      if (!inClass && (next === "A" || next === "z")) return true;
      if (next === "a" || (next === "x" && afterLeading[i + 2] === "{")) return true;
      // Go octal escapes take two/three digits (or start with 0). A lone
      // non-zero digit remains an invalid escape and is diagnosed separately.
      if (next === "0" || (next >= "1" && next <= "9" && /\d/.test(afterLeading[i + 2] ?? ""))) return true;
      i++;
      continue;
    }
    if (!inClass && ch === "[") {
      inClass = true;
      continue;
    }
    if (inClass) {
      if (ch === "[" && POSIX_CLASS_RE.test(afterLeading.slice(i))) {
        return true;
      }
      if (ch === "]") {
        inClass = false;
      }
    }
  }
  return false;
}

const GO_PUNCT_OUTSIDE_CLASS = new Set('!"#%&\',-:;<=>@_`~'.split(""));
const GO_PUNCT_INSIDE_CLASS = new Set('!"#%&\',:;<=>@_`~'.split(""));

/**
 * Go RE2 allows escaping any ASCII punctuation (e.g. `\-`, `\_`, `\#`), while
 * JS Unicode mode (`u`) throws `Invalid escape` on non-syntax punctuation.
 * Unescape those harmless Go identity escapes before compiling with `"u"`.
 */
function unescapeGoPunctuation(pattern: string): string {
  let out = "";
  let inClass = false;
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === "\\" && i + 1 < pattern.length) {
      const next = pattern[i + 1];
      const safeSet = inClass ? GO_PUNCT_INSIDE_CLASS : GO_PUNCT_OUTSIDE_CLASS;
      if (safeSet.has(next)) {
        out += next;
      } else {
        out += ch + next;
      }
      i++;
      continue;
    }
    if (!inClass && ch === "[") {
      inClass = true;
    } else if (inClass && ch === "]") {
      inClass = false;
    }
    out += ch;
  }
  return out;
}

/**
 * Translates Go `\pX` shorthands and short script names (`\p{Han}`) only when
 * JavaScript's `"u"` mode requires `Script=` (keeping `\p{ASCII}`, `\p{Letter}`,
 * etc. untouched).
 */
function translateGoUnicodeProperties(source: string): string {
  const expandedShort = source.replace(/\\([pP])([CLMNPSZ])/g, "\\$1{$2}");
  return expandedShort.replace(/\\([pP])\{([A-Za-z][A-Za-z0-9_]*)\}/g, (match, kind: string, name: string) => {
    try {
      new RegExp(`\\${kind}{${name}}`, "u");
      return match;
    } catch {
      try {
        const scriptForm = `\\${kind}{Script=${name}}`;
        new RegExp(scriptForm, "u");
        return scriptForm;
      } catch {
        return match;
      }
    }
  });
}

/**
 * Translates the Go RE2 constructs that have an exact JavaScript equivalent.
 * Unicode mode also makes JS reject Perl-only unknown escapes (such as `\R`)
 * that Go rejects, while `unescapeGoPunctuation` preserves valid Go punctuation
 * escapes (`\-`, `\_`, `\#`, etc.).
 */
export function compileGoRegexPreview(raw: string): RegExp {
  let source = translateGoUnicodeProperties(
    unescapeGoPunctuation(expandGoQuotedLiterals(raw)).replace(/\(\?P<([A-Za-z_][A-Za-z0-9_]*)>/g, "(?<$1>"),
  );
  let flags = "u";
  const leadingFlags = /^\(\?([imsU-]+)\)/.exec(source);
  if (leadingFlags) {
    const enabled = leadingFlags[1].split("-")[0];
    if (enabled.includes("i")) flags += "i";
    if (enabled.includes("m")) flags += "m";
    if (enabled.includes("s")) flags += "s";
    source = source.slice(leadingFlags[0].length);
  }
  return new RegExp(source, flags);
}

/**
 * Validates common Go RE2 syntax and mirrors the backend's 512-byte limit.
 * Definite Go RE2 incompatibilities return `severity: "error"`; valid Go
 * constructs that JS cannot preview faithfully return `severity: "warning"`.
 */
export function validateGoRegex(raw: string, opts: { requireCapture?: boolean } = {}): RegexDiagnostic | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;

  const byteLen = new TextEncoder().encode(raw).length;
  if (byteLen > MAX_PATTERN_BYTES) {
    return { severity: "error", message: t("forms.rules.patternEditor.tooLong", { max: MAX_PATTERN_BYTES, current: byteLen }) };
  }

  const structural = stripEscapesAndClasses(expandGoQuotedLiterals(trimmed));
  if (/\(\?(?:=|!|<=|<!)/.test(structural)) {
    return { severity: "error", message: t("forms.rules.patternEditor.re2Lookaround") };
  }
  if (/\\[1-9]/.test(structural)) {
    return { severity: "error", message: t("forms.rules.patternEditor.re2Backref") };
  }
  if (hasPreviewUnsupportedGoSyntax(trimmed)) {
    return { severity: "warning", message: t("forms.rules.patternEditor.re2PreviewLimited") };
  }

  try {
    compileGoRegexPreview(trimmed);
  } catch {
    return { severity: "error", message: t("settings.rules.invalidRegex") };
  }

  if (raw !== trimmed) {
    return { severity: "warning", message: t("forms.rules.patternEditor.whitespaceTrimmed") };
  }

  if (opts.requireCapture) {
    const hasCapture = /\((?!\?)|\(\?P<|\(\?<[^=!]/.test(structural);
    if (!hasCapture) {
      return { severity: "warning", message: t("forms.rules.patternEditor.missingCapture") };
    }
  }

  return null;
}

/** Returns true when a pattern list exceeds the backend limit or has a definite RE2 error. */
export function hasBlockingEpisodePatternError(patterns: readonly string[]): boolean {
  return (
    patterns.length > MAX_EPISODE_PATTERNS ||
    patterns.some((pattern) => validateGoRegex(pattern)?.severity === "error")
  );
}

export type EpisodeTestMatchResult =
  | { matched: false; usedDefaults: boolean }
  | {
      matched: true;
      usedDefaults: boolean;
      ruleIndex: number;
      episode: string;
      matchStart: number;
      matchEnd: number;
      captureStart: number;
      captureEnd: number;
    };

/** Mirrors CanonicalEpisode plus the parser's captured vN suffix removal. */
function normalizeEpisodeCapture(group: string): { raw: string; episode: string } | undefined {
  let raw = group.trim();
  const version = raw.toLowerCase().indexOf("v");
  if (version > 0) raw = raw.slice(0, version);
  if (!/^\d+(?:\.\d+)?$/.test(raw)) return undefined;
  let [integer, fraction] = raw.split(".");
  integer = integer.replace(/^0+/, "") || "0";
  fraction = fraction?.replace(/0+$/, "") ?? "";
  const episode = fraction ? `${integer}.${fraction}` : integer;
  if (!fraction && BigInt(integer) > 100000n) return undefined;
  return { raw, episode };
}

/**
 * Evaluates one title with the backend's ordered first-match behavior: select
 * the first non-empty capture, strip vN, then canonicalize the decimal. An
 * empty entry runs the built-in rules at that position, as ParseEpisodeExact does.
 */
export function testEpisodePatterns(title: string, patterns: readonly string[]): EpisodeTestMatchResult {
  const usedDefaults = patterns.length === 0;
  const activeList = usedDefaults ? DEFAULT_EPISODE_PATTERNS : patterns;
  if (!title.trim()) return { matched: false, usedDefaults };

  for (let i = 0; i < activeList.length; i++) {
    const raw = activeList[i];
    if (!usedDefaults && !raw.trim()) {
      const fallback = testEpisodePatterns(title, []);
      if (fallback.matched) return fallback;
      continue;
    }
    const diag = validateGoRegex(raw);
    if (diag?.severity === "error" || hasPreviewUnsupportedGoSyntax(raw)) continue;
    try {
      const re = compileGoRegexPreview(raw.trim());
      const m = re.exec(title);
      if (!m) continue;
      const group = m.slice(1).find((capture) => capture !== undefined && capture.trim() !== "");
      if (group === undefined) continue;
      const normalized = normalizeEpisodeCapture(group);
      if (!normalized) continue;
      const relCap = m[0].indexOf(normalized.raw);
      const captureStart = relCap >= 0 ? m.index + relCap : m.index;
      const captureEnd = relCap >= 0 ? captureStart + normalized.raw.length : m.index + m[0].length;
      return {
        matched: true,
        usedDefaults,
        ruleIndex: i,
        episode: normalized.episode,
        matchStart: m.index,
        matchEnd: m.index + m[0].length,
        captureStart,
        captureEnd,
      };
    } catch {
      continue;
    }
  }
  return { matched: false, usedDefaults };
}
