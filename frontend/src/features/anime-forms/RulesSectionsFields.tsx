import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { getNamingTemplateLibrary } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import type {
  FileEffectRules,
  FileFilterRules,
  NamingRules,
  ParserRules,
  ReleaseFilterRules,
  RulesSections,
} from "../../lib/api/client";
import { Chip, Field, Input, TagInput } from "../../ui";
import { HelpTip, Select, type SelectOption } from "../../ui/base";
import { NamingTemplateFields } from "../naming/NamingTemplateFields";
import { t } from "../../lib/i18n";
import { joinSentences } from "../../lib/i18n/format";
import { parserKindUsesPatterns } from "../../lib/parserKind";

// The editors of the rules sections. Every section is a complete value owned by this draft or
// this anime itself (one-time input: the default settings are copied at draft creation and the
// section is independent of them afterwards), edited in place. List fields always use TagInput;
// scalar fields use single-line inputs.

// tag_filters is an optional section edited by TagFilterCard (passed in
// through `afterReleaseFilter`, draft and owner); this editor covers the five
// regex/naming/effect sections.
export type SectionKey = Exclude<keyof RulesSections, "tag_filters">;

const fileOperationOptions = (): SelectOption<FileEffectRules["operation"]>[] => [
  { value: "hardlink", label: t("file.operation.hardlink") },
  { value: "copy", label: t("file.operation.copy") },
  { value: "move", label: t("file.operation.move") },
];

const collisionPolicyOptions = (): SelectOption<FileEffectRules["collision_policy"]>[] => [
  { value: "versioned", label: t("file.collision.versioned") },
  { value: "fail", label: t("file.collision.fail") },
  { value: "overwrite", label: t("file.collision.overwrite") },
];

export type SectionMeta = { title: string; hint: string; help?: { label: string; body: string } };

const sectionTitles = (): Record<SectionKey, SectionMeta> => ({
  parser: {
    title: t("forms.rules.parserTitle"),
    hint: t("forms.rules.parserHint"),
    help: { label: t("settings.rules.episodePatternsHelpLabel"), body: t("settings.rules.episodePatternsHelp") },
  },
  release_filter: {
    title: t("anime.rules.titleFilter"),
    hint: t("forms.rules.titleFilterHint"),
    help: { label: t("settings.rules.regexHelpLabel"), body: t("settings.rules.regexHelp") },
  },
  file_filter: {
    title: t("settings.rules.fileRules"),
    hint: t("forms.rules.fileFilterHint"),
    help: { label: t("forms.rules.fileFilterHelpLabel"), body: joinSentences(t("settings.rules.regexHelp"), t("settings.rules.fileRegexHelp"), t("forms.rules.fileFilterHelp")) },
  },
  naming: { title: t("forms.rules.namingTitle"), hint: t("forms.rules.namingHint") },
  file_effect: { title: t("forms.rules.effectTitle"), hint: t("forms.rules.effectHint") },
});

const defaultSectionValues: {
  parser: ParserRules;
  release_filter: ReleaseFilterRules;
  file_filter: FileFilterRules;
  naming: NamingRules;
  file_effect: FileEffectRules;
} = {
  parser: { episode_patterns: [] },
  release_filter: { must_contain: "", not_in: "" },
  file_filter: { must_contain: "", not_in: "" },
  naming: {
    language: "js-naming-1",
    unknown: { directory_template: "", video_filename_template: "" },
    tv: { directory_template: "", video_filename_template: "" },
    movie: { directory_template: "", video_filename_template: "" },
    subtitle_suffix_template: "",
  },
  file_effect: { operation: "hardlink", collision_policy: "versioned" },
};

function validRegex(tag: string): string | null {
  try {
    new RegExp(tag);
    return null;
  } catch {
    return t("settings.rules.invalidRegex");
  }
}

