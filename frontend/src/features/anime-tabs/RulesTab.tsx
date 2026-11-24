import { Fragment, useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSavePart } from "../../lib/saveParts";
import {
  getBusinessPreset,
  getInstanceParserPolicy,
  getTitleTagVocabulary,
  isVersionConflict,
  patchAnimeRules,
  toUserMessage,
  type ParserPolicyView,
  type RulesSections,
} from "../../lib/api/client";
import { configKeys, fileMaintenanceKeys } from "../../lib/queryKeys";
import { observedExtraTags, tagRowSummary } from "../../lib/anibtTags";
import { countTags, titleTagLabel } from "../../lib/titleTags";
import { TagFilterCard } from "../anime-forms/TagFilterCard";
import { changeCount, sectionsFromRules, valuesEqual } from "../../lib/draft";
import { RulesSectionsFields } from "../anime-forms/RulesSectionsFields";
import { StateBlock, Button, ConflictBlock } from "../../ui";
import type { AnimeTabHost, DraftTabHost, OwnerTabHost } from "./host";
import { useOwnerEdit } from "./useOwnerEdit";
import { FileEffectPolicySection } from "./FileEffectPolicySection";
import { ReplacementPolicySection } from "./ReplacementPolicySection";
import { ParserPolicySection } from "./ParserPolicySection";
import { DraftParserChoice } from "./DraftParserChoice";
import { draftParserKind, modifiedRuleSections, presetRuleSections } from "../anime-config/businessDraft";
import { confirmBusinessReplace } from "../anime-config/confirmBusinessReplace";
import { toast } from "../../ui/toast";
import { t, tryT } from "../../lib/i18n";
// The rules tab's sole implementation: every section is a complete value owned by this draft or
// this anime, edited through the same RulesSectionsFields. Every setting is one-time input: the
// default settings are copied at draft creation and later changes to them never alter the draft
// or the subscription; "use defaults everywhere" copies them once more. Tag filtering is a preset
// rule, configurable before and after subscribing. Before subscribing it saves with the whole
// draft; after subscribing the rules sections, parser, unknown-episode handling and automatic
// alignment are this tab's draft, submitted one by one by the dialog footer's single save.
// "Preview rules" (a side-effect-free replay of collected releases; the RSS area switches to
// preview mode) is an action.

export function RulesTab({
  host,
  readOnly = false,
  onContentChange,
  onSaved,
  onPreviewChange,
  onParserPreviewChange,
  active = true,
}: {
  host: AnimeTabHost;
  /** A read-only workspace (a retired owner): every rules edit/save/preview entry is hidden. */
  readOnly?: boolean;
  onContentChange?: () => void;
  /** Notifies the outer shell after a successful rules save. */
  onSaved?: () => void;
  /** Owner mode: reports unsaved rules sections so switching to the RSS tab automatically previews them (undefined when clean). */
  onPreviewChange?: (sections: RulesSections | undefined) => void;
  /** Owner mode: reports an unsaved parser kind change so the RSS tab previews it without saving first. */
  onParserPreviewChange?: (kind: ParserPolicyView["parser_kind"] | undefined) => void;
  active?: boolean;
}) {
  if (host.mode === "draft") {
    return <DraftRulesTab host={host} />;
  }
  if (readOnly) {
    return <ReadOnlyRulesPanel host={host} active={active} />;
  }
  return (
    <div className="metadata-stack">
      <OwnerRulesTab
        host={host}
        active={active}
        onContentChange={onContentChange}
        onSaved={onSaved}
        onPreviewChange={onPreviewChange}
        onParserPreviewChange={onParserPreviewChange}
      />
      <FileEffectPolicySection animeID={host.animeID} onChanged={host.onChanged} active={active} />
      <ReplacementPolicySection animeID={host.animeID} onChanged={host.onChanged} active={active} />
      {host.rulesExtra}
    </div>
  );
}

