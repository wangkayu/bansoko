import { useMemo, type ReactNode } from "react";
import type { TagFilterRow, TagFilterRules, TitleTagVocabulary } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { TagRowEditor } from "./TagRowEditor";
import { ANIBT_ROW_VOCABULARY, titleRowVocabulary } from "../../lib/tagRowVocabulary";
import { SectionCard, type SectionMeta } from "./RulesSectionsFields";

// The anime rules page's "tag filter" card (preset rules, shown before and after subscribing).
// Two independent rule rows are in effect together:
// - AniBT tags: shown only when the anime (or draft) has an AniBT source, acting only on AniBT's
//   seeds;
// - title parsing: acts on all seeds, its vocabulary coming from the server's reltitle
//   vocabulary.
// The value is a complete copy owned by this draft or this anime (the default settings are
// copied at draft creation), edited in place.

const tagFilterMeta = (): SectionMeta => ({
  title: t("tagFilters.title"),
  hint: t("tagFilters.hint"),
  help: { label: t("tagFilters.helpLabel"), body: t("tagFilters.help") },
});

export type TagRowObservations = {
  /** Observed tags ("kind:value"). */
  tags: readonly string[];
  /** How often each tag occurs (by comparison key). */
  counts?: ReadonlyMap<string, number>;
};

export function TagFilterCard({
  value,
  showAniBT,
  anibt,
  title,
  titleVocabulary,
  titleVocabularyFailed = false,
  modified = false,
  onChange,
}: {
  /** The current tag filter; while absent (the draft has not loaded the default settings yet) it displays as loading. */
  value: TagFilterRules | undefined;
  showAniBT: boolean;
  anibt?: TagRowObservations;
  title?: TagRowObservations;
  titleVocabulary: TitleTagVocabulary | undefined;
  titleVocabularyFailed?: boolean;
  /** Differs from the current default settings: "modified" shows beside the title. */
  modified?: boolean;
  onChange: (next: TagFilterRules) => void;
}) {
  const titleVocab = useMemo(() => titleRowVocabulary(titleVocabulary), [titleVocabulary]);

  function update(row: "anibt" | "title", next: TagFilterRow) {
    if (!value) return;
    onChange({ ...value, [row]: next });
  }

  return (
    <SectionCard meta={tagFilterMeta()} modified={modified}>
      {value ? (
        <div className="tw:grid tw:gap-4">
          {showAniBT && (
            <TagRowBlock heading={t("anibtTags.title")}>
              <TagRowEditor
                vocabulary={ANIBT_ROW_VOCABULARY}
                value={value.anibt}
                extraTags={anibt?.tags}
                counts={anibt?.counts}
                onChange={(next) => update("anibt", next)}
              />
            </TagRowBlock>
          )}
          <TagRowBlock heading={t("titleTags.title")} hint={t("titleTags.hint")}>
            {titleVocabularyFailed || (titleVocabulary !== undefined && !Array.isArray(titleVocabulary.dimensions)) ? (
              <p className="tw:text-xs tw:text-destructive">{t("titleTags.unavailable")}</p>
            ) : Array.isArray(titleVocabulary?.dimensions) ? (
              <TagRowEditor
                vocabulary={titleVocab}
                value={value.title}
                extraTags={title?.tags}
                counts={title?.counts}
                onChange={(next) => update("title", next)}
              />
            ) : (
              <p className="tw:text-xs tw:text-muted-foreground">{t("settings.rules.loading")}</p>
            )}
          </TagRowBlock>
        </div>
      ) : (
        <p className="tw:text-xs tw:text-muted-foreground">{t("settings.rules.loading")}</p>
      )}
    </SectionCard>
  );
}

export function TagRowBlock({ heading, hint, children }: { heading: string; hint?: string; children: ReactNode }) {
  return (
    <section className="tw:grid tw:gap-2" aria-label={heading}>
      <div className="tw:flex tw:flex-wrap tw:items-baseline tw:gap-x-2">
        <h4 className="tw:text-sm tw:font-medium">{heading}</h4>
        {hint && <span className="tw:text-xs tw:text-muted-foreground">{hint}</span>}
      </div>
      {children}
    </section>
  );
}