export function RulesSectionsFields({
  sections,
  onChange,
  only,
  parserChoice,
  parserKind,
  afterReleaseFilter,
  afterFileEffect,
  modified,
}: {
  sections: RulesSections;
  onChange: (next: RulesSections) => void;
  /**
   * 只渲染列出的段。缺少段时其余段既不渲染也不改动 `sections`：调用方仍然
   * 提交完整五段，隐藏段保持它原样的值。一次性下载只暴露影响「这个文件会落成
   * 什么样」的段（命名、文件过滤，必要时解析器），标题过滤与入库方式不改变渲染
   * 结果，因此不在那里出现。
   */
  only?: readonly SectionKey[];
  /** The "episode parser" choice above the episode recognition rules inside the parsing section (the subscribed instance's policy card, saved separately). */
  parserChoice?: ReactNode;
  /**
   * 显示中的解析器。集数识别规则只对 regex 生效：其他解析器时隐藏规则输入框，
   * 但不改动已保存的规则。未知（undefined）按 regex 显示。
   */
  parserKind?: string;
  /** Extra cards after the title-filter card (tag filtering). */
  afterReleaseFilter?: ReactNode;
  /** Extra cards after the import settings (the subscribed "after download completes" card). */
  afterFileEffect?: ReactNode;
  /** Sections differing from the current default settings: the section card's title shows "modified". Unmarked when absent. */
  modified?: ReadonlySet<keyof RulesSections>;
}) {
  function sectionProps<K extends SectionKey>(section: K) {
    // A draft that has not loaded the default settings lacks this section: it displays as empty and the first input becomes the complete value.
    const current = sections[section];
    const value = current?.mode === "replace" ? current.value : defaultSectionValues[section];
    return {
      value: value as never,
      setValue(next: unknown) {
        onChange({ ...sections, [section]: { mode: "replace", value: next } });
      },
    };
  }

  const parser = sectionProps("parser");
  const releaseFilter = sectionProps("release_filter");
  const fileFilter = sectionProps("file_filter");
  const naming = sectionProps("naming");
  const fileEffect = sectionProps("file_effect");
  const shown = (section: SectionKey) => only === undefined || only.includes(section);
  const usesPatterns = parserKindUsesPatterns(parserKind);

  return (
    <div className="rules-stack">
      {shown("parser") && (
        <SectionCard meta={sectionTitles().parser} modified={modified?.has("parser")}>
          <div className="tw:grid tw:min-w-0 tw:gap-3">
            {parserChoice}
            {usesPatterns ? (
              <div className="rules-responsive-wide">
                <TagRow
                  label={t("settings.rules.episodePatterns")}
                  value={(parser.value as ParserRules).episode_patterns}
                  validate={validRegex}
                  placeholder={t("forms.rules.episodePatternsPlaceholder")}
                  onChange={(episode_patterns) => parser.setValue({ ...(parser.value as ParserRules), episode_patterns })}
                />
              </div>
            ) : (
              <p className="naming-muted text-caption">{t("forms.rules.episodePatternsUnused")}</p>
            )}
          </div>
        </SectionCard>
      )}

      {shown("release_filter") && (
        <SectionCard meta={sectionTitles().release_filter} modified={modified?.has("release_filter")}>
          <RegexFields mustLabel={t("settings.rules.titleMust")} notLabel={t("settings.rules.titleNot")} value={releaseFilter.value as ReleaseFilterRules} onChange={releaseFilter.setValue} />
        </SectionCard>
      )}
      {shown("release_filter") && afterReleaseFilter}
      {shown("file_filter") && (
        <SectionCard meta={sectionTitles().file_filter} modified={modified?.has("file_filter")}>
          <RegexFields mustLabel={t("settings.rules.fileMust")} notLabel={t("settings.rules.fileNot")} value={fileFilter.value as FileFilterRules} onChange={fileFilter.setValue} />
        </SectionCard>
      )}

      {shown("naming") && (
        <SectionCard meta={sectionTitles().naming} modified={modified?.has("naming")}>
          <AnimeNamingFields value={naming.value as NamingRules} onChange={(next) => naming.setValue(next)} />
        </SectionCard>
      )}

      {shown("file_effect") && (
        <SectionCard meta={sectionTitles().file_effect} modified={modified?.has("file_effect")}>
          <div className="rules-responsive">
            <Field label={t("file.operationTitle")}>
              <Select<FileEffectRules["operation"]>
                label={t("file.operationTitle")}
                value={(fileEffect.value as FileEffectRules).operation}
                options={fileOperationOptions()}
                onChange={(operation) => fileEffect.setValue({ ...(fileEffect.value as FileEffectRules), operation })}
              />
            </Field>
            <Field label={t("file.collisionTitle")} hint={(fileEffect.value as FileEffectRules).collision_policy === "overwrite" ? t("file.collisionOverwriteNote") : undefined}>
              <Select<FileEffectRules["collision_policy"]>
                label={t("file.collisionTitle")}
                value={(fileEffect.value as FileEffectRules).collision_policy}
                options={collisionPolicyOptions()}
                onChange={(collision_policy) => fileEffect.setValue({ ...(fileEffect.value as FileEffectRules), collision_policy })}
              />
            </Field>
          </div>
        </SectionCard>
      )}
      {shown("file_effect") && afterFileEffect}
    </div>
  );
}

// The compact section card: title + one-line hint + content. `modified`: this section differs
// from the current default settings, shown as "modified" beside the title (a marking chip, Chip
// neutral).
export function SectionCard({ meta, modified = false, children }: { meta: SectionMeta; modified?: boolean; children: React.ReactNode }) {
  return (
    <section className="rules-card">
      <div className="rules-card-header">
        <div className="rules-mode-header">
          <span className="rules-card-title text-body">{meta.title}</span>
          {modified && <Chip title={t("config.modifiedTitle")}>{t("config.modified")}</Chip>}
          <span className="rules-card-hint text-caption">
            {meta.hint}
            {meta.help && (
              <HelpTip label={meta.help.label} className="tw:ml-1 tw:align-middle">
                {meta.help.body}
              </HelpTip>
            )}
          </span>
        </div>
      </div>
      <div className="rules-card-body">{children}</div>
    </section>
  );
}

function RegexFields({ value, onChange, mustLabel, notLabel }: { value: ReleaseFilterRules; onChange: (next: ReleaseFilterRules) => void; mustLabel: string; notLabel: string }) {
  return <div className="rules-responsive">
    <Field label={mustLabel}><Input value={value.must_contain} onChange={(e) => onChange({ ...value, must_contain: e.target.value })} /></Field>
    <Field label={notLabel}><Input value={value.not_in} onChange={(e) => onChange({ ...value, not_in: e.target.value })} /></Field>
  </div>;
}

function TagRow({
  label,
  value,
  onChange,
  validate,
  placeholder,
}: {
  label: string;
  value: string[];
  onChange?: (next: string[]) => void;
  validate?: (tag: string) => string | null;
  placeholder?: string;
}) {
  return (
    <Field label={label}>
      <TagInput
        ariaLabel={label}
        value={value}
        onChange={onChange}
        validateItem={validate}
        placeholder={placeholder ?? t("forms.rules.tagPlaceholder")}
      />
    </Field>
  );
}

// Per-anime naming: "choose template" copies a global library template's content into this anime's field; there is no sample selection.
function AnimeNamingFields({ value, onChange }: { value: NamingRules; onChange: (next: NamingRules) => void }) {
  const library = useQuery({
    queryKey: configKeys.namingTemplates,
    queryFn: getNamingTemplateLibrary,
  });
  return <NamingTemplateFields value={value} onChange={onChange} templates={library.data?.templates} />;
}
