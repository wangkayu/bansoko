import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ApiError,
  getNamingTemplateLibrary,
  previewFileManagerRecognition,
  submitFileManagerRecognition,
  toUserMessage,
  type FileManagerRecognitionBlocker,
  type FileManagerRecognitionConflict,
  type FileManagerRecognitionConflictChoice,
  type FileManagerRecognitionPreview,
  type FileManagerRecognitionRequest,
  type FileManagerRecognitionResult,
  type FileManagerRecognitionRow,
  type AnimeMetadataDraft,
  type NamingRules,
  type RulesSections,
  type TitleSuggestion,
} from "../../lib/api/client";
import { joinList } from "../../lib/i18n/format";
import { t, tryT } from "../../lib/i18n";
import { cn } from "../../lib/utils";
import { configKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, Field, HelpTip, Input } from "../../ui/base";
import { BoundSubtitles, type BoundSubtitleItem } from "../../ui/BoundSubtitles";
import { MetadataFields } from "../anime-forms/MetadataFields";
import { TitleSuggestionBadges } from "../anime-forms/TitleSuggestionBadges";
import { RulesSectionsFields, type SectionKey } from "../anime-forms/RulesSectionsFields";
import { TaskConfigPanel } from "../anime-forms/TaskConfigPanel";
import { TaskSplitLayout, taskPanelColumnClass, taskPreviewColumnClass } from "../anime-forms/TaskSplitLayout";
import type { MetadataProvider } from "../../lib/metadata/types";
import { ParserKindSelect } from "../parser/ParserKindSelect";
import { useParserKindOptions } from "../parser/useParserKindOptions";
import { ConflictDialog } from "./ConflictDialog";
import { useIdempotencyKeys } from "./batch";

// RecognitionDialog — the complex window behind 识别…: the naming core one-shot
// step two renders with, fed by the manual selection instead of a seed
// manifest. The right side is the same editors one-shot uses (parser, then the
// naming templates: an absolute target folder and a filename, with the unknown
// and subtitle variants) plus this operation's optional match; the left side is
// the server's rendered plan, refreshed as the configuration changes. The folder
// the files were selected in only says where they come from; every target is
// rendered by the rules and must land inside the file manager's roots. Nothing
// is written until 应用: conflicts are settled by the skip / version / replace /
// cancel choice, and the submit confirms the plan digest the preview returned. A
// disabled 应用 carries the reason as its tooltip.
const DEBOUNCE_MS = 400;

const EMPTY_NAMING: NamingRules = {
  language: "js-naming-1",
  unknown: { directory_template: "", video_filename_template: "" },
  tv: { directory_template: "", video_filename_template: "" },
  movie: { directory_template: "", video_filename_template: "" },
  subtitle_suffix_template: "",
};
const DEFAULT_RULES: RulesSections = {
  parser: { mode: "replace", value: { episode_patterns: [] } },
  naming: { mode: "replace", value: EMPTY_NAMING },
};
const EMPTY_METADATA: AnimeMetadataDraft = {
  bgm: null,
  tmdb: null,
  media_kind: null,
  total_episodes: null,
  year: null,
  season: null,
};
/** The operation's temporary draft: exactly the values the rendering reads. */
type RecognitionDraft = {
  parserKind: string;
  rules: RulesSections;
  /** The effective title this render names with (anime.title). */
  title: string;
  /** The operation's own anime facts: typed or matched from a metadata provider. */
  metadata: AnimeMetadataDraft;
  episodeText: string;
  offsetText: string;
};
const INITIAL_DRAFT: RecognitionDraft = {
  parserKind: "reltitle",
  rules: DEFAULT_RULES,
  title: "",
  metadata: EMPTY_METADATA,
  episodeText: "",
  offsetText: "0",
};
const PARSER_ONLY: readonly SectionKey[] = ["parser"];
const NAMING_ONLY: readonly SectionKey[] = ["naming"];

