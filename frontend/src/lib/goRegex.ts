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
/** Mirrors `MaxFilterPatternBytes` in `internal/anime/domain/regex_filter.go`. */
export const MAX_FILTER_PATTERN_BYTES = 4096;

export type RegexDiagnostic = {
  severity: "error" | "warning";
  message: string;
};

const POSIX_CLASS_RE = /^\[:\^?(?:alnum|alpha|ascii|blank|cntrl|digit|graph|lower|print|punct|space|upper|word|xdigit):\]/;
// Go RE2 inline flag syntax requires at least one flag before or after `-` (rejects `(?-)`, `(?--i)`, `(?i-)`).
const LEADING_FLAGS_RE = /^\(\?([imsU]+(?:-[imsU]+)?|-[imsU]+)\)/;
const INLINE_FLAGS_TOKEN_RE = /^\(\?(?:[imsU]+(?:-[imsU]+)?|-[imsU]+)([):])/;
const GO_HEX_BRACE_RE = /^\\x\{([0-9A-Fa-f]+)\}/;

const GO_PUNCT_OUTSIDE_CLASS = new Set('!"#%&\',-:;<=>@_`~'.split(""));
const GO_PUNCT_INSIDE_CLASS = new Set('!"#%&\',:;<=>@_`~'.split(""));

function isOctalDigit(ch: string | undefined): boolean {
  return ch !== undefined && ch >= "0" && ch <= "7";
}

/**
 * Returns the digit count (1..3) of a valid Go RE2 octal escape at `escapeIndex`
 * (`\0[0-7]{0,2}` or `\[1-7][0-7]{1,2}`), or 0 if not a valid Go octal escape.
 */
function matchGoOctalDigits(pattern: string, escapeIndex: number): number {
  const d1 = pattern[escapeIndex + 1];
  if (d1 === "0") {
    let len = 1;
    if (isOctalDigit(pattern[escapeIndex + 2])) {
      len++;
      if (isOctalDigit(pattern[escapeIndex + 3])) len++;
    }
    return len;
  }
  if (d1 !== undefined && d1 >= "1" && d1 <= "7" && isOctalDigit(pattern[escapeIndex + 2])) {
    return isOctalDigit(pattern[escapeIndex + 3]) ? 3 : 2;
  }
  return 0;
}

/**
 * Matches a valid Go `\x{hex}` escape (code point <= 0x10FFFF) at `escapeIndex`.
 * Returns the matched raw length and parsed code point, or null if invalid.
 */
function matchGoHexBrace(pattern: string, escapeIndex: number): { length: number; codePoint: number } | null {
  const m = GO_HEX_BRACE_RE.exec(pattern.slice(escapeIndex));
  if (!m) return null;
  const codePoint = Number.parseInt(m[1], 16);
  if (!Number.isFinite(codePoint) || codePoint > 0x10ffff) return null;
  return { length: m[0].length, codePoint };
}

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
 * Respects leading literal `]` in `[...]`, nested POSIX classes, and valid Go octal escapes.
 */
function stripEscapesAndClasses(pattern: string): string {
  let out = "";
  let inClass = false;
  let classFirst = false;
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === "\\" && i + 1 < pattern.length) {
      const octalLen = matchGoOctalDigits(pattern, i);
      if (octalLen > 0) {
        i += octalLen;
        classFirst = false;
        continue;
      }
      const next = pattern[i + 1];
      if (!inClass && next >= "1" && next <= "9") {
        out += "\\" + next;
      }
      i++;
      classFirst = false;
      continue;
    }
    if (!inClass && ch === "[") {
      inClass = true;
      classFirst = true;
      continue;
    }
    if (inClass) {
      if (classFirst && ch === "^") {
        continue;
      }
      if (ch === "[" && POSIX_CLASS_RE.test(pattern.slice(i))) {
        const m = POSIX_CLASS_RE.exec(pattern.slice(i))!;
        i += m[0].length - 1;
        classFirst = false;
        continue;
      }
      if (ch === "]" && !classFirst) {
        inClass = false;
        continue;
      }
      classFirst = false;
      continue;
    }
    out += ch;
  }
  return out;
}

