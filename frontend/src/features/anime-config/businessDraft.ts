import { getBusinessPreset, type AnimeDraft, type BusinessPresetView, type BusinessSnapshot, type RulesSections } from "../../lib/api/client";
import { canonicalJSON, valuesEqual } from "../../lib/draft";
import { sha256Hex } from "../../lib/sha256";
import { t } from "../../lib/i18n";

/** The five rules sections' draft keys and their business-snapshot section names. */
const RULE_SECTIONS = [
  ["parser", "parser"],
  ["release_filter", "release_rules"],
  ["file_filter", "file_rules"],
  ["naming", "naming_rules"],
  ["file_effect", "file_effect"],
] as const;

type RuleKey = (typeof RULE_SECTIONS)[number][0];
type SnapshotSection = (typeof RULE_SECTIONS)[number][1];

/**
 * Every newly created candidate starts from one complete Global business
 * snapshot. The server never fills a missing business in, and an instance
 * activated from a business-less revision has no frozen Vars program, so it
 * can never acquire a release.
 *
 * `preset` 允许调用方改走共享查询缓存（同一会话里新建草稿与一次性任务读到的是
 * 同一份 Global，不各自再领一次）；缺省仍是一次直接读取。
 */
export async function withGlobalBusiness(
  draft: AnimeDraft,
  preset: Promise<BusinessPresetView> = getBusinessPreset(),
): Promise<AnimeDraft> {
  return freezeBusinessDraft(loadBusinessPreset(draft, await preset));
}

/** A full copy of the global preset's rules sections and tag filter (one-time input: afterwards independent of Global). */
export function presetRuleSections(preset: BusinessPresetView): RulesSections {
  const sections = preset.snapshot.sections;
  return {
    parser: { mode: "replace", value: structuredClone(sections.parser) },
    release_filter: { mode: "replace", value: structuredClone(sections.release_rules) },
    file_filter: { mode: "replace", value: structuredClone(sections.file_rules) },
    naming: { mode: "replace", value: structuredClone(sections.naming_rules) },
    file_effect: { mode: "replace", value: structuredClone(sections.file_effect) },
    tag_filters: { mode: "replace", value: structuredClone(preset.tag_filters) },
  };
}

/**
 * Atomic load: business fields replaced, identity/source/metadata and manual intent retained.
 * 规则各段与标签过滤是全局预设的完整副本：每项设置都是一次性输入（owner 2026-10-20），
 * 之后修改默认设置不会改变这份草稿，也不会改变由它创建的订阅。
 */
export function loadBusinessPreset(draft: AnimeDraft, preset: BusinessPresetView): AnimeDraft {
  const next = structuredClone(draft);
  next.business = structuredClone(preset.snapshot);
  next.business_digest = preset.digest;
  const sections = next.business.sections;
  const manual = draft.subgroups.entries.filter((entry) => entry.selection_mode === "manual");
  next.subgroups = structuredClone(sections.subgroups);
  next.subgroups.entries = next.subgroups.entries.filter((entry) => !manual.some((intent) => intent.provider === entry.provider && intent.group_ref === entry.group_ref));
  next.subgroups.entries.push(...structuredClone(manual));
  next.rules.sections = presetRuleSections(preset);
  return next;
}

/**
 * 「全部使用默认」：规则各段与标签过滤重新复制当前默认设置，业务快照（解析器、未识别
 * 集数、保留与清理、程序引用）换成当前全局预设。字幕组、RSS 源、元数据与身份不动——
 * 它们不在规则页。
 */
export function resetToGlobal(draft: AnimeDraft, preset: BusinessPresetView): AnimeDraft {
  const next = structuredClone(draft);
  next.business = structuredClone(preset.snapshot);
  next.business.sections.subgroups = structuredClone(draft.subgroups);
  next.business_digest = preset.digest;
  next.rules.sections = presetRuleSections(preset);
  return next;
}

/** Whether the rules sections and tag filter differ from the copy of the default settings (used by the post-subscription rules page's "use defaults everywhere"). */
export function rulesDifferFromPreset(sections: RulesSections, preset: BusinessPresetView): boolean {
  return !valuesEqual(sections, presetRuleSections(preset));
}

/** The rules page's section cards (five sections + tag filter). */
export type RuleCardKey = keyof RulesSections;

/**
 * 规则页各段卡的「已修改」标记：与当前默认设置的副本逐段比较（同一个规范 JSON
 * 比较，与 hasCustomSettings / rulesDifferFromPreset 一致）。传入编辑中的值，未保存
 * 的修改也算。解析段同时比较显示中的解析器；未知时只比较集数识别规则。还没读到
 * 默认设置（读取中或失败）时不标记任何段；还没有值的段也不标记。
 */