// The pre-subscription rules tab: tag filtering, like release filtering, is a preset rule that
// can be configured before subscribing; it saves with the whole draft and becomes the new anime's
// settings at subscribe time. No seeds have been collected yet, so there are no observed tags;
// the AniBT row shows when the draft contains an AniBT source. A line at the top notes that the
// values come from the default settings, with the whole-page "use defaults everywhere" on the
// right.
function DraftRulesTab({ host }: { host: DraftTabHost }) {
  const { draft, onDraftChange } = host;
  const sections = draft.rules.sections;
  const titleVocabulary = useQuery({
    queryKey: configKeys.titleTagVocabulary,
    queryFn: getTitleTagVocabulary,
    staleTime: Infinity,
  });
  const showAniBTRow = draft.origin === "anibt" || draft.subgroups.entries.some((entry) => entry.provider === "anibt");
  const draftKind = draftParserKind(draft);
  const modified = useMemo(
    () => modifiedRuleSections(sections, host.businessPreset, draftKind, { includeAniBT: showAniBTRow }),
    [sections, host.businessPreset, draftKind, showAniBTRow],
  );
  return (
    <div className="metadata-stack">
      {host.rulesHeaderAction && (
        <div className="tw:flex tw:items-center tw:justify-between tw:gap-3">
          <p className="naming-muted text-caption tw:min-w-0">{t("config.rulesHint")}</p>
          {host.rulesHeaderAction}
        </div>
      )}
      <RulesSectionsFields
        sections={sections}
        onChange={(next) => onDraftChange({ ...draft, rules: { sections: next } })}
        // The parser and the episode recognition rules are one block: picking reltitle hides the recognition rules.
        parserChoice={<DraftParserChoice draft={draft} onDraftChange={onDraftChange} />}
        parserKind={draftKind}
        modified={modified}
        afterReleaseFilter={(
          <TagFilterCard
            value={sections.tag_filters?.value}
            modified={modified.has("tag_filters")}
            showAniBT={showAniBTRow}
            titleVocabulary={titleVocabulary.data}
            titleVocabularyFailed={titleVocabulary.isError}
            onChange={(value) => onDraftChange({ ...draft, rules: { sections: { ...sections, tag_filters: { mode: "replace", value } } } })}
          />
        )}
      />
      {host.rulesExtra}
    </div>
  );
}

// The read-only rules panel: a retired instance's rules sections are no longer editable (the
// backend would 409 anyway), but each section renders a concise summary of its historical values
// so users can inspect what rules an ended anime used.
function formatReadOnlySection(
  section: string,
  rules: OwnerTabHost["workspace"]["rules"],
): string | undefined {
  const effective = rules.effective;
  if (section === "release_filter" || section === "release_rules") {
    const { must_contain, not_in } = effective.release_filter;
    const parts = [
      must_contain ? `${t("settings.rules.titleMust")}: ${must_contain}` : "",
      not_in ? `${t("settings.rules.titleNot")}: ${not_in}` : "",
    ].filter(Boolean);
    return parts.length > 0 ? parts.join(" · ") : t("tabs.rules.readOnlyEmpty");
  }
  if (section === "tag_filters") {
    const tagRules = rules.effective_tag_filters;
    if (!tagRules) return t("tabs.rules.readOnlyEmpty");
    const hasAniBT = rules.has_anibt_source || tagRules.anibt.require.length > 0 || tagRules.anibt.exclude.length > 0;
    const parts = [
      hasAniBT ? `${t("anibtTags.title")}: ${tagRowSummary(tagRules.anibt)}` : "",
      `${t("titleTags.title")}: ${tagRowSummary(tagRules.title, titleTagLabel)}`,
    ].filter(Boolean);
    return parts.join(" · ");
  }
  if (section === "file_filter" || section === "file_rules") {
    const { must_contain, not_in } = effective.file_filter;
    const parts = [
      must_contain ? `${t("settings.rules.fileMust")}: ${must_contain}` : "",
      not_in ? `${t("settings.rules.fileNot")}: ${not_in}` : "",
    ].filter(Boolean);
    return parts.length > 0 ? parts.join(" · ") : t("tabs.rules.readOnlyEmpty");
  }
  if (section === "file_effect") {
    const { operation, collision_policy } = effective.file_effect;
    return `${t(`file.operation.${operation}`)} · ${t(`file.collision.${collision_policy}`)}`;
  }
  if (section === "parser") {
    const patterns = effective.parser.episode_patterns;
    return patterns.length > 0 ? patterns.join(" / ") : undefined;
  }
  if (section === "naming" || section === "naming_rules") {
    const tvDir = effective.naming.tv.directory_template;
    const tvFile = effective.naming.tv.video_filename_template;
    return [tvDir, tvFile].filter(Boolean).join(" / ") || t("tabs.rules.readOnlyEmpty");
  }
  return undefined;
}

function ReadOnlyRulesPanel({ host, active }: { host: OwnerTabHost; active: boolean }) {
  return (
    <div className="metadata-stack">
      <StateBlock
        kind="empty"
        title={t("tabs.rulesReadOnly")}
        description={t("anime.rules.endedHint")}
      />
      <div role="group" className="sources-list" aria-label={t("anime.rules.endedAria")}>
        {Object.keys(host.workspace.rules.sections).map((section) => {
          const summary = formatReadOnlySection(section, host.workspace.rules);
          return (
            <Fragment key={section}>
              <div className="sources-row tw:flex-wrap tw:gap-y-1">
                <span className="sources-name-text text-body">{tryT(`tabs.sectionName.${section}`) ?? t("maint.unknown")}</span>
                {summary && <span className="tw:min-w-0 tw:text-xs tw:text-muted-foreground tw:wrap-anywhere">{summary}</span>}
              </div>
              {/* The parser card follows the "parsing" row directly, matching the editable page's placement. */}
              {section === "parser" && (
                <ParserPolicySection
                  animeID={host.animeID}
                  initialPolicy={host.workspace.parser_policy}
                  readOnly
                  active={active}
                />
              )}
            </Fragment>
          );
        })}
      </div>
      <FileEffectPolicySection animeID={host.animeID} onChanged={host.onChanged} readOnly active={active} />
      <ReplacementPolicySection animeID={host.animeID} readOnly active={active} />
    </div>
  );
}

