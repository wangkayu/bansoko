import { Fragment, useEffect, useId, useMemo, useRef, useState } from "react";
import { ChevronRight } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  ApiError,
  downloadSnapshotSeed,
  newUUID,
  toUserMessage,
  type AnimeDraft,
  type RSSListFile,
  type RulesSections,
  type SnapshotSeedDownloadInput,
} from "../../lib/api/client";
import { providerLabel } from "../../lib/providers/registry";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { formatBytes } from "../../lib/labels";
import { namingFactLabel, namingFailureDetail, namingPendingLabel, reasonLabel } from "../../lib/i18n/reasons";
import { joinList } from "../../lib/i18n/format";
import { RulesSectionsFields, type SectionKey } from "../anime-forms/RulesSectionsFields";
import { MetadataFields } from "../anime-forms/MetadataFields";
import { draftParserKind, freezeBusinessDraft, withDraftParserKind } from "../anime-config/businessDraft";
import { Button, Field, Input, StateBlock, StatePill } from "../../ui";
import { Dialog, HelpTip, SpinnerIcon } from "../../ui/base";
import { ReasonText } from "../../ui/ReasonText";
import { BoundSubtitles, type BoundSubtitleItem } from "../../ui/BoundSubtitles";
import { fileUnitsSummary, groupBoundSubtitles } from "../../lib/boundSubtitles";
import { t } from "../../lib/i18n";
import { cn } from "../../lib/utils";
import {
  factField,
  needsExpand,
  seedEpisodeLabel,
  selectableRow,
  shortTarget,
  toggleKey,
  type FactField,
  type OneShotSeedGroup,
  type OneShotSeedRow,
} from "./seedRows";
import { REVEAL_MS, Reveal, SeedBrowser } from "./SeedBrowser";
import { OneShotParserSelect } from "./OneShotParserSelect";
import { useOneShotParserOptions } from "./parserOptions";
import { useOneShotSeedSource, type SearchSeedBrowse } from "./useOneShotSeeds";

// The one-shot download: a centered dialog opened by a card's "browse seeds". Two steps:
//   1. Catalog: subgroups listed first, and entering a group fetches its seed list and renders all
//      the seeds at once (RSS facts only — no filter verdicts shown, no subgroup-selection limits).
//      A custom source has a single seed list and enters it directly; a single subgroup is entered
//      directly too (back still returns to the list). A seed's archive is fetched only when the seed
//      is clicked, to show the file list. Selection is checkboxes plus "next" only — no per-row
//      download action; this step parses and renders nothing.
//   2. Preview first: the wide column is the server read model's rendering per seed (filter
//      outcomes and target paths); the narrow column is this task's own configuration (loaded from
//      the global config; a custom preset loads from the preset and edits never write back). Title,
//      Bangumi/TMDB and offset are always visible; parser, file filters and naming collapse into the
//      default-folded "advanced". On phones a sticky top line summarizes (targets and empties),
//      each empty tappable and jumping to its input. Submission happens in this step and the result
//      shows in place.
//
// The dialog presents only the server's judgments: downloadability comes from download_action,
//   filter outcomes and target paths from the RSS read model; the frontend never re-computes rules.

type SeedOutcome = { key: string; ok: boolean; error?: string };

const OFFSET_MIN = -1000;
const OFFSET_MAX = 1000;
//** "Naming renders on its own inside the advanced section (the jump anchor). */
const NAMING_ONLY: readonly SectionKey[] = ["naming"];
/** Re-read throttle for the narrow column's consecutive adjustments. */
const PREVIEW_DEBOUNCE_MS = 250;

/** The brief outline when jumping to an input: a marker only, no motion. */
const JUMP_FLASH = ["tw:outline-2", "tw:outline-offset-2", "tw:outline-warning"];
const JUMP_FLASH_MS = 1600;
/** Typeable fields; hidden inputs that come with dropdown selects are excluded. */
const TEXT_INPUT = "input:not([disabled]):not([type='hidden']):not([aria-hidden='true']), textarea:not([disabled])";
/** Text buttons in the body (empties, summary): inherit the surrounding row's color and font size. */
const inlineLink =
  "tw:cursor-pointer tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:text-inherit tw:underline tw:decoration-dotted tw:underline-offset-2 tw:hover:decoration-solid tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring";
const SPLIT = "\u0000";

/** Dedup empties (several paths may share a name, e.g. multiple episode kinds), keeping the first occurrence's path for jumps. */
function factLabels(facts: readonly string[]): { path: string; label: string }[] {
  const seen = new Map<string, string>();
  for (const path of facts) {
    const label = namingFactLabel(path);
    if (!seen.has(label)) seen.set(label, path);
  }
  return [...seen].map(([label, path]) => ({ path, label }));
}

/** Summary and empty notes jump to the directly editable field first (year, TMDB…), falling back to naming only when none exists. */
function firstFactField(facts: readonly string[]): FactField {
  return facts.map(factField).find((field) => field !== "naming") ?? "naming";
}

/** The preview's empty-facts note: warning colored, each fact tappable, jumping to its matching input. */
function MissingFactsNote({ facts, onJump }: { facts: readonly string[]; onJump: (field: FactField) => void }) {
  const labels = factLabels(facts);
  // {facts} in the copy becomes per-fact buttons; the surrounding text is kept verbatim per language.
  const [before = "", after = ""] = t("oneshot.missingFacts", { facts: SPLIT }).split(SPLIT);
  return (
    <p className="text-caption tw:m-0 tw:text-warning">
      {before}
      {labels.map((fact, index) => (
        <Fragment key={fact.path}>
          {index > 0 && t("common.format.listSeparator")}
          <button type="button" className={inlineLink} onClick={() => onJump(factField(fact.path))}>
            {fact.label}
          </button>
        </Fragment>
      ))}
      {after}
    </p>
  );
}

