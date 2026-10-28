import { getLocale, t, tryT } from "./index";
import { factsCopy } from "./facts.generated";
import { joinList } from "./format";

/** Normalizes a backend reason code into a copy-key suffix: lowercased, all other characters unchanged. */
function normalizeReasonCode(code: string): string {
  return code.trim().toLowerCase();
}

const NAMING_FACT_MISSING_PREFIX = "naming_fact_missing_";
const NAMING_FACT_UNDEFINED_PREFIX = "naming_fact_undefined_";

/** The name of the missing fact in naming_fact_missing_<fact>; other codes return undefined. */
export function namingFactMissingName(code: string | null | undefined): string | undefined {
  if (!code) return undefined;
  const normalized = normalizeReasonCode(code);
  if (!normalized.startsWith(NAMING_FACT_MISSING_PREFIX)) return undefined;
  const fact = normalized.slice(NAMING_FACT_MISSING_PREFIX.length);
  return tryT(`namingFact.${fact}`) ?? factLabelForSlug(fact) ?? tryT("namingFact.unknown");
}

let factPathsBySlug: Map<string, string> | undefined;

/** The naming template's missing-code fact path has its dot restored to an underscore (ep_display -> ep.display). */
function factLabelForSlug(slug: string): string | undefined {
  const path = factPathForSlug(slug);
  return path === undefined ? undefined : tryT(`facts.${path}.label`);
}

function factPathForSlug(slug: string): string | undefined {
  if (!factPathsBySlug) {
    factPathsBySlug = new Map();
    const walk = (node: object, prefix: string) => {
      for (const [key, value] of Object.entries(node)) {
        if (key === "label" && typeof value === "string") factPathsBySlug?.set(prefix.replaceAll(".", "_"), prefix);
        else if (value && typeof value === "object") walk(value, prefix ? `${prefix}.${key}` : key);
      }
    };
    walk(factsCopy, "");
  }
  return factPathsBySlug.get(slug);
}

/**
 * The variable in naming_fact_undefined_<var>: prefer the paths the server lists (empty_facts),
 *   otherwise restore the registry path from the reason code; when neither exists return
 *   undefined, and non-membership codes return null. Variables display as they are spelled in the
 *   template (anime.year) — the name the user wrote into the template themselves.
 */
function undefinedVariables(code: string | null | undefined, paths?: readonly string[] | null): string | null | undefined {
  if (!code) return null;
  const normalized = normalizeReasonCode(code);
  if (!normalized.startsWith(NAMING_FACT_UNDEFINED_PREFIX)) return null;
  if (paths && paths.length > 0) return joinList([...new Set(paths)]);
  return factPathForSlug(normalized.slice(NAMING_FACT_UNDEFINED_PREFIX.length));
}

/** The display name of a naming fact path (anime.year); unregistered paths use the generic name and the raw path is never shown. */
export function namingFactLabel(path: string): string {
  const slug = normalizeReasonCode(path).replaceAll(".", "_");
  return tryT(`namingFact.${slug}`) ?? factLabelForSlug(slug) ?? t("namingFact.unknown");
}

/** A short note about naming facts that rendered empty ("empty values: year, season directory"); undefined when there is none. */
export function emptyFactsNote(facts: readonly string[] | null | undefined): string | undefined {
  if (!facts || facts.length === 0) return undefined;
  const labels = [...new Set(facts.map(namingFactLabel))];
  return t("naming.emptyFacts", { facts: joinList(labels) });
}

/**
 * A short note for when naming is rejected over information, for use with "naming failed:
 *   {message}": an undefined variable ("undefined variable: bgm.id") or an empty value making the
 *   path invalid ("year, season directory empty; path invalid"). Other reason codes return
 *   undefined and the caller shows the server's original text.
 */