export function modifiedRuleSections(
  sections: RulesSections,
  preset: BusinessPresetView | undefined,
  parserKind?: string,
): ReadonlySet<RuleCardKey> {
  const modified = new Set<RuleCardKey>();
  if (!preset) return modified;
  const defaults = presetRuleSections(preset);
  for (const key of Object.keys(defaults) as RuleCardKey[]) {
    if (sections[key] !== undefined && !valuesEqual(sections[key], defaults[key])) modified.add(key);
  }
  if (parserKind !== undefined && parserKind !== preset.snapshot.parser.kind) modified.add("parser");
  return modified;
}

/**
 * 草稿与当前默认设置是否不同：任一规则段或标签过滤的值不同、解析器是这份草稿自己换的，
 * 或未识别集数 / 保留与清理 / 解析器与默认不同。还没读到默认设置时只看解析器。
 * 「全部使用默认」在没有任何不同时不可用。
 */
export function hasCustomSettings(draft: AnimeDraft, preset?: BusinessPresetView): boolean {
  const business = draft.business;
  if (business && isDraftChosenParser(business.parser)) return true;
  if (!preset) return false;
  if (rulesDifferFromPreset(draft.rules.sections, preset)) return true;
  if (!business) return false;
  return business.parser.kind !== preset.snapshot.parser.kind
    || business.unclassified_route !== preset.snapshot.unclassified_route
    || !valuesEqual(business.replacement, preset.snapshot.replacement);
}

/**
 * Freeze explicit edits into loaded provenance; no network/current-Global lookup.
 * 规则五段的值冻进快照并重算摘要；缺段（还没载入默认设置）的草稿不能冻结。
 */
export async function freezeBusinessDraft(draft: AnimeDraft): Promise<AnimeDraft> {
  if (!draft.business) return draft;
  const next = structuredClone(draft);
  const business: BusinessSnapshot = structuredClone(draft.business);
  for (const [kind, ref] of Object.entries(business.programs)) {
    const bases = kind === "vars" ? ["naming-vars-v1"] : ["release-facts-v1"];
    if (!/^sha256:[a-f0-9]{64}$/.test(ref.digest) || !bases.some((base) => ref.version === `${base}@${ref.digest}`)) throw new Error(t("config.validation.configInvalid"));
  }
  business.sections.subgroups = structuredClone(draft.subgroups);
  const rules = draft.rules.sections;
  for (const [key, name] of RULE_SECTIONS) {
    const section = rules[key as RuleKey];
    if (section?.mode !== "replace") throw new Error(t("config.validation.configInvalid"));
    (business.sections as Record<SnapshotSection, unknown>)[name] = structuredClone(section.value);
  }
  next.business = business;
  next.business_digest = `sha256:${sha256Hex(new TextEncoder().encode(canonicalJSON(business)))}`;
  return next;
}

type ParserEnvelope = BusinessSnapshot["parser"];

/** The origin an envelope records when a single draft swapped its parser: a one-shot task, or the anime draft before subscribing. */
export type DraftParserOwner = "one_shot_draft" | "candidate_draft";

/** Whether the envelope is the parser this draft swapped in itself (not the loaded global default or preset). */
export function isDraftChosenParser(envelope: ParserEnvelope | undefined): boolean {
  return envelope?.source_owner === "one_shot_draft" || envelope?.source_owner === "candidate_draft";
}

/** The parser the draft froze (the business snapshot's parser envelope); empty without a business snapshot. */
export function draftParserKind(draft: AnimeDraft | undefined): string | undefined {
  return draft?.business?.parser.kind;
}

/**
 * 只为这一份草稿换解析器：解析器跟随业务快照冻结，预览按它解析集数，提交时随草稿
 * 一起重新冻结（freezeBusinessDraft 重算摘要）。选回载入时的解析器就原样恢复载入时
 * 的信封；换成别的则把来源标成这份草稿，不冒充全局默认。不写回任何订阅、预设或全局。
 */
export function withDraftParserKind(draft: AnimeDraft, kind: string, loaded: ParserEnvelope, owner: DraftParserOwner = "one_shot_draft"): AnimeDraft {
  if (!draft.business) return draft;
  const parser: ParserEnvelope =
    kind === loaded.kind ? structuredClone(loaded) : { ...structuredClone(loaded), kind, provenance: owner, source_owner: owner };
  return { ...draft, business: { ...draft.business, parser } };
}