function OwnerRulesTab({
  host,
  active = true,
  onContentChange,
  onSaved,
  onPreviewChange,
  onParserPreviewChange,
}: {
  host: OwnerTabHost;
  active?: boolean;
  onContentChange?: () => void;
  onSaved?: () => void;
  onPreviewChange?: (sections: RulesSections | undefined) => void;
  onParserPreviewChange?: (kind: ParserPolicyView["parser_kind"] | undefined) => void;
}) {
  const { animeID, workspace, onChanged } = host;
  const saved = useMemo(() => sectionsFromRules(workspace.rules), [workspace.rules]);
  const { edit, setEdit, baseline, markSaved } = useOwnerEdit<RulesSections>(saved, valuesEqual);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  // The parser card's displayed value (the unsaved choice winning): decides whether the parsing section shows the episode recognition rules.
  const [parserKind, setParserKind] = useState<string>();
  const [parserDirty, setParserDirty] = useState(false);
  // The default parser filled into the parser card by "use defaults everywhere" (the unsaved choice, submitted with the same save).
  const [parserReset, setParserReset] = useState<{ kind: string; seq: number }>();
  // Between a successful save and the re-read, the CAS version follows the server's response.
  const [localVersion, setLocalVersion] = useState<{ base: number; next: number }>();
  const rulesVersion =
    localVersion && localVersion.base === workspace.rules.version ? localVersion.next : workspace.rules.version;
  const count = edit && baseline ? changeCount(baseline, edit) : 0;
  // Unsaved rules feed directly into the RSS tab's automatic owner_draft preview (matching metadata preview).
  const previewKey = count > 0 && edit ? JSON.stringify(edit) : "";
  useEffect(() => {
    onPreviewChange?.(previewKey ? (JSON.parse(previewKey) as RulesSections) : undefined);
  }, [previewKey, onPreviewChange]);
  useEffect(() => () => onPreviewChange?.(undefined), [onPreviewChange]);

  // Report unsaved parser kind changes into the RSS tab's owner_draft preview as well.
  const previewParserKind =
    parserDirty && (parserKind === "regex" || parserKind === "reltitle") ? parserKind : undefined;
  useEffect(() => {
    onParserPreviewChange?.(previewParserKind);
  }, [previewParserKind, onParserPreviewChange]);
  useEffect(() => () => onParserPreviewChange?.(undefined), [onParserPreviewChange]);

  // The tag filter card: the title-parsing row acts on all seeds and always shows; the AniBT row
  // shows only when the anime has an AniBT source.
  const showAniBTRow = workspace.rules.has_anibt_source;
  // "Use defaults everywhere": fills the rules sections, tag filter and parser with a copy of the current default settings, saved by the user.
  const queryClient = useQueryClient();
  // Seed from workspace.parser_policy when present (P2): zero waterfall and zero first-frame jump.
  const workspaceParserPolicy = workspace.parser_policy ?? undefined;
  const parserPolicy = useQuery({
    queryKey: fileMaintenanceKeys.parserPolicy(animeID),
    queryFn: () => getInstanceParserPolicy(animeID),
    initialData: workspaceParserPolicy,
    enabled: active && workspaceParserPolicy === undefined,
  });
  const businessPreset = useQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset, enabled: active });
  const [resetting, setResetting] = useState(false);
  const titleVocabulary = useQuery({
    queryKey: configKeys.titleTagVocabulary,
    queryFn: getTitleTagVocabulary,
    staleTime: Infinity,
  });
  const tagSource = "release_summary" in workspace ? workspace.release_summary.tag_observations : workspace.releases;
  const anibtObservations = useMemo(() => {
    const lists = tagSource.map(item => "anibt_tags" in item ? item.anibt_tags : item.source_facts.anibt?.tags);
    return { tags: observedExtraTags(lists), counts: countTags(lists) };
  }, [tagSource]);
  const titleObservations = useMemo(() => {
    const lists = tagSource.map(item => "title_tags" in item ? item.title_tags : item.source_facts.title_tags?.tags);
    return { tags: lists.flatMap((tags) => tags ?? []), counts: countTags(lists) };
  }, [tagSource]);
  const sampleTitles = useMemo(
    () => ("releases" in workspace ? workspace.releases.map((release) => release.raw_facts.title) : undefined),
    [workspace],
  );

  const latestParserPolicy =
    parserPolicy.data && workspaceParserPolicy
      ? workspaceParserPolicy.version > parserPolicy.data.version
        ? workspaceParserPolicy
        : parserPolicy.data
      : (parserPolicy.data ?? workspaceParserPolicy);
  const effectiveParserKind = parserKind ?? latestParserPolicy?.parser_kind;
  const parserKindLoading = effectiveParserKind === undefined && parserPolicy.isPending;

  // Single memoized pass computes both per-card "modified" badges and "Use defaults everywhere" state.
  const modified = useMemo(
    () => (edit ? modifiedRuleSections(edit, businessPreset.data, effectiveParserKind, { includeAniBT: showAniBTRow }) : new Set<keyof RulesSections>()),
    [edit, businessPreset.data, effectiveParserKind, showAniBTRow],
  );
  const matchesDefaults = businessPreset.data !== undefined && modified.size === 0;

  async function save() {
    if (!edit) return { ok: true as const };
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const result = await patchAnimeRules(animeID, {
        expected_version: rulesVersion,
        sections: edit,
      });
      setLocalVersion({ base: workspace.rules.version, next: result.rules.version });
      markSaved(edit);
      onSaved?.();
      onChanged();
      return { ok: true as const, message: t("tabs.rules.saved", { n: result.reevaluation.scheduled_count }) };
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("tabs.rules.saveFailed")));
      return { ok: false as const };
    } finally {
      setBusy(false);
    }
  }

  useSavePart("rules", { count, label: t("tabs.rules.partName"), busy, save });

  async function fillDefaults() {
    if ((count > 0 || parserDirty) && !await confirmBusinessReplace(t("tabs.rules.useDefaultsBody"), {
      title: t("config.useDefaultsTitle"),
      confirmLabel: t("config.useDefaultsConfirm"),
    })) return;
    setResetting(true);
    setError(undefined);
    try {
      const preset = await queryClient.fetchQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset, staleTime: 0 });
      onContentChange?.();
      setEdit(presetRuleSections(preset));
      setParserReset((prev) => ({ kind: preset.snapshot.parser.kind, seq: (prev?.seq ?? 0) + 1 }));
      toast.success(t("config.useDefaultsDone"));
    } catch (cause) {
      setError(toUserMessage(cause, t("tabs.rules.useDefaultsFailed")));
    } finally {
      setResetting(false);
    }
  }

  // A 409 refresh: re-read the workspace (along onChanged's refetch path), keeping local edits.
  function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    onChanged();
  }

  if (!edit) return <StateBlock kind="busy" skeleton="settings" title={t("tabs.rules.reading")} />;
  return (
    <div className="metadata-stack">
      <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-2">
        <p className="naming-muted text-caption tw:min-w-0 tw:flex-1">{t("tabs.rules.ownerHint")}</p>
        <div className="tw:flex tw:flex-none tw:flex-wrap tw:items-center tw:gap-2">
          <Button
            variant="ghost"
            size="sm"
            loading={resetting}
            disabled={busy || matchesDefaults}
            title={matchesDefaults ? t("config.useDefaultsNone") : undefined}
            onClick={() => void fillDefaults()}
          >
            {t("config.useDefaults")}
          </Button>
        </div>
      </div>
      {conflict !== undefined && (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
      )}
      {conflict === undefined && error && <StateBlock kind="error" title={error} />}
      <RulesSectionsFields
        sections={edit}
        onChange={(next) => {
          onContentChange?.();
          setEdit(next);
        }}
        parserChoice={(
          <ParserPolicySection
            animeID={animeID}
            active={active}
            initialPolicy={workspace.parser_policy}
            onKindChange={setParserKind}
            onDirtyChange={setParserDirty}
            onSaved={onChanged}
            reset={parserReset}
          />
        )}
        parserKind={effectiveParserKind}
        parserKindLoading={parserKindLoading}
        sampleTitles={sampleTitles}
        modified={modified}
        afterReleaseFilter={(
          <TagFilterCard
            value={edit.tag_filters?.value}
            modified={modified.has("tag_filters")}
            showAniBT={showAniBTRow}
            anibt={anibtObservations}
            title={titleObservations}
            titleVocabulary={titleVocabulary.data}
            titleVocabularyFailed={titleVocabulary.isError}
            onChange={(value) => {
              onContentChange?.();
              setEdit({ ...edit, tag_filters: { mode: "replace", value } });
            }}
          />
        )}
      />
    </div>
  );
}