export function namingFailureDetail(code: string | null | undefined, emptyFacts?: readonly string[] | null): string | undefined {
  const variables = undefinedVariables(code, emptyFacts);
  if (variables !== null) return variables === undefined ? t("rss.nameFactUndefinedUnknown") : t("rss.nameFactUndefined", { vars: variables });
  const fact = namingFactMissingName(code);
  if (fact === undefined) return undefined;
  // When the server supplies all the empty values, list them one by one ("year, season directory"); otherwise only the first item inside the reason code.
  const facts = emptyFacts && emptyFacts.length > 0 ? joinList([...new Set(emptyFacts.map(namingFactLabel))]) : fact;
  return t("rss.nameFactMissing", { fact: spacedAfterCjk(facts) });
}

/** Appends one space when a label ending in a Latin letter or digit precedes CJK text ("TMDB score empty" needs it, "Season empty" does not); English templates carry their own spaces (used after joining a CJK-locale fact list). */
function spacedAfterCjk(label: string): string {
  return getLocale() === "zh-CN" && /[A-Za-z0-9]$/.test(label) ? `${label} ` : label;
}

const CONFIG_INCOMPLETE_PREFIX = "config_incomplete:";
const TEMPLATE_VARIABLE_UNDEFINED_ITEM = "template_variable_undefined:";

/** The copy of config_incomplete:<item> (the current configuration lacks an input a path needs); other codes return undefined. */
function configIncompleteLabel(code: string): string | undefined {
  const normalized = normalizeReasonCode(code);
  if (!normalized.startsWith(CONFIG_INCOMPLETE_PREFIX)) return undefined;
  const item = normalized.slice(CONFIG_INCOMPLETE_PREFIX.length);
  if (item.startsWith(TEMPLATE_VARIABLE_UNDEFINED_ITEM)) {
    const names = item.slice(TEMPLATE_VARIABLE_UNDEFINED_ITEM.length).split(",").filter(Boolean);
    if (names.length === 0) return undefined;
    return tryT("reason.config_incomplete_template_variable_undefined", { vars: joinList([...new Set(names)]) });
  }
  return tryT(`reason.config_incomplete_${item.replaceAll(":", "_")}`);
}

/** A registered reason code returns its copy; unregistered (or empty) returns undefined. */
export function knownReasonLabel(code: string | null | undefined): string | undefined {
  if (!code) return undefined;
  if (normalizeReasonCode(code).startsWith(CONFIG_INCOMPLETE_PREFIX)) return configIncompleteLabel(code);
  const variables = undefinedVariables(code);
  if (variables !== null) {
    return variables === undefined ? t("reason.naming_fact_undefined_unknown") : t("reason.naming_fact_undefined", { vars: variables });
  }
  const fact = namingFactMissingName(code);
  if (fact !== undefined) return tryT("reason.naming_fact_missing", { fact: spacedAfterCjk(fact) });
  return tryT(`reason.${normalizeReasonCode(code)}`);
}

export type ReasonView = {
  /** The display text: the registered copy, or the "unknown reason" when unregistered. */
  text: string;
  /** Present only when unregistered: the raw code, placed into the tooltip. */
  title?: string;
};

export function reasonView(code: string | null | undefined): ReasonView | undefined {
  if (!code) return undefined;
  const known = knownReasonLabel(code);
  if (known !== undefined) return { text: known };
  // The http_<status> codes in diagnostics are one whole class; registering each individually would be pointless.
  const http = /^http_([1-5]\d\d)$/.exec(normalizeReasonCode(code));
  if (http) return { text: tryT("reason.http_status", { status: http[1] }) ?? code, title: code };
  return { text: t("common.unknownReason"), title: code };
}

/** The plain-text form; used when the caller needs no tooltip. */
export function reasonLabel(code: string | null | undefined): string | undefined {
  return reasonView(code)?.text;
}

/**
 * For reason codes where the target simply cannot render yet without being a configuration error
 *   (naming variables not ready) return the copy; the rest return undefined and the caller shows
 *   the server's template-error original under the "naming failed" heading.
 */
export function namingPendingLabel(code: string | null | undefined): string | undefined {
  if (code && normalizeReasonCode(code) === "naming_vars_unavailable") return knownReasonLabel(code);
  return undefined;
}