/**
 * Detects valid Go RE2 constructs that JavaScript RegExp cannot evaluate faithfully:
 * - non-leading or scoped inline flags (`EP(?i)(\d+)`, `(?i:...)`)
 * - ungreedy flag `(?U)` (including in leading flags)
 * - text-boundary anchors `\A` / `\z`
 * - valid Go octal escapes (`\0`, `\12`, `\123`), bell (`\a`) and valid `\x{...}` code points
 * - POSIX character classes (`[[:digit:]]`, etc.)
 */
function hasPreviewUnsupportedGoSyntax(raw: string): boolean {
  const expanded = expandGoQuotedLiterals(raw);
  const leading = LEADING_FLAGS_RE.exec(expanded);
  if (leading && leading[1].split("-")[0].includes("U")) {
    return true;
  }
  const afterLeading = leading ? expanded.slice(leading[0].length) : expanded;
  let inClass = false;
  let classFirst = false;
  for (let i = 0; i < afterLeading.length; i++) {
    const ch = afterLeading[i];
    if (ch === "\\" && i + 1 < afterLeading.length) {
      const next = afterLeading[i + 1];
      if (!inClass && (next === "A" || next === "z")) return true;
      if (next === "a") return true;
      if (next === "x" && afterLeading[i + 2] === "{") {
        const hex = matchGoHexBrace(afterLeading, i);
        if (hex) return true;
      }
      const octalLen = matchGoOctalDigits(afterLeading, i);
      if (octalLen > 0) return true;
      i++;
      classFirst = false;
      continue;
    }
    if (!inClass) {
      if (ch === "(" && INLINE_FLAGS_TOKEN_RE.test(afterLeading.slice(i))) {
        return true;
      }
      if (ch === "[") {
        inClass = true;
        classFirst = true;
      }
      continue;
    }
    if (classFirst && ch === "^") {
      continue;
    }
    if (ch === "[" && POSIX_CLASS_RE.test(afterLeading.slice(i))) {
      return true;
    }
    if (ch === "]" && !classFirst) {
      inClass = false;
      continue;
    }
    classFirst = false;
  }
  return false;
}

/**
 * Translates Go `\pX` / `\p{Name}` Unicode property escapes, Go punctuation identity
 * escapes (`\-`, `\_`, `\#`), Go leading-`]` character classes (`[]a]` -> `[\]a]`),
 * and Go named capture groups (`(?P<name>` -> `(?<name>`) while respecting escaped
 * backslashes (`\\pL`) and character class boundaries.
 *
 * When `placeholderUnsupported` is true, also substitutes syntax-equivalent JS
 * tokens for valid preview-unsupported Go constructs (`\A`, `\z`, `\a`, valid
 * `\x{...}`, valid Go octal escapes, POSIX classes, and scoped/non-leading flags)
 * so structural syntax errors (e.g. `\A[unclosed`, `(?i:unclosed`, `\x{ZZ}`) are
 * still caught by the JS compiler.
 */