export function RecognitionDialog({
  paths,
  singleVideo,
  onClose,
  onFinished,
}: {
  /** Selected files, all in one directory. */
  paths: string[];
  /** Whether the selection holds exactly one video, so a manual episode applies to it. */
  singleVideo: boolean;
  onClose: () => void;
  onFinished: (result: FileManagerRecognitionResult, skippedConflicts: number) => void;
}) {
  const idempotency = useIdempotencyKeys();

  const library = useQuery({ queryKey: configKeys.namingTemplates, queryFn: getNamingTemplateLibrary });
  const defaultTemplate = library.data?.templates.find((item) => item.key === library.data?.default_key) ?? library.data?.templates[0];

  const parserOptions = useParserKindOptions(true, "reltitle", "file_name");
  const multipleParsers = parserOptions.length > 1;

  // The temporary draft of this operation: the values that drive the rendering,
  // and nothing else. It is never written anywhere — the preview request is read
  // from it and the panel edits it in place, the same model as one-shot's task
  // draft, so the two settings columns are used the same way.
  const [draft, setDraft] = useState<RecognitionDraft>(INITIAL_DRAFT);
  const update = (patch: Partial<RecognitionDraft>) => setDraft((current) => ({ ...current, ...patch }));
  const updateMetadata = (patch: Partial<AnimeMetadataDraft>) =>
    setDraft((current) => ({ ...current, metadata: { ...current.metadata, ...patch } }));
  const { parserKind, rules, title, metadata, episodeText, offsetText } = draft;
  const parserRules = rules.parser?.mode === "replace" ? rules.parser.value : { episode_patterns: [] };
  const patterns = parserRules.episode_patterns;
  const naming = rules.naming?.mode === "replace" ? rules.naming.value : EMPTY_NAMING;
  // Every media kind has its own variant, and the one the matched file resolves
  // to decides which renders. An empty variant renders no filename, so the
  // preview is only requested once at least one variant can name a file; a
  // variant left empty simply cannot name that kind of file.
  const namingEmpty = [naming.tv, naming.movie, naming.unknown].every((variant) => variant.video_filename_template.trim() === "");
  // The shared section cards, with hints for this operation's input: file names
  // in one folder, not an RSS title and a folder tree.
  const hints = { parser: t("fileManager.recognize.parserHint"), naming: t("fileManager.recognize.namingHint") };
  const [matchProvider, setMatchProvider] = useState<MetadataProvider | undefined>();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [planChanged, setPlanChanged] = useState(false);
  const [conflictOpen, setConflictOpen] = useState(false);

  // The library's default template seeds the draft once; it is also the state
  // 「恢复默认」 returns to. Afterwards the bodies are this operation's own.
  const baseline = useRef<RulesSections>(DEFAULT_RULES);
  const seeded = useRef(false);
  useEffect(() => {
    if (defaultTemplate && !seeded.current) {
      seeded.current = true;
      const rules: RulesSections = { ...DEFAULT_RULES, naming: { mode: "replace", value: defaultTemplate.rules } };
      baseline.current = rules;
      setDraft((current) => ({ ...current, rules }));
    }
  }, [defaultTemplate]);
  const advancedSummary = joinList([t("forms.rules.parserTitle"), t("forms.rules.namingTitle")]);
  const advancedChanged =
    parserKind !== INITIAL_DRAFT.parserKind ||
    JSON.stringify([rules.parser, rules.naming]) !== JSON.stringify([baseline.current.parser, baseline.current.naming]);
  function restoreDefaults() {
    setDraft({ ...INITIAL_DRAFT, rules: baseline.current });
  }

  const episode = wholeNumber(episodeText, 1, 99999);
  const offset = wholeNumber(offsetText, -99999, 99999);
  // A number the user started typing must be whole before it is rendered, so a
  // half-typed value never silently becomes zero or drops a field.
  const numberInvalid = (episodeText.trim() !== "" && episode === null) || (offsetText.trim() !== "" && offset === null);
  // An empty title is not an error: without one the request carries no match and
  // the render is a pure parse of the file names, exactly like one-shot's fields
  // left untouched.

  const inputs = {
    parserKind,
    patterns,
    naming,
    title,
    metadata,
    episodeText,
    offsetText,
  };
  const debounced = useDebounced(inputs, DEBOUNCE_MS);
  const settled = Object.keys(debounced).every((key) => debounced[key as keyof typeof debounced] === inputs[key as keyof typeof inputs]);

  const request = useMemo<FileManagerRecognitionRequest | null>(() => {
    if (namingEmpty) return null;
    const d = debounced;
    const dEpisode = wholeNumber(d.episodeText, 1, 99999);
    const dOffset = wholeNumber(d.offsetText, -99999, 99999);
    if ((d.episodeText.trim() !== "" && dEpisode === null) || (d.offsetText.trim() !== "" && dOffset === null)) {
      return null;
    }
    const dTitle = d.title.trim();
    const dMediaKind = d.metadata.media_kind ?? d.metadata.tmdb?.media_kind ?? "tv";
    const dEpisodeWanted = singleVideo && dEpisode !== null;
    const dOffsetValue = dOffset ?? 0;
    // A match is sent once it carries anything: the draft (which needs a title),
    // a chosen episode or a non-zero offset; with none of them the render is a
    // pure parse of the file names.
    const hasMatch = dTitle !== "" || dEpisodeWanted || dOffsetValue !== 0;
    return {
      paths,
      parser:
        d.parserKind === "regex"
          ? { id: "regex", ...(d.patterns.length > 0 ? { config: { episode_patterns: d.patterns } } : {}) }
          : { id: "reltitle" },
      naming: d.naming,
      ...(hasMatch
        ? {
            match: {
              ...(dTitle !== "" && d.metadata.season != null ? { season: d.metadata.season } : {}),
              ...(dEpisodeWanted ? { episode: dEpisode } : {}),
              offset: dOffsetValue,
              // The render reads these facts, never a re-read of a stored anime.
              ...(dTitle !== ""
                ? {
                    anime: {
                      title: dTitle,
                      media_kind: dMediaKind,
                      year: d.metadata.year ?? null,
                      bgm: d.metadata.bgm ?? null,
                      tmdb: d.metadata.tmdb ?? null,
                    },
                  }
                : {}),
            },
          }
        : {}),
    };
    // The paths are fixed for this dialog; the debounced inputs are the request.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced, paths, singleVideo]);

  const preview = useQuery({
    queryKey: ["file-manager", "recognition-preview", JSON.stringify(request)],
    queryFn: () => previewFileManagerRecognition(request!),
    enabled: request !== null && settled,
    retry: false,
    staleTime: 0,
    gcTime: 0,
    placeholderData: (previous: FileManagerRecognitionPreview | undefined) => previous,
  });
  // The previous plan stays visible while the debounced inputs are updating;
  // 应用 stays closed until the preview for the current inputs has landed.
  const data = preview.data;
  const rows = data?.rows ?? [];
  const hasBlockers = rows.some((row) => row.disposition === "error");
  const blockedCount = rows.filter((row) => row.disposition === "error").length;
  const actionable = rows.filter((row) => row.disposition === "rename" || row.disposition === "move").length;
  const unknownCount = rows.filter((row) => row.disposition === "skip").length;
  const unknownRoute = data?.unknown_route;
  const conflicts = data?.conflicts ?? [];
  // A conflicting target is replaceable only when it is a plain file on disk:
  // a managed, in-progress or duplicated target refuses the whole batch.
  const conflictsOverwritable = rows.every((row) => row.conflict?.occupied_by === undefined || row.conflict.occupied_by === "file");
  const canApply =
    Boolean(data) &&
    settled &&
    !preview.isFetching &&
    !preview.isError &&
    !numberInvalid &&
    !hasBlockers &&
    actionable > 0 &&
    !busy;
  // Why 应用 cannot be clicked right now: the first failed gate of canApply,
  // shown as the button's tooltip so a blocked submit is observable instead of
  // silently disabled. busy shows the spinner, so it stays untipped.
  let applyDisabledReason: string | undefined;
  if (!busy) {
    if (namingEmpty) applyDisabledReason = t("fileManager.recognize.preview.emptyTemplate");
    else if (numberInvalid) applyDisabledReason = t("fileManager.recognize.invalidNumber");
    else if (!data) applyDisabledReason = t("fileManager.recognize.preview.loading");
    else if (preview.isError) applyDisabledReason = t("fileManager.recognize.preview.failed");
    else if (!settled || preview.isFetching) applyDisabledReason = t("fileManager.recognize.preview.loading");
    else if (hasBlockers) applyDisabledReason = t("fileManager.recognize.applyDisabled.blockers", { n: blockedCount });
    else if (actionable === 0) applyDisabledReason = t("fileManager.recognize.applyDisabled.nothing");
  }

  // The title candidate a search one-shot reads from its seeds' parsed titles,
  // here from the parser's read of the first file's own name: a badge by the
  // title input and the metadata search's first query.
  const titleHint = data?.title_hint?.trim() ?? "";
  const titleSuggestions: TitleSuggestion[] = titleHint !== "" ? [{ source: "provider", text: titleHint }] : [];
  const matchQueryCandidates = titleHint !== "" ? [titleHint] : undefined;

  async function apply(choice: FileManagerRecognitionConflictChoice) {
    if (!data || !request || busy) return;
    setBusy(true);
    setError(null);
    setPlanChanged(false);
    const body = { ...request, expected_plan_digest: data.plan_digest, conflict: choice };
    try {
      const result = await submitFileManagerRecognition(body, idempotency(JSON.stringify(body)));
      onFinished(result, choice === "skip" ? skippedByConflicts(data.rows, data.conflicts) : 0);
    } catch (cause) {
      if (cause instanceof ApiError && cause.code === "FILE_MANAGER_RECOGNITION_PLAN_CHANGED") {
        setPlanChanged(true);
        void preview.refetch();
      } else {
        setError(toUserMessage(cause, t("common.operationFailed")));
      }
      setBusy(false);
      setConflictOpen(false);
    }
  }

  return (
    <>
      <Dialog
        open
        tier="L"
        title={t("fileManager.recognize.title")}
        description={t("fileManager.recognize.sub", { n: paths.length })}
        onRequestClose={busy ? () => {} : onClose}
        closeDisabled={busy}
        bodyLayout="fill"
        footer={
          <>
            <span className="tw:mr-auto tw:min-w-0 tw:text-xs tw:text-muted-foreground">
              {t("fileManager.recognize.willProcess", { n: actionable })}
              {unknownCount > 0 ? ` · ${t("fileManager.recognize.unknownShort", { n: unknownCount })}` : ""}
            </span>
            <Button onClick={onClose} disabled={busy}>
              {t("act.cancel")}
            </Button>
            <Button
              variant="primary"
              loading={busy}
              disabled={!canApply}
              title={applyDisabledReason}
              onClick={() => (conflicts.length > 0 ? setConflictOpen(true) : void apply("block"))}
            >
              {t("fileManager.recognize.apply")}
            </Button>
          </>
        }
      >
        {/* The shared split shell one-shot step two uses: the plan stays in
            place while the settings column is read and edited. */}
        <TaskSplitLayout>
          <section
            aria-label={t("fileManager.recognize.preview.heading")}
            className={taskPreviewColumnClass}
          >
            <h3 className="text-body tw:m-0">
              {t("fileManager.recognize.preview.heading")}
              <span className="tw:ml-2 tw:text-xs tw:font-normal tw:text-muted-foreground">{t("fileManager.recognize.preview.sub")}</span>
            </h3>
            {/* Where the selected files were read from. Targets are rendered by
                the naming rules and are shown absolute on each row. */}
            {data ? (
              <p className="text-caption break-anywhere tw:m-0 tw:text-muted-foreground" title={data.directory}>
                {t("fileManager.recognize.preview.folder", { path: data.directory })}
              </p>
            ) : null}
            {namingEmpty ? (
              <Alert tone="warning" title={t("fileManager.recognize.preview.emptyTemplate")} />
            ) : numberInvalid ? (
              <Alert tone="warning" title={t("fileManager.recognize.invalidNumber")} />
            ) : !data && preview.isFetching ? (
              <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.recognize.preview.loading")}</p>
            ) : rows.length === 0 ? null : (
              <RecognitionPreview rows={rows} />
            )}
            {preview.isError && settled ? (
              <Alert tone="error" title={toUserMessage(preview.error, t("fileManager.recognize.preview.failed"))} />
            ) : null}
            {data && unknownCount > 0 ? (
              <Alert
                tone="info"
                title={t("fileManager.recognize.unknownNote", { n: unknownCount })}
                description={t(unknownRoute === "route" ? "fileManager.recognize.unknownRouteText" : "fileManager.recognize.unknownSkipText")}
              />
            ) : null}
            {data && conflicts.length > 0 ? (
              <Alert tone="warning" title={t("fileManager.recognize.conflictTitle", { n: conflicts.length })} />
            ) : null}
            {planChanged ? <Alert tone="warning" title={t("fileManager.recognize.planChanged")} /> : null}
            {error ? <Alert tone="error" title={error} /> : null}
          </section>

          <TaskConfigPanel
            ariaLabel={t("fileManager.recognize.configAria")}
            title={t("fileManager.recognize.configTitle")}
            note={t("fileManager.recognize.intro")}
            className={taskPanelColumnClass}
            actions={
              <>
                <HelpTip label={t("fileManager.recognize.restoreDefaultsHelpLabel")}>{t("fileManager.recognize.restoreDefaultsHint")}</HelpTip>
                <Button size="sm" variant="secondary" disabled={busy} onClick={restoreDefaults}>
                  {t("fileManager.recognize.restoreDefaults")}
                </Button>
              </>
            }
            advanced={{
              open: advancedOpen,
              onToggle: () => setAdvancedOpen((value) => !value),
              label: t("fileManager.recognize.advanced"),
              summary: advancedSummary,
              changed: advancedChanged,
              changedLabel: t("fileManager.recognize.advancedChanged"),
              children: (
                <>
                  <RulesSectionsFields
                    sections={rules}
                    onChange={(next) => update({ rules: next })}
                    only={PARSER_ONLY}
                    parserKind={parserKind}
                    hints={hints}
                    parserChoice={
                      multipleParsers ? (
                        <Field label={t("policy.parser.kindLabel")}>
                          <ParserKindSelect value={parserKind} options={parserOptions} onChange={(kind) => update({ parserKind: kind })} />
                        </Field>
                      ) : undefined
                    }
                  />
                  <RulesSectionsFields sections={rules} onChange={(next) => update({ rules: next })} only={NAMING_ONLY} />
                </>
              ),
            }}
          >
            {/* The same flat field column one-shot renders: the title with the
                parser's candidate as a badge, the metadata rows in one-shot's
                read/edit style, then the episode inputs. Total episodes play no
                part in a rename, so the row is not offered here. */}
            <div className="tw:grid tw:grid-cols-1 tw:gap-2">
              <Field label={t("field.title")}>
                <Input
                  id="recognize-title"
                  aria-label={t("field.title")}
                  value={title}
                  onChange={(event) => update({ title: event.target.value })}
                  autoComplete="off"
                  disabled={busy}
                />
              </Field>
              {/* The parser's read of the first file's name is the candidate, the
                  way a search one-shot's seeds carry their parsed titles. */}
              <TitleSuggestionBadges suggestions={titleSuggestions} current={title} onPick={(next) => update({ title: next })} />
              <MetadataFields
                idPrefix="recognize"
                showYear
                showSeason
                title={title}
                matchQueryCandidates={matchQueryCandidates}
                onMatchTitle={(matched) =>
                  // Only fills an empty title; a title the user already has is never overwritten.
                  setDraft((current) => (current.title.trim() === "" ? { ...current, title: matched } : current))
                }
                value={metadata}
                onChange={(next, kind) =>
                  setDraft((current) => ({
                    ...current,
                    metadata: kind ? { ...next, media_kind: kind } : next,
                  }))
                }
                matchProvider={matchProvider}
                onMatchProviderChange={setMatchProvider}
                onSaveMediaKind={async (next) => {
                  updateMetadata({ media_kind: next });
                  return true;
                }}
                mediaKind={metadata.media_kind ?? null}
              />
            </div>
            {singleVideo ? (
              <div className="tw:grid tw:gap-1.5">
                <Field label={t("fileManager.recognize.match.episode")}>
                  <Input
                    inputMode="numeric"
                    type="number"
                    min={1}
                    max={99999}
                    step="1"
                    aria-label={t("fileManager.recognize.match.episode")}
                    value={episodeText}
                    onChange={(event) => update({ episodeText: event.target.value })}
                    aria-invalid={episodeText !== "" && episode === null ? true : undefined}
                    autoComplete="off"
                    disabled={busy}
                  />
                </Field>
              </div>
            ) : null}
            <div className="tw:grid tw:gap-1.5">
              <Field label={t("fileManager.recognize.match.offset")}>
                <Input
                  inputMode="numeric"
                  type="number"
                  min={-99999}
                  max={99999}
                  step="1"
                  aria-label={t("fileManager.recognize.match.offset")}
                  value={offsetText}
                  onChange={(event) => update({ offsetText: event.target.value })}
                  aria-invalid={offset !== null ? undefined : true}
                  autoComplete="off"
                  disabled={busy}
                />
              </Field>
            </div>
            <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.recognize.applyHint")}</p>
          </TaskConfigPanel>
        </TaskSplitLayout>
      </Dialog>
      {conflictOpen && data ? (
        <ConflictDialog
          targets={conflicts}
          busy={busy}
          onCancel={() => setConflictOpen(false)}
          onSkip={() => void apply("skip")}
          onVersion={() => void apply("version")}
          onOverwrite={conflictsOverwritable ? () => void apply("overwrite") : undefined}
        />
      ) : null}
    </>
  );
}

type PreviewGroup = {
  key: string;
  video?: FileManagerRecognitionRow;
  subtitles: FileManagerRecognitionRow[];
  others: FileManagerRecognitionRow[];
};

/**
 * The plan in the one-shot preview's shape: one entry per source group — the
 * source path, the full rendered target under it, and the subtitles the video
 * carries as format badges on its row. The group is the unit the naming core
 * works in, so a subtitle can never look like an independent rename that
 * happens to sit next to its video.
 */
function RecognitionPreview({ rows }: { rows: readonly FileManagerRecognitionRow[] }) {
  return (
    <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1.5 tw:p-0">
      {groupRecognitionRows(rows).map((group) => {
        const videoIncluded = group.video !== undefined && isActionable(group.video);
        // A subtitle follows its video: it is named with it, or, when the video
        // is not processed, leaves with it. Only a subtitle that fails on its own
        // while its video is processed needs a row of its own.
        const bound = group.video
          ? group.subtitles.filter((row) => (videoIncluded ? isActionable(row) : true))
          : [];
        const failed = group.video ? group.subtitles.filter((row) => videoIncluded && !isActionable(row)) : [];
        return (
          <li key={group.key} className="tw:grid tw:gap-1">
            {group.video ? (
              <VideoRow row={group.video} subtitles={bound} />
            ) : (
              group.subtitles.map((row) => <RecognitionFileRow key={row.path} row={row} />)
            )}
            {failed.map((row) => <RecognitionFileRow key={row.path} row={row} />)}
            {group.others.map((row) => <RecognitionFileRow key={row.path} row={row} />)}
          </li>
        );
      })}
    </ul>
  );
}

function groupRecognitionRows(rows: readonly FileManagerRecognitionRow[]): PreviewGroup[] {
  const groups = new Map<string, PreviewGroup>();
  for (const row of rows) {
    const key = row.group || row.path;
    let group = groups.get(key);
    if (!group) {
      group = { key, subtitles: [], others: [] };
      groups.set(key, group);
    }
    if (row.kind === "video" && !group.video) group.video = row;
    else if (row.kind === "subtitle") group.subtitles.push(row);
    else group.others.push(row);
  }
  return [...groups.values()];
}

function isActionable(row: FileManagerRecognitionRow): boolean {
  return row.disposition === "rename" || row.disposition === "move";
}

/** A video's row: its source, its rendered target, and the subtitles bound to it. */
function VideoRow({ row, subtitles }: { row: FileManagerRecognitionRow; subtitles: readonly FileManagerRecognitionRow[] }) {
  return (
    <div className="tw:grid tw:gap-0.5 tw:rounded-md tw:bg-muted/15 tw:px-2.5 tw:py-2">
      <SourcePath row={row} />
      <TargetLine row={row} />
      <RecognitionMeta row={row} />
      <BlockerLines row={row} />
      <BoundSubtitles
        videoName={row.path}
        subtitles={subtitles.map(subtitleItem)}
        videoExcluded={!isActionable(row)}
        showLanguage={false}
      />
    </div>
  );
}

/** A file that is not carried by a video in the selection (or failed on its own). */
function RecognitionFileRow({ row }: { row: FileManagerRecognitionRow }) {
  return (
    <div className="tw:grid tw:gap-0.5 tw:rounded-md tw:bg-muted/15 tw:px-2.5 tw:py-2">
      <SourcePath row={row} />
      <TargetLine row={row} />
      <RecognitionMeta row={row} />
      <BlockerLines row={row} />
    </div>
  );
}

function SourcePath({ row }: { row: FileManagerRecognitionRow }) {
  return (
    <p className="text-caption break-anywhere tw:m-0 tw:text-muted-foreground" title={row.path}>
      {row.path}
    </p>
  );
}

function TargetLine({ row }: { row: FileManagerRecognitionRow }) {
  const target = row.target_path ?? row.target_name;
  return (
    <p
      className={cn("break-anywhere tw:m-0 tw:text-sm", row.disposition === "error" && "tw:text-destructive")}
      title={target}
    >
      {target ?? <span className="text-caption tw:text-muted-foreground">{t(`fileManager.recognize.preview.disposition.${row.disposition}`)}</span>}
    </p>
  );
}

function RecognitionMeta({ row }: { row: FileManagerRecognitionRow }) {
  const episode = row.episode;
  if (!episode && !row.conflict) return null;
  return (
    <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-x-3 tw:gap-y-1 tw:text-xs tw:text-muted-foreground">
      {episode ? (
        <span title={episode.raw ? t("fileManager.recognize.preview.episodeRaw", { raw: episode.raw }) : undefined}>
          {t("fileManager.recognize.preview.episode", { n: episode.number })}
          {episode.number_end ? `-${episode.number_end}` : ""}
          {episode.offset !== 0 ? ` ${t("fileManager.recognize.preview.episodeOffset", { n: episode.offset })}` : ""}
          {episode.source === "manual" ? ` · ${t("fileManager.recognize.preview.episodeManual")}` : ""}
        </span>
      ) : null}
      {row.conflict ? <span className="tw:text-warning">{conflictText(row.conflict)}</span> : null}
    </div>
  );
}

function BlockerLines({ row }: { row: FileManagerRecognitionRow }) {
  if (!row.blockers?.length) return null;
  return (
    <>
      {row.blockers.map((blocker, index) => (
        <p key={index} className="tw:m-0 tw:text-xs tw:text-destructive">
          {blockerText(blocker)}
        </p>
      ))}
    </>
  );
}

/** One bound subtitle in the shared badge form; its language is not part of the recognition preview. */
function subtitleItem(row: FileManagerRecognitionRow): BoundSubtitleItem {
  return {
    key: row.path,
    path: row.path,
    included: isActionable(row),
    target: row.target_path ?? row.target_name ?? null,
    targetNote: row.conflict ? conflictText(row.conflict) : undefined,
  };
}

function conflictText(conflict: FileManagerRecognitionConflict): string {
  const reason = t(`fileManager.recognize.preview.conflict.${conflict.occupied_by}`);
  return conflict.version_path ? `${reason} · ${t("fileManager.recognize.preview.conflictVersionPath", { path: conflict.version_path })}` : reason;
}

function blockerText(blocker: FileManagerRecognitionBlocker): string {
  const facts = blocker.facts ?? [];
  if (blocker.code === "facts_missing") {
    return facts.length > 0
      ? t("fileManager.recognize.preview.blocker.facts_missing", { facts: joinList(facts.map(factLabel)) })
      : t("fileManager.recognize.preview.blocker.facts_missing_unnamed");
  }
  return t(`fileManager.recognize.preview.blocker.${blocker.code}`);
}

function factLabel(path: string): string {
  return tryT(`facts.${path}.label`) ?? path;
}

/** The files a skip leaves alone: each conflicting row plus the subtitles a conflicting video carries. */
function skippedByConflicts(rows: readonly FileManagerRecognitionRow[], conflicts: readonly string[]): number {
  const conflictingTargets = new Set(conflicts);
  const blockedVideoGroups = new Set(
    rows
      .filter((row) => row.kind === "video" && row.target_path && conflictingTargets.has(row.target_path))
      .map((row) => row.group),
  );
  const dropped = new Set(
    rows
      .filter(
        (row) =>
          (row.target_path !== undefined && conflictingTargets.has(row.target_path)) ||
          (row.kind === "subtitle" && blockedVideoGroups.has(row.group)),
      )
      .map((row) => row.path),
  );
  return dropped.size;
}

function wholeNumber(text: string, min: number, max: number): number | null {
  const trimmed = text.trim();
  if (trimmed === "") return null;
  if (!/^-?\d{1,5}$/.test(trimmed)) return null;
  const value = Number(trimmed);
  return value >= min && value <= max ? value : null;
}

function useDebounced<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}
