import { Fragment, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSavePart } from "../../lib/saveParts";
import {
  getBusinessPreset,
  getTitleTagVocabulary,
  isVersionConflict,
  patchAnimeRules,
  toUserMessage,
  type RulesSections,
} from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { observedExtraTags } from "../../lib/anibtTags";
import { countTags } from "../../lib/titleTags";
import { TagFilterCard } from "../anime-forms/TagFilterCard";
import { changeCount, sectionsFromRules, valuesEqual } from "../../lib/draft";
import { RulesSectionsFields } from "../anime-forms/RulesSectionsFields";
import { StateBlock, Button, ConflictBlock } from "../../ui";
import type { AnimeTabHost, DraftTabHost, OwnerTabHost } from "./host";
import { useOwnerEdit } from "./useOwnerEdit";
import { FileEffectPolicySection } from "./FileEffectPolicySection";
import { ParserPolicySection } from "./ParserPolicySection";
import { DraftParserChoice } from "./DraftParserChoice";
import { draftParserKind, modifiedRuleSections, presetRuleSections, rulesDifferFromPreset } from "../anime-config/businessDraft";
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
  onPreview,
  active = true,
}: {
  host: AnimeTabHost;
  /** A read-only workspace (a retired owner): every rules edit/save/preview entry is hidden. */
  readOnly?: boolean;
  onContentChange?: () => void;
  /** Notifies the outer shell after a successful rules save (owner: leaves rules preview mode). */
  onSaved?: () => void;
  /** The owner-mode rules preview entry (on success the shell switches to the RSS tab's preview mode). */
  onPreview?: (sections: RulesSections) => void;
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
        onPreview={onPreview}
      />
      <FileEffectPolicySection animeID={host.animeID} onChanged={host.onChanged} active={active} />
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
  const modified = modifiedRuleSections(sections, host.businessPreset, draftParserKind(draft));
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
        parserKind={draftParserKind(draft)}
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
// backend would 409 anyway). No mutation callback is consumed here, so the save row / preview
// button simply do not exist.
function ReadOnlyRulesPanel({ host, active }: { host: OwnerTabHost; active: boolean }) {
  return (
    <div className="metadata-stack">
      <StateBlock
        kind="empty"
        title={t("tabs.rulesReadOnly")}
        description={t("anime.rules.endedHint")}
      />
      <div role="group" className="sources-list" aria-label={t("anime.rules.endedAria")}>
        {Object.keys(host.workspace.rules.sections).map((section) => (
          <Fragment key={section}>
            <div className="sources-row">
              <span className="sources-name-text text-body">{tryT(`tabs.sectionName.${section}`) ?? t("maint.unknown")}</span>
            </div>
            {/* The read-only panel has no section body; the parser card follows the "parsing" row directly, matching the editable page's placement. */}
            {section === "parser" && <ParserPolicySection animeID={host.animeID} readOnly active={active} />}
          </Fragment>
        ))}
      </div>
      <FileEffectPolicySection animeID={host.animeID} onChanged={host.onChanged} readOnly active={active} />
    </div>
  );
}

function OwnerRulesTab({
  host,
  active = true,
  onContentChange,
  onSaved,
  onPreview,
}: {
  host: OwnerTabHost;
  active?: boolean;
  onContentChange?: () => void;
  onSaved?: () => void;
  onPreview?: (sections: RulesSections) => void;
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

  // The tag filter card: the title-parsing row acts on all seeds and always shows; the AniBT row
  // shows only when the anime has an AniBT source.
  const showAniBTRow = workspace.rules.has_anibt_source;
  // "Use defaults everywhere": fills the rules sections, tag filter and parser with a copy of the current default settings, saved by the user.
  const queryClient = useQueryClient();
  const businessPreset = useQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset, enabled: active });
  const [resetting, setResetting] = useState(false);
  const titleVocabulary = useQuery({
    queryKey: configKeys.titleTagVocabulary,
    queryFn: getTitleTagVocabulary,
    staleTime: Infinity,
  });
  const anibtObservations = useMemo(() => {
    const lists = workspace.releases.map((release) => release.source_facts.anibt?.tags);
    return { tags: observedExtraTags(lists), counts: countTags(lists) };
  }, [workspace.releases]);
  const titleObservations = useMemo(() => {
    const lists = workspace.releases.map((release) => release.source_facts.title_tags?.tags);
    return { tags: lists.flatMap((tags) => tags ?? []), counts: countTags(lists) };
  }, [workspace.releases]);

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

  if (!edit) return <StateBlock kind="busy" title={t("tabs.rules.reading")} />;
  const matchesDefaults = businessPreset.data !== undefined
    && !rulesDifferFromPreset(edit, businessPreset.data)
    && (parserKind === undefined || parserKind === businessPreset.data.snapshot.parser.kind);
  const modified = modifiedRuleSections(edit, businessPreset.data, parserKind);
  return (
    <div className="metadata-stack">
      <div className="tw:flex tw:items-center tw:justify-between tw:gap-3">
        <p className="naming-muted text-caption tw:min-w-0">{t("tabs.rules.ownerHint")}</p>
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
            onKindChange={setParserKind}
            onDirtyChange={setParserDirty}
            reset={parserReset}
          />
        )}
        parserKind={parserKind}
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
      {onPreview && (
        <div className="save-row">
          <Button variant="secondary" disabled={busy} onClick={() => onPreview(edit)}>
            {t("tabs.rules.preview")}
          </Button>
        </div>
      )}
    </div>
  );
}