function translateGoToJsSource(expanded: string, placeholderUnsupported: boolean): string {
  let out = "";
  let inClass = false;
  let classFirst = false;
  for (let i = 0; i < expanded.length; i++) {
    const ch = expanded[i];
    if (ch === "\\" && i + 1 < expanded.length) {
      const next = expanded[i + 1];
      if (placeholderUnsupported) {
        if (!inClass && next === "A") {
          out += "^";
          i++;
          continue;
        }
        if (!inClass && next === "z") {
          out += "$";
          i++;
          continue;
        }
        if (next === "a") {
          out += "\\x07";
          i++;
          classFirst = false;
          continue;
        }
        if (next === "x" && expanded[i + 2] === "{") {
          const hex = matchGoHexBrace(expanded, i);
          if (hex) {
            out += `\\u{${hex.codePoint.toString(16)}}`;
            i += hex.length - 1;
            classFirst = false;
            continue;
          }
        }
        const octalLen = matchGoOctalDigits(expanded, i);
        if (octalLen > 0) {
          const codePoint = Number.parseInt(expanded.slice(i + 1, i + 1 + octalLen), 8);
          out += `\\u{${codePoint.toString(16)}}`;
          i += octalLen;
          classFirst = false;
          continue;
        }
      }

      if (next === "p" || next === "P") {
        const rest = expanded.slice(i);
        const braced = /^\\([pP])\{([A-Za-z][A-Za-z0-9_]*)\}/.exec(rest);
        if (braced) {
          out += translateUnicodeProperty(braced[0], braced[1], braced[2]);
          i += braced[0].length - 1;
          classFirst = false;
          continue;
        }
        const shortProp = /^\\([pP])([CLMNPSZ])/.exec(rest);
        if (shortProp) {
          out += `\\${shortProp[1]}{${shortProp[2]}}`;
          i += shortProp[0].length - 1;
          classFirst = false;
          continue;
        }
      }

      const safeSet = inClass ? GO_PUNCT_INSIDE_CLASS : GO_PUNCT_OUTSIDE_CLASS;
      out += safeSet.has(next) ? next : ch + next;
      i++;
      classFirst = false;
      continue;
    }

    if (!inClass) {
      if (ch === "(") {
        if (placeholderUnsupported) {
          const inlineFlags = INLINE_FLAGS_TOKEN_RE.exec(expanded.slice(i));
          if (inlineFlags) {
            out += inlineFlags[1] === ":" ? "(?:" : "(?:)";
            i += inlineFlags[0].length - 1;
            continue;
          }
        }
        const namedGroup = /^\(\?P<([A-Za-z_][A-Za-z0-9_]*)>/.exec(expanded.slice(i));
        if (namedGroup) {
          out += `(?<${namedGroup[1]}>`;
          i += namedGroup[0].length - 1;
          continue;
        }
      }
      if (ch === "[") {
        inClass = true;
        classFirst = true;
      }
      out += ch;
      continue;
    }

    if (classFirst && ch === "^") {
      out += ch;
      continue;
    }
    if (placeholderUnsupported && ch === "[") {
      const posix = POSIX_CLASS_RE.exec(expanded.slice(i));
      if (posix) {
        out += "a";
        i += posix[0].length - 1;
        classFirst = false;
        continue;
      }
    }
    if (ch === "]" && classFirst) {
      // Go allows an unescaped literal `]` as the first member of `[...]` / `[^...]`;
      // JS `"u"` mode requires escaping it.
      out += "\\]";
      classFirst = false;
      continue;
    }
    if (ch === "]") {
      inClass = false;
    } else {
      classFirst = false;
    }
    out += ch;
  }
  return out;
}

function translateUnicodeProperty(match: string, kind: string, name: string): string {
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
}

/**
 * Translates the Go RE2 constructs that have an exact JavaScript equivalent.
 * Unicode mode also makes JS reject Perl-only unknown escapes (such as `\R`)
 * that Go rejects, while `translateGoToJsSource` preserves valid Go punctuation
 * escapes (`\-`, `\_`, `\#`, etc.) and leading-`]` character classes (`[]a]`).
 */
export function compileGoRegexPreview(raw: string, opts: { extraFlags?: string } = {}): RegExp {
  const expanded = expandGoQuotedLiterals(raw);
  let flags = "u" + (opts.extraFlags ?? "");
  let body = expanded;
  const leadingFlags = LEADING_FLAGS_RE.exec(expanded);
  if (leadingFlags) {
    const enabled = leadingFlags[1].split("-")[0];
    if (enabled.includes("i")) flags += "i";
    if (enabled.includes("m")) flags += "m";
    if (enabled.includes("s")) flags += "s";
    body = expanded.slice(leadingFlags[0].length);
  }
  return new RegExp(translateGoToJsSource(body, false), flags);
}

/** Compiles a syntax-checked JS equivalent even when preview-unsupported Go syntax is present. */
function compileGoRegexSyntaxCheck(raw: string): RegExp {
  const expanded = expandGoQuotedLiterals(raw);
  const leadingFlags = LEADING_FLAGS_RE.exec(expanded);
  const body = leadingFlags ? expanded.slice(leadingFlags[0].length) : expanded;
  return new RegExp(translateGoToJsSource(body, true), "u");
}

/**
 * Validates common Go RE2 syntax and mirrors backend byte limits.
 * Definite Go RE2 incompatibilities return `severity: "error"`; valid Go
 * constructs that JS cannot preview faithfully return `severity: "warning"`.
 */
export function validateGoRegex(
  raw: string,
  opts: {
    requireCapture?: boolean;
    /** Byte limit enforced by the backend (`512` for episode patterns, `4096` for filter regexes). */
    maxBytes?: number;
    /** Warn when leading/trailing spaces will be ignored by `ParseEpisodeExact` (true by default for episode patterns; false for filter regexes). */
    warnTrimmedWhitespace?: boolean;
  } = {},
): RegexDiagnostic | null {
  const warnTrimmed = opts.warnTrimmedWhitespace !== false;
  const target = warnTrimmed ? raw.trim() : raw;
  if (!target) return null;

  const maxBytes = opts.maxBytes ?? MAX_PATTERN_BYTES;
  const byteLen = new TextEncoder().encode(raw).length;
  if (byteLen > maxBytes) {
    return { severity: "error", message: t("forms.rules.patternEditor.tooLong", { max: maxBytes, current: byteLen }) };
  }

  const structural = stripEscapesAndClasses(expandGoQuotedLiterals(target));
  if (/\(\?(?:=|!|<=|<!)/.test(structural)) {
    return { severity: "error", message: t("forms.rules.patternEditor.re2Lookaround") };
  }
  if (/\\[1-9]/.test(structural)) {
    return { severity: "error", message: t("forms.rules.patternEditor.re2Backref") };
  }

  const previewUnsupported = hasPreviewUnsupportedGoSyntax(target);
  try {
    if (previewUnsupported) {
      compileGoRegexSyntaxCheck(target);
    } else {
      compileGoRegexPreview(target);
    }
  } catch {
    return { severity: "error", message: t("settings.rules.invalidRegex") };
  }

  if (previewUnsupported) {
    return { severity: "warning", message: t("forms.rules.patternEditor.re2PreviewLimited") };
  }

  if (warnTrimmed && raw !== target) {
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
      const re = compileGoRegexPreview(raw.trim(), { extraFlags: "d" });
      const m = re.exec(title);
      if (!m) continue;
      let groupIndex = -1;
      for (let g = 1; g < m.length; g++) {
        if (m[g] !== undefined && m[g].trim() !== "") {
          groupIndex = g;
          break;
        }
      }
      if (groupIndex < 0) continue;
      const group = m[groupIndex];
      const normalized = normalizeEpisodeCapture(group);
      if (!normalized) continue;

      let captureStart: number;
      let captureEnd: number;
      const groupSpan = m.indices?.[groupIndex];
      if (groupSpan) {
        const innerOffset = group.indexOf(normalized.raw);
        captureStart = innerOffset >= 0 ? groupSpan[0] + innerOffset : groupSpan[0];
        captureEnd = innerOffset >= 0 ? captureStart + normalized.raw.length : groupSpan[1];
      } else {
        const relCap = m[0].indexOf(normalized.raw);
        captureStart = relCap >= 0 ? m.index + relCap : m.index;
        captureEnd = relCap >= 0 ? captureStart + normalized.raw.length : m.index + m[0].length;
      }

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