/** One subtitle in the nested list: the target and filter outcome the server gives. */
function subtitleItem(file: RSSListFile): BoundSubtitleItem {
  const pending = file.render_error ? namingPendingLabel(file.reason_code) : undefined;
  const failure = file.render_error && !pending ? namingFailureDetail(file.reason_code, file.empty_facts) ?? file.render_error : undefined;
  return {
    key: file.file_ref,
    path: file.source_path,
    sizeBytes: file.size_bytes,
    language: file.sub_lang_tag ?? file.language,
    languageSource: file.sub_lang_tag ? file.sub_lang_source : undefined,
    included: file.included,
    reasonCode: file.reason_code,
    target: file.rendered_target,
    targetProblem: pending ?? failure,
  };
}

/** Step two's file row: target and filter outcome. The target path is this step's verdict, one font step above the source.
 *  Beneath the video row sit the external subtitles the server bound to it: format badges, details collapsed by default. */
function FileRow({ file, subtitles, onJump }: { file: RSSListFile; subtitles: RSSListFile[]; onJump: (field: FactField) => void }) {
  const pending = file.render_error ? namingPendingLabel(file.reason_code) : undefined;
  const failure = file.render_error && !pending ? namingFailureDetail(file.reason_code, file.empty_facts) ?? file.render_error : undefined;
  const emptyFacts = file.included ? (file.empty_facts ?? []) : [];
  return (
    <li className="tw:grid tw:gap-0.5 tw:rounded-md tw:bg-muted/15 tw:px-2.5 tw:py-2">
      <p className="text-caption break-anywhere tw:m-0 tw:text-muted-foreground" title={file.source_path}>
        {file.source_path}
        {file.size_bytes !== undefined && <span> · {formatBytes(file.size_bytes)}</span>}
      </p>
      <p className="break-anywhere tw:m-0 tw:text-sm" title={file.rendered_target ?? undefined}>
        {file.rendered_target ?? <span className="text-muted text-caption">{pending ?? failure ?? t("oneshot.targetMissing")}</span>}
      </p>
      {emptyFacts.length > 0 && <MissingFactsNote facts={emptyFacts} onJump={onJump} />}
      {(file.in_library === true || (!file.included && file.reason_code)) && (
        <p className="tw:m-0 tw:flex tw:flex-wrap tw:items-center tw:gap-1.5 text-caption tw:text-muted-foreground">
          {file.in_library === true && <StatePill tone="online">{t("oneshot.fileInLibrary")}</StatePill>}
          {!file.included && file.reason_code ? <ReasonText code={file.reason_code} /> : null}
        </p>
      )}
      <BoundSubtitles videoName={file.source_path} subtitles={subtitles.map(subtitleItem)} videoExcluded={!file.included} />
    </li>
  );
}

/** One seed's render: a one-line verdict, per-file targets, and the submission result. */
function SeedRender({
  row,
  expandError,
  outcome,
  onJump,
}: {
  row: OneShotSeedRow;
  expandError?: string;
  outcome?: SeedOutcome;
  onJump: (field: FactField) => void;
}) {
  const files = row.item.files;
  const groups = groupBoundSubtitles(files, (file) => file.source_path, (file) => file.bound_video_source_path);
  const overlap = row.item.library_overlap;
  const episode = seedEpisodeLabel(row);
  return (
    <li className="tw:grid tw:gap-1.5 tw:rounded-control tw:border tw:border-border tw:px-3 tw:py-2.5 tw:motion-safe:animate-reveal">
      <p className="break-anywhere tw:m-0 tw:text-sm tw:font-medium">{row.title}</p>
      {outcome && (
        <p className="tw:m-0 tw:flex tw:flex-wrap tw:items-center tw:gap-1.5">
          <StatePill tone={outcome.ok ? "online" : "offline"}>
            {outcome.ok ? t("oneshot.resultOk") : t("oneshot.resultFailed")}
          </StatePill>
          {outcome.error && <span className="text-muted text-caption">{outcome.error}</span>}
        </p>
      )}
      {expandError ? (
        <StateBlock kind="error" title={expandError} />
      ) : needsExpand(row) ? (
        <span className="tw:grid tw:gap-1.5" role="status" aria-label={t("oneshot.expanding")}>
          <span className="tw:block tw:h-3 tw:w-2/3 tw:rounded tw:bg-muted tw:motion-safe:animate-pulse" />
          <span className="tw:block tw:h-3 tw:w-1/2 tw:rounded tw:bg-muted tw:motion-safe:animate-pulse" />
        </span>
      ) : files.length === 0 ? (
        <p className="text-muted text-caption tw:m-0">{t("oneshot.previewEmpty")}</p>
      ) : (
        <>
          {/* Episodes, the per-video-unit file count and library overlap are peer meta facts: one line joined with " · ". */}
          <p className="text-caption tw:m-0 tw:text-muted-foreground">
            {episode && <span>{episode} · </span>}
            <span>{fileUnitsSummary(groups, (file) => ({ kind: file.media_kind, included: file.included, target: file.rendered_target }))}</span>
            <span> · </span>
            <span>
            {overlap.verdict === "present"
              ? t("oneshot.libraryPresent", { present: overlap.present_files ?? 0, pending: overlap.pending_files ?? 0 })
              : overlap.verdict === "absent"
                ? t("oneshot.libraryAbsent")
                : overlap.reason_code
                  ? t("oneshot.libraryUndeterminedReason", { reason: reasonLabel(overlap.reason_code) ?? overlap.reason_code })
                  : t("oneshot.libraryUndetermined")}
            </span>
          </p>
          <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0">
            {groups.map(({ file, subtitles }) => (
              <FileRow key={file.file_ref} file={file} subtitles={subtitles} onJump={onJump} />
            ))}
          </ul>
        </>
      )}
    </li>
  );
}

export function OneShotDialog({
  open,
  entry,
  search,
  onClose,
  onBack,
  onOpenRecords,
}: {
  open: boolean;
  /** The catalog entry whose seeds are browsed; exactly one of entry/search is given. */
  entry?: CatalogEntryView;
  /** A keyword-search snapshot: a clicked search result browses its seeds in this same dialog. */
  search?: SearchSeedBrowse;
  onClose: () => void;
  /**
   * Returns to the surface that opened this dialog (e.g. the search results window) instead of
   * dismissing everything. Step one's footer offers it in place of cancel; closing (the header
   * close, Esc) still dismisses.
   */
  onBack?: () => void;
  /** Jumps to the execution record (the one-shot filter); no entry renders when the host has not wired it. */
  onOpenRecords?: () => void;
}) {
  const source = useOneShotSeedSource({ entry, search, enabled: open });
  // The header's source label: the entry title, or the keyword the user searched (the server
  // never echoes the query text, so a search source renders the text the frontend itself holds).
  // A search renders as a source ("Torrent search · query"), never as a work title.
  const sourceLabel = entry?.title ?? (search ? t("oneshot.searchSourceLabel", { query: search.query.trim() }) : "");
  const site = entry?.source ?? search?.provider ?? "anibt";
  const sourceRef = useRef(source);
  sourceRef.current = source;

  const [step, setStep] = useState<1 | 2>(1);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const [expandingKeys, setExpandingKeys] = useState<ReadonlySet<string>>(new Set());
  const [filter, setFilter] = useState("");
  const [offsetText, setOffsetText] = useState(String(source.initialOffset));
  const [matchProvider, setMatchProvider] = useState<"bgm" | "tmdb">();
  const [submitting, setSubmitting] = useState(false);
  const [outcomes, setOutcomes] = useState<ReadonlyMap<string, SeedOutcome>>(new Map());
  const [expandErrors, setExpandErrors] = useState<ReadonlyMap<string, string>>(new Map());
  // One idempotency key per seed: a retry of the same request (e.g. after a network failure) reuses
//   the same key and the server dedups by it; a changed request (config edited) takes a new key, or
//   the server rejects same-key different-body.
  const idempotency = useRef(new Map<string, { key: string; body: string }>());

  // A custom preset's offset is known only after the preset is read: sync the input once on first arrival.
  const offsetSeeded = useRef(false);
  useEffect(() => {
    if (offsetSeeded.current || source.draft === undefined) return;
    offsetSeeded.current = true;
    setOffsetText(String(source.initialOffset));
  }, [source.draft, source.initialOffset]);

  const allRows = useMemo(() => source.groups.flatMap((group) => group.rows), [source.groups]);
  // Selection survives filters, groups and re-reads: a filter is only a view, and a re-read changes row content, never identity.
  const selectedRows = useMemo(() => allRows.filter((row) => selected.has(row.key)), [allRows, selected]);

  // Step one's navigation: the subgroup list first, then a group's seeds. A custom preset and a
  // keyword search both have a single seed list and no subgroup layer.
  const [activeGroupKey, setActiveGroupKey] = useState<string>();
  const [direction, setDirection] = useState<"forward" | "back">("forward");
  const singleList = source.kind !== "provider";
  const singleGroup = singleList ? source.groups[0] : undefined;
  const activeGroup = singleGroup ?? source.groups.find((group) => group.key === activeGroupKey);
  const activeEntry = source.groupEntries.find((group) => group.key === activeGroupKey);
  const level: "groups" | "seeds" = singleList || activeGroupKey !== undefined ? "seeds" : "groups";
  const needle = filter.trim().toLowerCase();
  const selectableVisible = useMemo(
    () => (activeGroup?.rows ?? []).filter((row) => (!needle || row.title.toLowerCase().includes(needle)) && selectableRow(row)),
    [activeGroup, needle],
  );

  // Match-dialog query candidates (search source only): the parsed title hints of the checked
  // seeds, deduped, most frequent first; the query text itself is always the last candidate.
  const matchQueryCandidates = useMemo(() => {
    if (!search) return undefined;
    const hints = new Map(search.snapshot.seeds.map((seed) => [seed.stable_seed_key, seed.title_hint]));
    const counts = new Map<string, number>();
    for (const row of selectedRows) {
      const hint = hints.get(row.stableSeedKey)?.trim();
      if (hint) counts.set(hint, (counts.get(hint) ?? 0) + 1);
    }
    const ranked = [...counts].sort((a, b) => b[1] - a[1]).map(([hint]) => hint);
    const query = search.query.trim();
    if (query && !counts.has(query)) ranked.push(query);
    return ranked;
  }, [search, selectedRows]);

  const trimmedOffset = offsetText.trim();
  const offsetValue = trimmedOffset === "" ? Number.NaN : Number(trimmedOffset);
  const offsetValid = Number.isInteger(offsetValue) && offsetValue >= OFFSET_MIN && offsetValue <= OFFSET_MAX;
  const effectiveOffset = offsetValid ? offsetValue : 0;

  const draft = source.draft;
  // A match confirmation writes metadata and then the title in one event: the second write must build on the first, not on the render's stale draft.
  const latestDraft = useRef(draft);
  latestDraft.current = draft;
  function commitDraft(next: AnimeDraft) {
    latestDraft.current = next;
    source.setDraft(next);
  }
  // "The configuration loaded at task start": restore-defaults resets locally only, with no request.
  const baseline = useRef<RulesSections>();
  const baselineParser = useRef<NonNullable<AnimeDraft["business"]>["parser"]>();
  useEffect(() => {
    if (!baseline.current && draft) baseline.current = structuredClone(draft.rules.sections);
    if (!baselineParser.current && draft?.business) baselineParser.current = structuredClone(draft.business.parser);
  }, [draft]);

  // Parser: the same option set as the instance's episode parser, shown only with multiple options
//   and only in step two (config + render). Edits go only into this task's draft (frozen and
//   committed with it); step two's preview re-reads under it.
  const parserKind = draftParserKind(draft);
  const parserOptions = useOneShotParserOptions(open, parserKind);
  const multipleParsers = parserOptions.length > 1;
  const configSections = useMemo<readonly SectionKey[]>(
    () => (multipleParsers ? ["parser", "file_filter"] : ["file_filter"]),
    [multipleParsers],
  );
  function changeParser(kind: string) {
    if (!draft || !baselineParser.current || kind === parserKind) return;
    source.setDraft(withDraftParserKind(draft, kind, baselineParser.current));
  }
  // The preview input is a read-only ref: selection changes must not change the effect identity, yet a re-read must use the latest selection.
  const previewInput = useRef({ draft, offset: effectiveOffset, rows: selectedRows });
  previewInput.current = { draft, offset: effectiveOffset, rows: selectedRows };

  /** Expands a seed (fetching bytes only), then re-reads the view under the current draft and offset on success. */
  async function expandRow(row: OneShotSeedRow): Promise<string | undefined> {
    setExpandingKeys((current) => new Set(current).add(row.key));
    try {
      const outcome = await sourceRef.current.expand(row);
      setExpandErrors((current) => {
        const next = new Map(current);
        if (outcome.error) next.set(row.key, outcome.error);
        else next.delete(row.key);
        return next;
      });
      if (!outcome.error) {
        const { draft: current, offset } = previewInput.current;
        await sourceRef.current.preview(current, offset);
      }
      return outcome.error;
    } finally {
      setExpandingKeys((current) => {
        const next = new Set(current);
        next.delete(row.key);
        return next;
      });
    }
  }

  // Any step-two adjustment re-reads under the same in-editing draft (throttled). Before re-reading,
//   selected-but-unexpanded seeds are expanded once: the read model returns only pending for
//   an unexpanded seed, impossible to preview.
  const previewSeq = useRef(0);
  useEffect(() => {
    if (!open || step !== 2 || !offsetValid) return;
    // A search draft starts without a title: no preview is requested until the user supplies one.
    if (source.kind === "search" && !previewInput.current.draft?.title.trim()) return;
    const seq = ++previewSeq.current;
    const timer = window.setTimeout(() => {
      void (async () => {
        const { draft: current, offset, rows } = previewInput.current;
        const errors = new Map<string, string>();
        for (const row of rows) {
          if (!needsExpand(row)) continue;
          const outcome = await sourceRef.current.expand(row);
          if (outcome.error) errors.set(row.key, outcome.error);
        }
        if (seq !== previewSeq.current) return;
        setExpandErrors(errors);
        await sourceRef.current.preview(current, offset);
      })();
    }, PREVIEW_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [open, step, offsetValid, draft, offsetText, selected, source.kind]);

  function openGroup(key: string) {
    setDirection("forward");
    setActiveGroupKey(key);
    setFilter("");
    const { draft: current, offset } = previewInput.current;
    void source.openGroup(key, current, offset);
  }

  function backToGroups() {
    setDirection("back");
    setActiveGroupKey(undefined);
    setFilter("");
  }

  function toggleSelection(row: OneShotSeedRow) {
    setSelected((current) => toggleKey(current, row.key));
  }

  function toggleExpanded(row: OneShotSeedRow) {
    const opening = !expanded.has(row.key) || expandErrors.has(row.key);
    setExpanded((current) => (expandErrors.has(row.key) ? new Set(current).add(row.key) : toggleKey(current, row.key)));
    // A seed's archive is fetched only when the seed is opened and has no files yet (once only; no repeat while in flight).
    if (opening && needsExpand(row) && !expandingKeys.has(row.key)) void expandRow(row);
  }

  // With a single subgroup it is entered directly (first time only: after going back the list
//   stays). Waiting for the config to load matters: reads after entering carry this task's draft.
  const autoEntered = useRef(false);
  const onlyGroupKey = source.kind === "provider" && source.groupEntries.length === 1 ? source.groupEntries[0]!.key : undefined;
  useEffect(() => {
    if (autoEntered.current || !open || step !== 1 || onlyGroupKey === undefined) return;
    if (source.loading || source.error || !source.draft || activeGroupKey !== undefined) return;
    autoEntered.current = true;
    openGroup(onlyGroupKey);
    // openGroup is a fresh function every render; enter exactly once, at the instant the condition holds.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, step, onlyGroupKey, source.loading, source.error, source.draft, activeGroupKey]);

  async function refreshGroup(group: OneShotSeedGroup) {
    const { draft: current, offset } = previewInput.current;
    const ok = await source.refreshGroup(group, current, offset);
    // After a refresh the snapshot identity changed: that group's original selection no longer refers to the same seeds.
    if (ok) setSelected((current) => new Set([...current].filter((key) => !group.rows.some((row) => row.key === key))));
  }

  /** Resets the naming / file-filter / parser sections and the episode offset to their task-start values. */
  function restoreDefaults() {
    setOffsetText(String(source.initialOffset));
    const base = baseline.current;
    if (!base || !draft) return;
    const sections: RulesSections = {
      ...draft.rules.sections,
      naming: structuredClone(base.naming),
      file_filter: structuredClone(base.file_filter),
      parser: structuredClone(base.parser),
    };
    const restored = { ...draft, rules: { sections } };
    const parser = baselineParser.current;
    source.setDraft(parser ? withDraftParserKind(restored, parser.kind, parser) : restored);
  }

  const submitted = outcomes.size > 0;
  const okKeys = new Set([...outcomes.values()].filter((outcome) => outcome.ok).map((outcome) => outcome.key));
  // Selected seeds not yet successfully submitted: everything on the first submit, only the failures after that.
  const pendingSubmit = selectedRows.filter((row) => !okKeys.has(row.key));

  /** A search draft's instance_label follows the title: the server requires both non-empty, so they are edited as one. */
  function setTitle(title: string) {
    const base = latestDraft.current;
    if (!base) return;
    commitDraft(source.kind === "search" ? { ...base, title, instance_label: title } : { ...base, title });
  }

  // Why submission is blocked (rendering only).
  const createBlock: string | undefined = !draft
    ? t("addPreset.loading")
    : !draft.title.trim()
      ? t("config.validation.titleRequired")
      : !offsetValid
      ? t("rss.offsetInvalid")
      : pendingSubmit.some(needsExpand)
        ? t("oneshot.expanding")
        : pendingSubmit.some((row) => row.feed.snapshot_version == null || !row.feed.content_hash)
          ? t("oneshot.previewStale")
          : undefined;

  function requestFor(row: OneShotSeedRow, prepared: AnimeDraft): SnapshotSeedDownloadInput {
    return {
      provider: row.scope.provider === "custom" ? "custom" : row.scope.provider === "mikan" ? "mikan" : "anibt",
      provider_entry_ref: row.scope.provider_entry_ref,
      provider_group_ref: row.scope.provider_group_ref,
      snapshot_version: row.feed.snapshot_version ?? 0,
      content_hash: row.feed.content_hash ?? "",
      input_source: "current_input",
      draft: prepared,
      ...(effectiveOffset !== 0 ? { episode_offset: effectiveOffset } : {}),
    };
  }

  function idempotencyKeyFor(row: OneShotSeedRow, input: SnapshotSeedDownloadInput): string {
    const body = JSON.stringify([row.snapshotID, row.stableSeedKey, input]);
    const known = idempotency.current.get(row.key);
    if (known && known.body === body) return known.key;
    const key = newUUID();
    idempotency.current.set(row.key, { key, body });
    return key;
  }

  /** Why one seed's submission failed; an expired search identity has no refresh, only a new search. */
  function submitErrorOf(cause: unknown): string {
    if (source.kind === "search" && cause instanceof ApiError && cause.code === "FEED_SNAPSHOT_REFRESH_REQUIRED") return t("oneshot.searchStale");
    return toUserMessage(cause, t("rss.oneShotFailed"));
  }

  async function create(targets: OneShotSeedRow[]) {
    if (targets.length === 0 || !draft) return;
    setSubmitting(true);
    let prepared: AnimeDraft;
    try {
      // The server re-freezes the draft and compares business_digest: what is submitted must already be the frozen form.
      prepared = await freezeBusinessDraft(draft);
    } catch (cause) {
      setSubmitting(false);
      toast.error(toUserMessage(cause, t("rss.oneShotFailed")));
      return;
    }
    const next: SeedOutcome[] = [];
    for (const row of targets) {
      const input = requestFor(row, prepared);
      try {
        await downloadSnapshotSeed(row.snapshotID, row.stableSeedKey, input, idempotencyKeyFor(row, input));
        next.push({ key: row.key, ok: true });
      } catch (cause) {
        next.push({ key: row.key, ok: false, error: submitErrorOf(cause) });
      }
    }
    setOutcomes((previous) => {
      const merged = new Map(previous);
      for (const outcome of next) merged.set(outcome.key, outcome);
      return merged;
    });
    setSubmitting(false);
    const ok = next.filter((outcome) => outcome.ok).length;
    const records = onOpenRecords ? { action: { label: t("oneshot.viewRecords"), onClick: onOpenRecords } } : undefined;
    if (ok === next.length) toast.success(t("oneshot.createdAll", { n: ok }), records);
    else if (ok === 0) toast.error(t("oneshot.createdNone"), records);
    else toast.warning(t("oneshot.createdPartial", { ok, failed: next.length - ok }), records);
  }

  // --- step two: advanced fold, phone summary, jump-to-input -------------------------
  const advancedId = useId();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const settingsRef = useRef<HTMLElement>(null);
  const previewRef = useRef<HTMLElement>(null);
  const namingRef = useRef<HTMLDivElement>(null);
  const [jump, setJump] = useState<{ field: FactField; seq: number }>();

  /** Jumps to an input: naming sits inside the advanced section, so expand it first. */
  function jumpTo(field: FactField) {
    if (field === "naming") setAdvancedOpen(true);
    setJump((current) => ({ field, seq: (current?.seq ?? 0) + 1 }));
  }

  function jumpNode(field: FactField): HTMLElement | null {
    const settings = settingsRef.current;
    if (!settings) return null;
    if (field === "naming") return namingRef.current;
    if (field === "title") return settings.querySelector<HTMLElement>("#oneshot-title");
    // The metadata rows come from the shared MetadataFields, with row titles carrying `${idPrefix}-…-label` ids.
    const labelId = { year: "oneshot-year-label", season: "oneshot-season-label", bgm: "oneshot-bgm-label", tmdb: "oneshot-tmdb-label", kind: "oneshot-media-kind-label" }[field];
    return settings.querySelector<HTMLElement>(`#${labelId}`)?.closest<HTMLElement>("section") ?? null;
  }

  useEffect(() => {
    if (!jump) return;
    let frame = 0;
    let timer = 0;
    let flashTimer = 0;
    let tries = 0;
    const reduced = typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const run = () => {
      const node = jumpNode(jump.field);
      // A just-expanded advanced section mounts on the next frame.
      if (!node) {
        if (tries++ < 10) frame = window.requestAnimationFrame(run);
        return;
      }
      node.scrollIntoView?.({ block: "center", behavior: reduced ? "auto" : "smooth" });
      // Look for a typeable field first (year, naming template); only without one land on a row button (match TMDB and friends).
      const focusable = node.matches("input, button")
        ? node
        : (node.querySelector<HTMLElement>(TEXT_INPUT) ?? node.querySelector<HTMLElement>("button:not([disabled])"));
      focusable?.focus({ preventScroll: true });
      node.classList.add(...JUMP_FLASH);
      flashTimer = window.setTimeout(() => node.classList.remove(...JUMP_FLASH), JUMP_FLASH_MS);
    };
    // Expanding the advanced section has a row-height transition: wait for it to finish before positioning, or the jump lands off.
    if (jump.field === "naming") timer = window.setTimeout(run, REVEAL_MS + 20);
    else run();
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
      window.clearTimeout(flashTimer);
    };
    // jumpNode is a read-only ref; each jump is triggered by seq.
  }, [jump]);

  // The advanced section's three parts differ from task start: a one-line hint shows even while folded.
  const advancedChanged = (() => {
    const base = baseline.current;
    if (!draft || !base) return false;
    const pick = (sections: RulesSections) => JSON.stringify([sections.naming, sections.file_filter, sections.parser]);
    const parserMoved = baselineParser.current !== undefined && parserKind !== undefined && parserKind !== baselineParser.current.kind;
    return parserMoved || pick(draft.rules.sections) !== pick(base);
  })();
  const advancedSummary = joinList([
    ...(multipleParsers ? [t("forms.rules.parserTitle")] : []),
    t("settings.rules.fileRules"),
    t("forms.rules.namingTitle"),
  ]);

  const awaitingTitle = source.kind === "search" && !!draft && !draft.title.trim();
  // The phone's sticky summary: seed count → the first target (shorthand) + empties.
  const summary = useMemo(() => {
    if (selectedRows.length === 0) return undefined;
    const files = selectedRows.flatMap((row) => row.item.files.filter((file) => file.included));
    const targets = files.flatMap((file) => (file.rendered_target ? [file.rendered_target] : []));
    const facts = [...new Set(files.flatMap((file) => file.empty_facts ?? []))];
    let target: string;
    // A search draft without a title has only placeholder-rendered targets (see useOneShotSeeds): show the prompt instead.
    if (awaitingTitle) {
      target = t("oneshot.titleEmptyPreview");
    } else if (targets.length > 0) {
      target = shortTarget(targets[0]!);
      if (targets.length > 1) target = `${target} ${t("oneshot.summaryMore", { n: targets.length })}`;
    } else if (selectedRows.some(needsExpand) || (files.length === 0 && source.pending)) {
      target = t("oneshot.previewing");
    } else if (files.length === 0) {
      target = t("oneshot.summaryNoFiles");
    } else {
      const failed = files[0]!;
      target = namingPendingLabel(failed.reason_code) ?? namingFailureDetail(failed.reason_code, failed.empty_facts) ?? t("oneshot.targetMissing");
    }
    return { text: t("oneshot.summary", { n: selectedRows.length, target }), facts };
  }, [selectedRows, source.pending, awaitingTitle]);

  const stepIntro = (
    <p className="text-caption tw:m-0 tw:text-muted-foreground">
      {t("oneshot.stepPosition", { n: step })} · {step === 1 ? t("oneshot.stepSeeds") : t("oneshot.stepSettings")}
    </p>
  );

  const allDone = submitted && pendingSubmit.length === 0;

  return (
    <Dialog
      open={open}
      onRequestClose={onClose}
      // Step one is a single column of subgroups/seeds: the M size (fixed height, entering/leaving a
//       group never jumps); step two needs side-by-side preview and config: the L size. On phones
//       both sizes go fullscreen.
      tier={step === 1 ? "M" : "L"}
      fixedHeight
      bodyLayout="fill"
      title={t("oneshot.title")}
      description={
        <span className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
          <span className="tw:min-w-0 tw:truncate" title={sourceLabel}>
            {sourceLabel}
          </span>
          <span>· {providerLabel(site)}</span>
        </span>
      }
      footer={
        step === 1 ? (
          <>
            {onBack ? (
              <Button onClick={onBack}>{t("act.back")}</Button>
            ) : (
              <Button onClick={onClose}>{t("act.cancel")}</Button>
            )}
            <Button
              variant="primary"
              onClick={() => setStep(2)}
              disabled={source.loading || selectedRows.length === 0}
            >
              {t("oneshot.nextWithCount", { n: selectedRows.length })}
            </Button>
          </>
        ) : (
          <>
            {/* After submission there is no return to the catalog: a created task never follows step one's selection. */}
            {!submitted && (
              <Button onClick={() => setStep(1)} disabled={submitting}>
                {t("oneshot.back")}
              </Button>
            )}
            {submitted && onOpenRecords && <Button onClick={onOpenRecords}>{t("oneshot.viewRecords")}</Button>}
            {allDone ? (
              <Button variant="primary" onClick={onClose}>
                {t("oneshot.done")}
              </Button>
            ) : (
              <Button
                variant="primary"
                onClick={() => void create(pendingSubmit)}
                disabled={submitting || createBlock !== undefined || pendingSubmit.length === 0}
                title={createBlock}
              >
                {submitting
                  ? t("oneshot.creating")
                  : submitted
                    ? t("oneshot.retryFailed", { n: pendingSubmit.length })
                    : t("oneshot.create", { n: pendingSubmit.length })}
              </Button>
            )}
          </>
        )
      }
    >
      {step === 1 && (
        <SeedBrowser
          level={level}
          direction={direction}
          header={stepIntro}
          emptyState={
            source.error && !(level === "seeds" && activeGroup) ? (
              <StateBlock
                kind="error"
                title={source.error}
                action={
                  // An expired search identity cannot be re-read usefully; the copy already says to search again.
                  source.stale ? undefined : (
                    <Button size="sm" onClick={source.reload}>
                      {t("act.retry")}
                    </Button>
                  )
                }
              />
            ) : singleList && source.loading ? (
              <StateBlock kind="busy" title={t("oneshot.seeding")} />
            ) : source.fetchRequired || source.fetchBusy ? (
              <StateBlock
                kind="empty"
                title={t("oneshot.loadSeedsHint")}
                action={
                  <Button size="sm" disabled={source.fetchBusy} onClick={() => source.fetchSeeds?.()}>
                    {source.fetchBusy ? t("act.refreshing") : t("oneshot.loadSeeds")}
                  </Button>
                }
              />
            ) : source.kind === "provider" && !source.loading && source.groupEntries.length === 0 ? (
              <StateBlock
                kind="empty"
                title={t("oneshot.noGroups")}
                action={
                  <Button size="sm" onClick={source.reload}>
                    {t("oneshot.refreshSeedsAction")}
                  </Button>
                }
              />
            ) : undefined
          }
          groupEntries={source.groupEntries}
          loadingGroups={source.loading}
          openingGroup={source.openingGroup}
          openError={source.openError}
          onOpenGroup={openGroup}
          activeGroup={activeGroup}
          activeTitle={source.kind === "search" ? sourceLabel : (activeGroup?.name ?? activeEntry?.name ?? "")}
          activeLoading={
            singleList
              ? activeGroup === undefined && source.pending
              : activeGroupKey !== undefined &&
                activeGroup === undefined &&
                !(source.openError?.key === activeGroupKey) &&
                (source.openingGroup === activeGroupKey || source.pending)
          }
          onBack={source.kind === "provider" ? backToGroups : undefined}
          onRefresh={activeGroup && source.kind === "provider" ? () => void refreshGroup(activeGroup) : undefined}
          refreshing={activeGroup !== undefined && source.refreshingGroup === activeGroup.key}
          refreshError={source.refreshError ?? (level === "seeds" && activeGroup ? source.error : undefined) ?? (source.openError?.key === activeGroupKey ? source.openError?.message : undefined)}
          pending={source.pending}
          filter={filter}
          onFilterChange={setFilter}
          selectedCount={selectedRows.length}
          onSelectAll={() => setSelected((current) => new Set([...current, ...selectableVisible.map((row) => row.key)]))}
          onClear={() => setSelected(new Set())}
          rowState={(row) => ({
            selected: selected.has(row.key),
            expanded: expanded.has(row.key),
            expanding: expandingKeys.has(row.key),
            expandError: expandErrors.get(row.key),
          })}
          onToggleSelected={toggleSelection}
          onToggleExpanded={toggleExpanded}
        />
      )}

      {step === 2 && (
        // Desktop: a step-position line on top, then the wide preview column left and the narrow
//         config column right, each scrolling. Phone: one column, scrolled by this container with a
//         sticky summary bar. auto-rows-max keeps min-h-0 columns from being crushed into equal
//         fixed row heights and overwriting each other.
        <div className="tw:grid tw:min-h-0 tw:flex-1 tw:grid-cols-[minmax(0,1fr)_minmax(0,24rem)] tw:grid-rows-[auto_minmax(0,1fr)] tw:phone:grid-cols-1 tw:phone:grid-rows-none tw:phone:auto-rows-max tw:phone:overflow-y-auto tw:phone:overscroll-contain">
          <div className="tw:col-span-2 tw:px-6 tw:pt-3 tw:phone:col-span-1 tw:phone:px-4">{stepIntro}</div>

          {summary && (
            <div
              data-oneshot-summary=""
              className="text-caption tw:sticky tw:top-0 tw:z-10 tw:hidden tw:items-center tw:gap-2 tw:border-b tw:border-border tw:bg-popover tw:px-4 tw:py-2 tw:phone:flex"
            >
              <button
                type="button"
                className="tw:min-w-0 tw:flex-1 tw:cursor-pointer tw:truncate tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:text-left tw:text-inherit tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
                onClick={() => previewRef.current?.scrollIntoView?.({ block: "start" })}
              >
                {summary.text}
              </button>
              {summary.facts.length > 0 && (
                <button
                  type="button"
                  className={cn(inlineLink, "tw:flex-none tw:text-warning")}
                  onClick={() => jumpTo(firstFactField(summary.facts))}
                >
                  {t("oneshot.missingFacts", { facts: joinList(factLabels(summary.facts).map((fact) => fact.label)) })}
                </button>
              )}
            </div>
          )}

          <section
            ref={previewRef}
            aria-label={t("oneshot.previewTitle")}
            className="tw:grid tw:min-h-0 tw:scroll-mt-12 tw:content-start tw:gap-2 tw:overflow-y-auto tw:px-6 tw:pt-3 tw:pb-4 tw:phone:overflow-visible tw:phone:px-4"
          >
            <div className="tw:flex tw:items-center tw:gap-2">
              <h3 className="text-body tw:m-0">{t("oneshot.previewTitle")}</h3>
              {source.pending && selectedRows.length > 0 && !source.error && (
                <span role="status" className="tw:flex tw:items-center tw:gap-1 tw:text-xs tw:text-muted-foreground tw:motion-safe:animate-fade-in">
                  <SpinnerIcon className="tw:size-3.5" />
                  {t("oneshot.previewing")}
                </span>
              )}
            </div>
            {createBlock && !submitting && draft && offsetValid && (
              <p className="text-caption tw:m-0 tw:text-muted-foreground">{createBlock}</p>
            )}
            {source.pending && selectedRows.length === 0 ? (
              <StateBlock kind="busy" title={t("oneshot.previewing")} />
            ) : source.error ? (
              <StateBlock kind="error" title={source.error} />
            ) : selectedRows.length === 0 ? (
              <StateBlock kind="empty" title={t("oneshot.selectToPreview")} />
            ) : awaitingTitle ? (
              <StateBlock kind="empty" title={t("oneshot.titleEmptyPreview")} />
            ) : (
              <ul className="tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0">
                {selectedRows.map((row) => (
                  <SeedRender
                    key={row.key}
                    row={row}
                    expandError={expandErrors.get(row.key)}
                    outcome={outcomes.get(row.key)}
                    onJump={jumpTo}
                  />
                ))}
              </ul>
            )}
          </section>

          <section
            ref={settingsRef}
            aria-label={t("oneshot.configTitle")}
            className="tw:grid tw:min-h-0 tw:content-start tw:gap-3 tw:overflow-y-auto tw:border-l tw:border-border tw:px-5 tw:pt-3 tw:pb-4 tw:phone:overflow-visible tw:phone:border-l-0 tw:phone:border-t tw:phone:px-4 tw:phone:pt-4"
          >
            <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
              <h3 className="text-body tw:m-0 tw:flex-1">{t("oneshot.configTitle")}</h3>
              <HelpTip label={t("oneshot.restoreDefaultsHelpLabel")}>{t("oneshot.restoreDefaultsHint")}</HelpTip>
              <Button size="sm" variant="secondary" disabled={!draft} onClick={restoreDefaults}>
                {t("oneshot.restoreDefaults")}
              </Button>
            </div>
            <p className="text-caption tw:m-0 tw:text-muted-foreground">
              {source.kind === "custom" ? t("oneshot.customConfigNote") : t("oneshot.configLocalNote")}
            </p>

            {draft && (
              <div className="tw:grid tw:gap-2">
                <Field label={t("field.title")}>
                  <Input
                    id="oneshot-title"
                    aria-label={t("field.title")}
                    value={draft.title}
                    onChange={(event) =>
                      // A search draft's instance_label follows the title: the server requires both
                      // non-empty from the first read on, so they are edited as one.
                      setTitle(event.target.value)
                    }
                  />
                </Field>
                <MetadataFields
                  idPrefix="oneshot"
                  showYear
                  showSeason
                  title={draft.title}
                  matchQueryCandidates={matchQueryCandidates}
                  onMatchTitle={(matched) => {
                    // Only fills an empty title; a title the user already has is never overwritten.
                    if (!(latestDraft.current ?? draft).title.trim()) setTitle(matched);
                  }}
                  value={draft.metadata}
                  onChange={(metadata) => commitDraft({ ...(latestDraft.current ?? draft), metadata })}
                  matchProvider={matchProvider}
                  onMatchProviderChange={setMatchProvider}
                  onSaveTotalEpisodes={async (total) => {
                    source.setDraft({ ...draft, metadata: { ...draft.metadata, total_episodes: total } });
                    return true;
                  }}
                  onSaveMediaKind={async (next) => {
                    source.setDraft({ ...draft, metadata: { ...draft.metadata, media_kind_override: next } });
                    return true;
                  }}
                  mediaKindOverride={draft.metadata.media_kind_override}
                />
              </div>
            )}

            <div className="tw:grid tw:gap-1.5">
              <Field label={t("oneshot.offset")}>
                <Input
                  aria-label={t("oneshot.offset")}
                  inputMode="numeric"
                  type="number"
                  min={OFFSET_MIN}
                  max={OFFSET_MAX}
                  step="1"
                  value={offsetText}
                  onChange={(event) => setOffsetText(event.target.value)}
                />
              </Field>
              {!offsetValid && <p className="text-danger text-caption">{t("rss.offsetInvalid")}</p>}
            </div>

            {draft && (
              <div className="tw:grid tw:gap-2 tw:border-t tw:border-border tw:pt-3">
                <button
                  type="button"
                  aria-expanded={advancedOpen}
                  aria-controls={advancedId}
                  onClick={() => setAdvancedOpen((value) => !value)}
                  className="tw:flex tw:w-full tw:min-w-0 tw:cursor-pointer tw:items-center tw:gap-2 tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:py-1 tw:text-left tw:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
                >
                  <ChevronRight
                    aria-hidden="true"
                    className={cn(
                      "icon-sm tw:flex-none tw:text-muted-foreground tw:transition-transform tw:duration-200 tw:ease-out tw:motion-reduce:transition-none",
                      advancedOpen && "tw:rotate-90",
                    )}
                  />
                  <span className="text-body tw:flex-none">{t("oneshot.advanced")}</span>
                  <span className="tw:min-w-0 tw:flex-1 tw:truncate tw:text-xs tw:text-muted-foreground">{advancedSummary}</span>
                  {advancedChanged && <StatePill tone="warning">{t("oneshot.advancedChanged")}</StatePill>}
                </button>
                <Reveal open={advancedOpen}>
                  <div id={advancedId} className="tw:grid tw:gap-3 tw:pt-1">
                    <RulesSectionsFields
                      sections={draft.rules.sections}
                      onChange={(sections) => source.setDraft({ ...draft, rules: { sections } })}
                      only={configSections}
                      parserKind={parserKind}
                      parserChoice={
                        multipleParsers && parserKind ? (
                          <Field label={t("policy.parser.kindLabel")}>
                            <OneShotParserSelect value={parserKind} options={parserOptions} onChange={changeParser} />
                          </Field>
                        ) : undefined
                      }
                    />
                    {/* Naming is its own block: the empty-facts "jump to naming" lands here. */}
                    <div ref={namingRef} className="tw:rounded-control">
                      <RulesSectionsFields
                        sections={draft.rules.sections}
                        onChange={(sections) => source.setDraft({ ...draft, rules: { sections } })}
                        only={NAMING_ONLY}
                      />
                    </div>
                  </div>
                </Reveal>
              </div>
            )}
          </section>
        </div>
      )}
    </Dialog>
  );
}
