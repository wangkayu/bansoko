import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import {
  ApiError,
  getNamingTemplateLibrary,
  previewFileManagerRecognition,
  submitFileManagerRecognition,
  toUserMessage,
  type FileManagerRecognitionBlocker,
  type FileManagerRecognitionConflictChoice,
  type FileManagerRecognitionMediaDiagnostic,
  type FileManagerRecognitionPreview,
  type FileManagerRecognitionRequest,
  type FileManagerRecognitionResult,
  type FileManagerRecognitionRow,
  type FileManagerRecognitionTitleDiagnostic,
  type FileManagerRecognitionTitleProvider,
  type FileManagerRecognitionWorkKey,
  type AnimeMetadataDraft,
  type NamingRules,
  type RulesSections,
  type TitleSuggestion,
  type UpstreamStatus,
} from "../../lib/api/client";
import { fileUnitsSummary } from "../../lib/boundSubtitles";
import { joinList } from "../../lib/i18n/format";
import { t } from "../../lib/i18n";
import { formatBytes } from "../../lib/labels";
import { configKeys } from "../../lib/queryKeys";
import { cn } from "../../lib/utils";
import { Button, Dialog, Field, HelpTip, Input, SpinnerIcon, StateBlock } from "../../ui/base";
import { BoundSubtitles, type BoundSubtitleItem } from "../../ui/BoundSubtitles";
import { TargetPathText } from "../../ui/TargetPathText";
import { ThemedToaster } from "../../ui/ThemedToaster";
import { toast } from "../../ui/toast";
import { useUpstreamWait } from "../../ui/UpstreamStatusBlock";
import { MetadataFields } from "../anime-forms/MetadataFields";
import { factLabels, firstFactField, inlineFactLinkClass, MissingFactsNote, useFactJump } from "../anime-forms/MissingFactsNote";
import { TitleSuggestionBadges } from "../anime-forms/TitleSuggestionBadges";
import { RulesSectionsFields, type SectionKey } from "../anime-forms/RulesSectionsFields";
import { TaskConfigPanel } from "../anime-forms/TaskConfigPanel";
import { TaskSplitLayout, taskPanelColumnClass, taskPreviewColumnClass } from "../anime-forms/TaskSplitLayout";
import { shortTarget, type FactField } from "../oneshot/seedRows";
import type { MetadataProvider } from "../../lib/metadata/types";
import { ParserKindSelect } from "../parser/ParserKindSelect";
import { useParserKindOptions } from "../parser/useParserKindOptions";
import { ConflictDialog } from "./ConflictDialog";
import { FileNumberingDialog } from "../file-numbering/FileNumberingDialog";
import { parseEpisodeInput, sanitizeOverrides, type FileEpisodeOverride, type FileNumberingItem } from "../file-numbering/model";
import { useIdempotencyKeys } from "./batch";

// RecognitionDialog — file-manager recognition aligned with OneShot step two:
// MediaInfo and referenced episode titles resolve automatically on demand via
// the backend's selective-placeholder preview, with a permanent full Reload
// action and session-scoped failure suppression (`episode_title_cache_only`) so
// offline/unconfigured edits stay instantaneous. Unresolved placeholders render
// inline with rose error-fallback highlighting (`TargetPathText tone="error"`)
// while outcomes are announced via toast + a single-line summary tail.
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

type RecognitionDraft = {
  parserKind: string;
  rules: RulesSections;
  title: string;
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

type TitleFailureReason = Exclude<NonNullable<FileManagerRecognitionTitleDiagnostic["reason"]>, "cache_miss" | "not_loaded">;

type RememberedTitleFailure = {
  key: FileManagerRecognitionWorkKey;
  reason: TitleFailureReason;
  retryAfterSeconds?: number;
  receivedAtMs: number;
};

type EffectiveTitleIssue = {
  id: string;
  provider: FileManagerRecognitionTitleProvider;
  workId: string;
  season: number;
  reason: TitleFailureReason;
  retryAfterSeconds?: number;
  receivedAtMs: number;
  fromCacheSuppression: boolean;
};

function workKeyString(key: FileManagerRecognitionWorkKey): string {
  return `${key.provider}:${key.work_id}:${key.season}`;
}

function isLiveTitleFailureReason(reason: FileManagerRecognitionTitleDiagnostic["reason"]): reason is TitleFailureReason {
  return (
    reason === "unconfigured" ||
    reason === "credentials_rejected" ||
    reason === "rate_limited" ||
    reason === "unreachable" ||
    reason === "invalid_response"
  );
}

function providerDisplayName(provider: FileManagerRecognitionTitleProvider): string {
  return t(provider === "bgm" ? "fileManager.recognize.runtimeIssue.providerBgm" : "fileManager.recognize.runtimeIssue.providerTmdb");
}

function titleIssueReasonText(provider: FileManagerRecognitionTitleProvider, reason: TitleFailureReason): string {
  const name = providerDisplayName(provider);
  switch (reason) {
    case "unconfigured":
      return t("fileManager.recognize.runtimeIssue.unconfigured", { provider: name });
    case "credentials_rejected":
      return t("fileManager.recognize.runtimeIssue.credentialsRejected", { provider: name });
    case "rate_limited":
      return t("fileManager.recognize.runtimeIssue.rateLimited", { provider: name });
    case "unreachable":
      return t("fileManager.recognize.runtimeIssue.unreachable", { provider: name });
    case "invalid_response":
      return t("fileManager.recognize.runtimeIssue.invalidResponse", { provider: name });
  }
}

export function RecognitionDialog({
  paths,
  singleVideo,
  onOpenConnectionSettings,
  onClose,
  onFinished,
}: {
  /** Selected files, all in one directory. */
  paths: string[];
  /** Whether the selection holds exactly one video, so a manual episode applies to it. */
  singleVideo: boolean;
  /** Optional deep-link into connection settings for unconfigured/rejected metadata providers. */
  onOpenConnectionSettings?: (provider: FileManagerRecognitionTitleProvider) => void;
  onClose: () => void;
  onFinished: (result: FileManagerRecognitionResult, skippedConflicts: number) => void;
}) {
  const idempotency = useIdempotencyKeys();

  const library = useQuery({ queryKey: configKeys.namingTemplates, queryFn: getNamingTemplateLibrary });
  const defaultTemplate = library.data?.templates.find((item) => item.key === library.data?.default_key) ?? library.data?.templates[0];

  const parserOptions = useParserKindOptions(true, "reltitle", "file_name");
  const multipleParsers = parserOptions.length > 1;

  const [draft, setDraft] = useState<RecognitionDraft>(INITIAL_DRAFT);
  const update = (patch: Partial<RecognitionDraft>) => setDraft((current) => ({ ...current, ...patch }));
  const updateMetadata = (patch: Partial<AnimeMetadataDraft>) =>
    setDraft((current) => ({ ...current, metadata: { ...current.metadata, ...patch } }));
  const { parserKind, rules, title, metadata, episodeText, offsetText } = draft;
  const parserRules = rules.parser?.mode === "replace" ? rules.parser.value : { episode_patterns: [] };
  const patterns = parserRules.episode_patterns;
  const naming = rules.naming?.mode === "replace" ? rules.naming.value : EMPTY_NAMING;
  const namingEmpty = [naming.tv, naming.movie, naming.unknown].every((variant) => variant.video_filename_template.trim() === "");
  const hints = { parser: t("fileManager.recognize.parserHint"), naming: t("fileManager.recognize.namingHint") };

  const [matchProvider, setMatchProvider] = useState<MetadataProvider | undefined>();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [conflictOpen, setConflictOpen] = useState(false);
  const [numberingOpen, setNumberingOpen] = useState(false);
  const [overrides, setOverrides] = useState<FileEpisodeOverride[]>([]);

  // Explicit full reload controls: a click clears session-suppressed failure
  // keys, sets the one-shot refresh_media ref, and bumps reloadEpoch.
  const [reloadEpoch, setReloadEpoch] = useState(0);
  const pendingRefreshMediaRef = useRef(false);
  const explicitReloadPendingRef = useRef(false);
  const [reloadingExplicit, setReloadingExplicit] = useState(false);
  const failedWorkKeysRef = useRef<Map<string, RememberedTitleFailure>>(new Map());
  const [rememberedFailures, setRememberedFailures] = useState<ReadonlyMap<string, RememberedTitleFailure>>(() => new Map());
  const lastToastedSignatureRef = useRef<string>("");

  const settingsRef = useRef<HTMLElement>(null);
  const previewRef = useRef<HTMLElement>(null);
  const namingRef = useRef<HTMLDivElement>(null);
  const jumpTo = useFactJump({
    idPrefix: "recognize",
    settingsRef,
    namingRef,
    onOpenAdvanced: () => setAdvancedOpen(true),
  });

  const baseline = useRef<RulesSections>(DEFAULT_RULES);
  const seeded = useRef(false);
  useEffect(() => {
    if (defaultTemplate && !seeded.current) {
      seeded.current = true;
      const seededRules: RulesSections = { ...DEFAULT_RULES, naming: { mode: "replace", value: defaultTemplate.rules } };
      baseline.current = seededRules;
      setDraft((current) => ({ ...current, rules: seededRules }));
    }
  }, [defaultTemplate]);

  const advancedSummary = joinList([t("forms.rules.parserTitle"), t("forms.rules.namingTitle")]);
  const advancedChanged =
    parserKind !== INITIAL_DRAFT.parserKind ||
    JSON.stringify([rules.parser, rules.naming]) !== JSON.stringify([baseline.current.parser, baseline.current.naming]);

  function restoreDefaults() {
    setDraft({ ...INITIAL_DRAFT, rules: baseline.current });
    setOverrides([]);
  }

  const parsedEpisode = episodeText.trim() === "" ? null : parseEpisodeInput(episodeText);
  const offset = wholeNumber(offsetText, -99999, 99999);
  const numberInvalid = (episodeText.trim() !== "" && parsedEpisode === null) || (offsetText.trim() !== "" && offset === null);

  const inputs = {
    parserKind,
    patterns,
    naming,
    title,
    metadata,
    episodeText,
    offsetText,
    overrides,
  };
  const debounced = useDebounced(inputs, DEBOUNCE_MS);
  const settled = Object.keys(debounced).every((key) => debounced[key as keyof typeof debounced] === inputs[key as keyof typeof inputs]);

  const baseRequest = useMemo<FileManagerRecognitionRequest | null>(() => {
    if (namingEmpty) return null;
    const d = debounced;
    const dParsed = d.episodeText.trim() === "" ? null : parseEpisodeInput(d.episodeText);
    const dOffset = wholeNumber(d.offsetText, -99999, 99999);
    if ((d.episodeText.trim() !== "" && dParsed === null) || (d.offsetText.trim() !== "" && dOffset === null)) {
      return null;
    }
    const dTitle = d.title.trim();
    const dMediaKind = d.metadata.media_kind ?? d.metadata.tmdb?.media_kind ?? "tv";
    const dEpisodeWanted = singleVideo && dParsed !== null;
    const dOffsetValue = dOffset ?? 0;
    const hasMatch = dTitle !== "" || dEpisodeWanted || dOffsetValue !== 0;
    return {
      paths,
      parser:
        d.parserKind === "regex"
          ? { id: "regex", ...(d.patterns.length > 0 ? { config: { episode_patterns: d.patterns } } : {}) }
          : { id: "reltitle" },
      naming: d.naming,
      ...(d.overrides.length > 0 ? { overrides: d.overrides } : {}),
      ...(hasMatch
        ? {
            match: {
              ...(dTitle !== "" && d.metadata.season != null ? { season: d.metadata.season } : {}),
              ...(dEpisodeWanted && dParsed
                ? { episode: dParsed.episode, ...(dParsed.episode_end === undefined ? {} : { episode_end: dParsed.episode_end }) }
                : {}),
              offset: dOffsetValue,
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
  }, [debounced, namingEmpty, paths, singleVideo]);

  const requestKey = useMemo(() => (baseRequest ? JSON.stringify(baseRequest) : null), [baseRequest]);

  const preview = useQuery({
    queryKey: ["file-manager", "recognition-preview", requestKey, reloadEpoch],
    queryFn: async () => {
      const refreshMedia = pendingRefreshMediaRef.current;
      pendingRefreshMediaRef.current = false;
      const cacheOnly = [...failedWorkKeysRef.current.values()].map((entry) => entry.key);
      const body: FileManagerRecognitionRequest = {
        ...baseRequest!,
        ...(refreshMedia ? { refresh_media: true } : {}),
        ...(cacheOnly.length > 0 ? { episode_title_cache_only: cacheOnly } : {}),
      };
      return previewFileManagerRecognition(body);
    },
    enabled: baseRequest !== null && settled,
    retry: false,
    staleTime: 0,
    gcTime: 0,
    placeholderData: (previous: FileManagerRecognitionPreview | undefined) => previous,
  });

  function triggerFullReload() {
    if (busy || namingEmpty || numberInvalid || preview.isFetching) return;
    failedWorkKeysRef.current = new Map();
    setRememberedFailures(new Map());
    pendingRefreshMediaRef.current = true;
    explicitReloadPendingRef.current = true;
    setReloadingExplicit(true);
    setReloadEpoch((n) => n + 1);
  }
  const triggerFullReloadRef = useRef(triggerFullReload);
  triggerFullReloadRef.current = triggerFullReload;

  // Reconcile runtime diagnostics into session failure suppression & toast notifications.
  const data = preview.data;
  const responseTick = preview.dataUpdatedAt;
  useEffect(() => {
    if (!data || preview.isFetching) return;
    const wasExplicitReload = explicitReloadPendingRef.current;
    explicitReloadPendingRef.current = false;
    setReloadingExplicit(false);

    const now = Date.now();
    const nextFailed = new Map(failedWorkKeysRef.current);
    let mapChanged = false;
    const freshTitleIssues: EffectiveTitleIssue[] = [];

    for (const diag of data.runtime_facts?.episode_titles ?? []) {
      const key: FileManagerRecognitionWorkKey = { provider: diag.provider, work_id: diag.work_id, season: diag.season };
      const id = workKeyString(key);
      if (diag.state === "known" || diag.state === "absent") {
        if (nextFailed.delete(id)) mapChanged = true;
        continue;
      }
      if (isLiveTitleFailureReason(diag.reason)) {
        const record: RememberedTitleFailure = {
          key,
          reason: diag.reason,
          retryAfterSeconds: diag.retry_after_seconds,
          receivedAtMs: now,
        };
        nextFailed.set(id, record);
        mapChanged = true;
        freshTitleIssues.push({
          id: diag.id,
          provider: diag.provider,
          workId: diag.work_id,
          season: diag.season,
          reason: diag.reason,
          retryAfterSeconds: diag.retry_after_seconds,
          receivedAtMs: now,
          fromCacheSuppression: false,
        });
      }
    }

    if (mapChanged) {
      failedWorkKeysRef.current = nextFailed;
      setRememberedFailures(new Map(nextFailed));
    }

    const mediaFailures = (data.runtime_facts?.media_probe ?? []).filter((d) => d.state === "unavailable");
    const sig = JSON.stringify([
      freshTitleIssues.map((i) => `${i.provider}:${i.workId}:${i.season}:${i.reason}`),
      mediaFailures.map((m) => `${m.path}:${m.reason ?? ""}`),
    ]);

    if (freshTitleIssues.length === 0 && mediaFailures.length === 0) {
      if (wasExplicitReload) {
        toast.success(t("fileManager.recognize.reloaded"));
      }
      lastToastedSignatureRef.current = "";
      return;
    }

    if (!wasExplicitReload && sig === lastToastedSignatureRef.current) return;
    lastToastedSignatureRef.current = sig;

    // Announce runtime fallback reasons cleanly via toast (at most one media toast + one title toast,
    // with the title toast emitted last so actionable config/rate-limit notices sit on top).
    if (mediaFailures.length > 0) {
      toast.warning(t("fileManager.recognize.runtimeIssue.mediaFailed", { n: mediaFailures.length }), {
        action: { label: t("fileManager.recognize.reload"), onClick: () => triggerFullReloadRef.current() },
      });
    }

    const firstTitle = freshTitleIssues[0];
    if (firstTitle) {
      const reasonMsg = titleIssueReasonText(firstTitle.provider, firstTitle.reason);
      if (firstTitle.reason === "unconfigured" || firstTitle.reason === "credentials_rejected") {
        toast.warning(t("fileManager.recognize.runtimeIssue.titleFallbackToast", { reason: reasonMsg }), {
          ...(onOpenConnectionSettings
            ? {
                action: {
                  label: t("fileManager.recognize.openConnections"),
                  onClick: () => onOpenConnectionSettings(firstTitle.provider),
                },
              }
            : {}),
        });
      } else if (firstTitle.reason === "rate_limited") {
        toast.warning(reasonMsg, {
          ...(firstTitle.retryAfterSeconds && firstTitle.retryAfterSeconds > 0
            ? { description: t("fileManager.recognize.runtimeIssue.rateLimitedWait", { n: firstTitle.retryAfterSeconds }) }
            : {}),
        });
      } else {
        toast.error(reasonMsg, {
          action: { label: t("fileManager.recognize.reload"), onClick: () => triggerFullReloadRef.current() },
        });
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- runs once per landed response snapshot
  }, [responseTick, preview.isFetching]);

  useEffect(() => {
    if (!preview.isError || !settled) return;
    const wasExplicit = explicitReloadPendingRef.current;
    explicitReloadPendingRef.current = false;
    setReloadingExplicit(false);
    if (data || wasExplicit) {
      toast.error(toUserMessage(preview.error, t("fileManager.recognize.preview.failed")));
    }
  }, [preview.isError, preview.error, settled, data]);

  // Effective runtime diagnostics for display (reusing remembered failure reasons on cache_miss).
  const mediaDiagnostics = useMemo(
    () => (data?.runtime_facts?.media_probe ?? []).filter((d) => d.state === "unavailable"),
    [data?.runtime_facts?.media_probe],
  );
  const titleDiagnostics = useMemo<EffectiveTitleIssue[]>(() => {
    const out: EffectiveTitleIssue[] = [];
    for (const diag of data?.runtime_facts?.episode_titles ?? []) {
      if (diag.state !== "unavailable") continue;
      const key = workKeyString({ provider: diag.provider, work_id: diag.work_id, season: diag.season });
      if (isLiveTitleFailureReason(diag.reason)) {
        const remembered = rememberedFailures.get(key);
        out.push({
          id: diag.id,
          provider: diag.provider,
          workId: diag.work_id,
          season: diag.season,
          reason: diag.reason,
          retryAfterSeconds: diag.retry_after_seconds ?? remembered?.retryAfterSeconds,
          receivedAtMs: remembered?.receivedAtMs ?? responseTick,
          fromCacheSuppression: false,
        });
      } else if (diag.reason === "cache_miss") {
        const remembered = rememberedFailures.get(key);
        if (remembered) {
          out.push({
            id: diag.id,
            provider: diag.provider,
            workId: diag.work_id,
            season: diag.season,
            reason: remembered.reason,
            retryAfterSeconds: remembered.retryAfterSeconds,
            receivedAtMs: remembered.receivedAtMs,
            fromCacheSuppression: true,
          });
        }
      }
    }
    return out;
  }, [data?.runtime_facts?.episode_titles, rememberedFailures, responseTick]);

  const diagnosticById = useMemo(() => {
    const map = new Map<string, { kind: "media"; item: FileManagerRecognitionMediaDiagnostic } | { kind: "title"; item: EffectiveTitleIssue }>();
    for (const item of mediaDiagnostics) map.set(item.id, { kind: "media", item });
    for (const item of titleDiagnostics) map.set(item.id, { kind: "title", item });
    return map;
  }, [mediaDiagnostics, titleDiagnostics]);

  const rows = useMemo(() => data?.rows ?? [], [data?.rows]);
  const groups = useMemo(() => groupRecognitionRows(rows), [rows]);

  const unitsText = useMemo(() => {
    const fileGroups = groups.flatMap((group) => {
      if (group.video) return [{ file: group.video, subtitles: group.subtitles }];
      return [...group.subtitles, ...group.others].map((file) => ({ file, subtitles: [] as FileManagerRecognitionRow[] }));
    });
    return fileUnitsSummary(fileGroups, (file) => ({
      kind: file.kind,
      included: isActionable(file),
      target: file.target_path ?? null,
    }));
  }, [groups]);

  const numberingItems: FileNumberingItem[] = useMemo(
    () =>
      rows.flatMap((row) =>
        row.kind === "video"
          ? [
              {
                path: row.path,
                sizeBytes: row.size_bytes,
                automaticSeason: metadata.season ?? null,
                automaticEpisode: automaticRecognitionNumber(row, offset ?? 0),
                automaticLabel: t("fileNumbering.automatic"),
                subtitleCount: rows.filter((subtitle) => subtitle.kind === "subtitle" && subtitle.group === row.group).length,
              },
            ]
          : [],
      ),
    [rows, metadata.season, offset],
  );

  const hasBlockers = rows.some((row) => row.disposition === "error");
  const blockedCount = rows.filter((row) => row.disposition === "error").length;
  const hasRuntimeBlockers = rows.some((row) => row.blockers?.some((b) => b.code === "runtime_facts_unavailable") || row.has_placeholder);
  const actionable = rows.filter(isActionable).length;
  const unknownCount = rows.filter((row) => row.disposition === "skip").length;
  const conflicts = data?.conflicts ?? [];
  const conflictsOverwritable = rows.every((row) => row.conflict?.occupied_by === undefined || row.conflict.occupied_by === "file");

  const canApply =
    Boolean(data) &&
    settled &&
    !preview.isFetching &&
    !preview.isError &&
    !numberInvalid &&
    !hasBlockers &&
    !hasRuntimeBlockers &&
    actionable > 0 &&
    !busy;

  let applyDisabledReason: string | undefined;
  if (!busy) {
    if (namingEmpty) applyDisabledReason = t("fileManager.recognize.preview.emptyTemplate");
    else if (numberInvalid) applyDisabledReason = t("fileManager.recognize.invalidNumber");
    else if (!data) applyDisabledReason = t("fileManager.recognize.preview.loading");
    else if (preview.isError) applyDisabledReason = t("fileManager.recognize.preview.failed");
    else if (!settled || preview.isFetching) applyDisabledReason = t("fileManager.recognize.preview.loading");
    else if (hasRuntimeBlockers) applyDisabledReason = t("fileManager.recognize.applyDisabled.runtimeFacts");
    else if (hasBlockers) applyDisabledReason = t("fileManager.recognize.applyDisabled.blockers", { n: blockedCount });
    else if (actionable === 0) applyDisabledReason = t("fileManager.recognize.applyDisabled.nothing");
  }

  const titleHint = data?.title_hint?.trim() ?? "";
  const titleSuggestions: TitleSuggestion[] = titleHint !== "" ? [{ source: "provider", text: titleHint }] : [];
  const matchQueryCandidates = titleHint !== "" ? [titleHint] : undefined;

  const phoneSummary = useMemo(() => {
    if (!data) return undefined;
    const videoRows = rows.filter((row) => row.kind === "video" && isActionable(row));
    const firstTarget = videoRows[0]?.target_path ?? rows.find((row) => row.target_path)?.target_path;
    const emptyFacts = [...new Set(rows.flatMap((row) => row.empty_facts ?? []))];
    const targetText = firstTarget
      ? videoRows.length > 1
        ? `${shortTarget(firstTarget)} ${t("oneshot.summaryMore", { n: videoRows.length })}`
        : shortTarget(firstTarget)
      : t("fileManager.recognize.willProcess", { n: actionable });
    return {
      text: `${t("fileManager.recognize.willProcess", { n: actionable })} · ${targetText}`,
      facts: emptyFacts,
    };
  }, [data, rows, actionable]);

  async function apply(choice: FileManagerRecognitionConflictChoice) {
    if (!data || !baseRequest || busy) return;
    setBusy(true);
    const body = { ...baseRequest, expected_plan_digest: data.plan_digest, conflict: choice };
    try {
      const result = await submitFileManagerRecognition(body, idempotency(JSON.stringify(body)));
      onFinished(result, choice === "skip" ? skippedByConflicts(data.rows, data.conflicts) : 0);
    } catch (cause) {
      if (
        cause instanceof ApiError &&
        (cause.code === "FILE_MANAGER_RECOGNITION_PLAN_CHANGED" || cause.code === "FILE_MANAGER_RECOGNITION_NOT_RENDERABLE")
      ) {
        toast.warning(t("fileManager.recognize.planChanged"));
        setReloadEpoch((n) => n + 1);
      } else {
        toast.error(toUserMessage(cause, t("common.operationFailed")));
      }
      setBusy(false);
      setConflictOpen(false);
    }
  }

  const refreshing = Boolean(data) && (!settled || preview.isFetching);

  return (
    <>
      <Dialog
        open
        tier="L"
        fixedHeight
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
        <ThemedToaster inDialog />
        <TaskSplitLayout>
          {phoneSummary ? (
            <div
              data-recognition-summary=""
              className="text-caption tw:sticky tw:top-0 tw:z-10 tw:hidden tw:items-center tw:gap-2 tw:border-b tw:border-border tw:bg-popover tw:px-4 tw:py-2 tw:phone:flex"
            >
              <button
                type="button"
                className="tw:min-w-0 tw:flex-1 tw:cursor-pointer tw:truncate tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:text-left tw:text-inherit tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
                onClick={() => previewRef.current?.scrollIntoView?.({ block: "start" })}
              >
                {phoneSummary.text}
              </button>
              {phoneSummary.facts.length > 0 ? (
                <button
                  type="button"
                  className={cn(inlineFactLinkClass, "tw:flex-none tw:text-warning")}
                  onClick={() => jumpTo(firstFactField(phoneSummary.facts))}
                >
                  {t("fileManager.recognize.preview.emptyFacts", {
                    facts: joinList(factLabels(phoneSummary.facts).map((fact) => fact.label)),
                  })}
                </button>
              ) : null}
            </div>
          ) : null}

          <section ref={previewRef} aria-label={t("fileManager.recognize.preview.heading")} className={taskPreviewColumnClass}>
            <div className="tw:flex tw:items-center tw:gap-2">
              <h3 className="text-body tw:m-0">{t("fileManager.recognize.preview.heading")}</h3>
              {refreshing ? (
                <span
                  role="status"
                  className="tw:flex tw:items-center tw:gap-1 tw:text-xs tw:text-muted-foreground tw:motion-safe:animate-fade-in"
                >
                  <SpinnerIcon className="tw:size-3.5" />
                  {reloadingExplicit ? t("fileManager.recognize.reloading") : t("oneshot.previewing")}
                </span>
              ) : (
                <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.recognize.preview.sub")}</span>
              )}
            </div>

            {namingEmpty ? (
              <StateBlock kind="empty" title={t("fileManager.recognize.preview.emptyTemplate")} />
            ) : numberInvalid && !data ? (
              <StateBlock kind="empty" title={t("fileManager.recognize.invalidNumber")} />
            ) : !data && preview.isError && settled ? (
              <StateBlock
                kind="error"
                title={toUserMessage(preview.error, t("fileManager.recognize.preview.failed"))}
                action={
                  <Button size="sm" variant="secondary" onClick={triggerFullReload}>
                    {t("fileManager.recognize.reload")}
                  </Button>
                }
              />
            ) : !data ? (
              <StateBlock kind="busy" skeleton="list" title={t("fileManager.recognize.preview.loading")} />
            ) : (
              <div className="tw:grid tw:gap-1.5 tw:rounded-control tw:border tw:border-border tw:px-3 tw:py-2.5 tw:motion-safe:animate-reveal">
                <p className="break-anywhere tw:m-0 tw:text-sm tw:font-medium" title={data.directory}>
                  {data.directory}
                </p>

                <div
                  role="toolbar"
                  aria-label={t("fileManager.recognize.actions")}
                  className="tw:flex tw:flex-wrap tw:items-center tw:gap-2"
                >
                  <Button
                    size="sm"
                    variant="secondary"
                    title={t("fileNumbering.seasonHint")}
                    disabled={busy || numberingItems.length === 0}
                    onClick={() => setNumberingOpen(true)}
                  >
                    {t("fileNumbering.title")}
                    {overrides.length > 0 ? (
                      <span
                        aria-hidden="true"
                        title={t("fileManager.recognize.numberingOverrides", { n: overrides.length })}
                        className="tw:ml-1 tw:rounded-full tw:bg-accent tw:px-1.5 tw:text-xs tw:font-semibold tw:text-accent-foreground"
                      >
                        {overrides.length}
                      </span>
                    ) : null}
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    loading={reloadingExplicit && preview.isFetching}
                    disabled={busy || namingEmpty || numberInvalid || preview.isFetching}
                    onClick={triggerFullReload}
                  >
                    {!(reloadingExplicit && preview.isFetching) ? <RefreshCw className="tw:size-3.5" aria-hidden="true" /> : null}
                    {t("fileManager.recognize.reload")}
                  </Button>
                  {overrides.length > 0 ? (
                    <button
                      type="button"
                      className={cn(inlineFactLinkClass, "text-caption tw:text-muted-foreground")}
                      onClick={() => setOverrides([])}
                    >
                      {t("fileManager.recognize.restoreNumbering")}
                    </button>
                  ) : null}
                </div>

                <SummaryMetaLine
                  unitsText={unitsText}
                  actionable={actionable}
                  conflictCount={conflicts.length}
                  titleIssues={titleDiagnostics}
                  mediaFailureCount={mediaDiagnostics.length}
                  onOpenConnectionSettings={onOpenConnectionSettings}
                />

                {rows.length > 0 ? (
                  <ul
                    aria-busy={refreshing || undefined}
                    className={cn(
                      "tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0 tw:transition-opacity",
                      refreshing && "tw:opacity-60",
                    )}
                  >
                    {groups.map((group) => {
                      const videoActive =
                        group.video !== undefined &&
                        (isActionable(group.video) || group.video.disposition === "unchanged");
                      const bound = group.video
                        ? group.subtitles.filter((row) => (videoActive ? isActionable(row) || row.disposition === "unchanged" : true))
                        : [];
                      const failed = group.video
                        ? group.subtitles.filter((row) => videoActive && !isActionable(row) && row.disposition !== "unchanged")
                        : [];
                      return (
                        <li key={group.key} className="tw:grid tw:gap-1">
                          {group.video ? (
                            <VideoRow
                              row={group.video}
                              subtitles={bound}
                              diagnosticById={diagnosticById}
                              onJump={jumpTo}
                            />
                          ) : (
                            group.subtitles.map((row) => (
                              <RecognitionFileRow key={row.path} row={row} diagnosticById={diagnosticById} onJump={jumpTo} />
                            ))
                          )}
                          {failed.map((row) => (
                            <RecognitionFileRow key={row.path} row={row} diagnosticById={diagnosticById} onJump={jumpTo} />
                          ))}
                          {group.others.map((row) => (
                            <RecognitionFileRow key={row.path} row={row} diagnosticById={diagnosticById} onJump={jumpTo} />
                          ))}
                        </li>
                      );
                    })}
                  </ul>
                ) : null}
              </div>
            )}
          </section>

          <TaskConfigPanel
            sectionRef={settingsRef}
            ariaLabel={t("fileManager.recognize.configAria")}
            title={t("fileManager.recognize.configTitle")}
            note={t("fileManager.recognize.intro")}
            className={taskPanelColumnClass}
            actions={
              <>
                <HelpTip label={t("fileManager.recognize.restoreDefaultsHelpLabel")}>
                  {t("fileManager.recognize.restoreDefaultsHint")}
                </HelpTip>
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
                  <div ref={namingRef} className="tw:rounded-control">
                    <RulesSectionsFields sections={rules} onChange={(next) => update({ rules: next })} only={NAMING_ONLY} />
                  </div>
                </>
              ),
            }}
          >
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
              <TitleSuggestionBadges suggestions={titleSuggestions} current={title} onPick={(next) => update({ title: next })} />
              <MetadataFields
                idPrefix="recognize"
                directEdit
                showYear
                showSeason
                title={title}
                matchQueryCandidates={matchQueryCandidates}
                onMatchTitle={(matched) =>
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
                    type="text"
                    inputMode="numeric"
                    aria-label={t("fileManager.recognize.match.episode")}
                    value={episodeText}
                    onChange={(event) => update({ episodeText: event.target.value })}
                    aria-invalid={episodeText !== "" && parsedEpisode === null ? true : undefined}
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
              {numberInvalid ? (
                <p className="text-caption tw:m-0 tw:text-destructive">{t("fileManager.recognize.invalidNumber")}</p>
              ) : null}
            </div>
            <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("fileManager.recognize.applyHint")}</p>
          </TaskConfigPanel>
        </TaskSplitLayout>
      </Dialog>
      {numberingOpen && data ? (
        <FileNumberingDialog
          title={t("fileManager.recognize.title")}
          items={numberingItems}
          overrides={overrides}
          onApply={(next) => setOverrides(sanitizeOverrides(next, new Set(numberingItems.map((item) => item.path))))}
          onClose={() => setNumberingOpen(false)}
        />
      ) : null}
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

function RateLimitedInlineTail({ issue }: { issue: EffectiveTitleIssue }) {
  const status = useMemo<UpstreamStatus>(
    () => ({
      state: "rate_limited",
      checked_at: new Date(issue.receivedAtMs).toISOString(),
      receivedAtMs: issue.receivedAtMs,
      retry_after_seconds: issue.retryAfterSeconds,
    }),
    [issue.receivedAtMs, issue.retryAfterSeconds],
  );
  const seconds = useUpstreamWait(status);
  const provider = providerDisplayName(issue.provider);
  return (
    <span className="tw:text-warning">
      {" · "}
      {seconds > 0
        ? t("fileManager.recognize.runtimeIssue.rateLimitedShort", { provider, n: seconds })
        : t("fileManager.recognize.runtimeIssue.rateLimitedShortReady", { provider })}
    </span>
  );
}

function SummaryMetaLine({
  unitsText,
  actionable,
  conflictCount,
  titleIssues,
  mediaFailureCount,
  onOpenConnectionSettings,
}: {
  unitsText: string;
  actionable: number;
  conflictCount: number;
  titleIssues: readonly EffectiveTitleIssue[];
  mediaFailureCount: number;
  onOpenConnectionSettings?: (provider: FileManagerRecognitionTitleProvider) => void;
}) {
  // Deduplicate summary tail per (provider, reason) so one batch never repeats phrases.
  const uniqueTitleIssues = useMemo(() => {
    const seen = new Set<string>();
    const list: EffectiveTitleIssue[] = [];
    for (const issue of titleIssues) {
      const key = `${issue.provider}:${issue.reason}`;
      if (seen.has(key)) continue;
      seen.add(key);
      list.push(issue);
    }
    return list;
  }, [titleIssues]);

  return (
    <p className="text-caption tw:m-0 tw:text-muted-foreground">
      {unitsText ? (
        <>
          <span>{unitsText}</span>
          <span> · </span>
        </>
      ) : null}
      <span>{t("fileManager.recognize.willProcess", { n: actionable })}</span>
      {conflictCount > 0 ? (
        <span className="tw:text-warning"> · {t("fileManager.recognize.conflictsSummary", { n: conflictCount })}</span>
      ) : null}
      {uniqueTitleIssues.map((issue) => {
        const provider = providerDisplayName(issue.provider);
        if (issue.reason === "rate_limited") {
          return <RateLimitedInlineTail key={`${issue.provider}:${issue.reason}`} issue={issue} />;
        }
        if (issue.reason === "unconfigured" || issue.reason === "credentials_rejected") {
          const label = t(
            issue.reason === "unconfigured"
              ? "fileManager.recognize.runtimeIssue.unconfiguredShort"
              : "fileManager.recognize.runtimeIssue.credentialsRejectedShort",
            { provider },
          );
          return (
            <span key={`${issue.provider}:${issue.reason}`} className="tw:text-warning">
              {" · "}
              {onOpenConnectionSettings ? (
                <button type="button" className={inlineFactLinkClass} onClick={() => onOpenConnectionSettings(issue.provider)}>
                  {label}
                </button>
              ) : (
                label
              )}
            </span>
          );
        }
        const label = t(
          issue.reason === "unreachable"
            ? "fileManager.recognize.runtimeIssue.unreachableShort"
            : "fileManager.recognize.runtimeIssue.invalidResponseShort",
          { provider },
        );
        return (
          <span key={`${issue.provider}:${issue.reason}`} className="tw:text-destructive">
            {" · "}
            {label}
          </span>
        );
      })}
      {mediaFailureCount > 0 ? (
        <span className="tw:text-warning">
          {" · "}
          {t("fileManager.recognize.runtimeIssue.mediaFailedShort", { n: mediaFailureCount })}
        </span>
      ) : null}
    </p>
  );
}

type DiagnosticMap = ReadonlyMap<
  string,
  { kind: "media"; item: FileManagerRecognitionMediaDiagnostic } | { kind: "title"; item: EffectiveTitleIssue }
>;

type PreviewGroup = {
  key: string;
  video?: FileManagerRecognitionRow;
  subtitles: FileManagerRecognitionRow[];
  others: FileManagerRecognitionRow[];
};

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

function rowPlaceholderHint(row: FileManagerRecognitionRow, diagnosticById: DiagnosticMap): string | undefined {
  if (!row.has_placeholder) return undefined;
  const runtimeBlocker = row.blockers?.find((b) => b.code === "runtime_facts_unavailable");
  for (const id of runtimeBlocker?.diagnostic_ids ?? []) {
    const entry = diagnosticById.get(id);
    if (!entry) continue;
    if (entry.kind === "title") {
      return t("fileManager.recognize.runtimeIssue.placeholderHintTitle", {
        reason: titleIssueReasonText(entry.item.provider, entry.item.reason),
      });
    }
    return t("fileManager.recognize.runtimeIssue.placeholderHintMedia");
  }
  return t("fileManager.recognize.runtimeIssue.placeholderHintGeneric");
}

function VideoRow({
  row,
  subtitles,
  diagnosticById,
  onJump,
}: {
  row: FileManagerRecognitionRow;
  subtitles: readonly FileManagerRecognitionRow[];
  diagnosticById: DiagnosticMap;
  onJump: (field: FactField) => void;
}) {
  return (
    <div className="tw:grid tw:gap-0.5 tw:rounded-md tw:bg-muted/15 tw:px-2.5 tw:py-2">
      <SourceLine row={row} />
      <TargetLine row={row} placeholderHint={rowPlaceholderHint(row, diagnosticById)} />
      {row.empty_facts?.length ? (
        <MissingFactsNote facts={row.empty_facts} copyKey="fileManager.recognize.preview.emptyFacts" onJump={onJump} />
      ) : null}
      <BlockerLines row={row} onJump={onJump} />
      <RecognitionMeta row={row} />
      <BoundSubtitles
        videoName={row.name}
        subtitles={subtitles.map(subtitleItem)}
        videoExcluded={row.disposition === "skip" || (row.disposition === "error" && !row.has_placeholder)}
        showLanguage={false}
        placeholderTone="error"
      />
    </div>
  );
}

function RecognitionFileRow({
  row,
  diagnosticById,
  onJump,
}: {
  row: FileManagerRecognitionRow;
  diagnosticById: DiagnosticMap;
  onJump: (field: FactField) => void;
}) {
  return (
    <div className="tw:grid tw:gap-0.5 tw:rounded-md tw:bg-muted/15 tw:px-2.5 tw:py-2">
      <SourceLine row={row} />
      <TargetLine row={row} placeholderHint={rowPlaceholderHint(row, diagnosticById)} />
      {row.empty_facts?.length ? (
        <MissingFactsNote facts={row.empty_facts} copyKey="fileManager.recognize.preview.emptyFacts" onJump={onJump} />
      ) : null}
      <BlockerLines row={row} onJump={onJump} />
      <RecognitionMeta row={row} />
    </div>
  );
}

function SourceLine({ row }: { row: FileManagerRecognitionRow }) {
  return (
    <p className="text-caption break-anywhere tw:m-0 tw:text-muted-foreground" title={row.path}>
      {row.name}
      {row.size_bytes !== undefined && row.size_bytes >= 0 ? <span> · {formatBytes(row.size_bytes)}</span> : null}
    </p>
  );
}

function TargetLine({ row, placeholderHint }: { row: FileManagerRecognitionRow; placeholderHint?: string }) {
  const target = row.target_path ?? row.target_name;
  const nonRuntimeError =
    row.disposition === "error" &&
    !row.has_placeholder &&
    !(row.blockers?.every((b) => b.code === "runtime_facts_unavailable") ?? false);
  return (
    <p className={cn("break-anywhere tw:m-0 tw:text-sm", nonRuntimeError && "tw:text-destructive")} title={target}>
      {target ? (
        <TargetPathText value={target} placeholderTone="error" placeholderHint={placeholderHint} />
      ) : (
        <span className="text-caption tw:text-muted-foreground">{t(`fileManager.recognize.preview.disposition.${row.disposition}`)}</span>
      )}
    </p>
  );
}

function RecognitionMeta({ row }: { row: FileManagerRecognitionRow }) {
  const episode = row.episode;
  if (!episode && !row.conflict) return null;
  return (
    <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-0.5 tw:text-xs tw:text-muted-foreground">
      {episode ? (
        <span title={episode.raw ? t("fileManager.recognize.preview.episodeRaw", { raw: episode.raw }) : undefined}>
          {t("fileManager.recognize.preview.episode", { n: episode.number })}
          {episode.number_end ? `-${episode.number_end}` : ""}
          {episode.offset !== 0 ? ` ${t("fileManager.recognize.preview.episodeOffset", { n: episode.offset })}` : ""}
          {episode.source === "manual" ? ` · ${t("fileManager.recognize.preview.episodeManual")}` : ""}
        </span>
      ) : null}
      {row.conflict ? <span className="tw:text-warning">{t(`fileManager.recognize.preview.conflict.${row.conflict.occupied_by}`)}</span> : null}
    </div>
  );
}

function BlockerLines({ row, onJump }: { row: FileManagerRecognitionRow; onJump: (field: FactField) => void }) {
  if (!row.blockers?.length) return null;
  return (
    <>
      {row.blockers.map((blocker, index) => {
        // When a runtime failure already renders as a rose ⟨...⟩ placeholder on the target
        // path, avoid repeating the generic blocker sentence beneath every single row.
        if (blocker.code === "runtime_facts_unavailable" && row.has_placeholder) return null;
        if (blocker.code === "facts_missing" && blocker.facts?.length) {
          return (
            <MissingFactsNote
              key={index}
              facts={blocker.facts}
              tone="error"
              copyKey="fileManager.recognize.preview.blocker.facts_missing"
              onJump={onJump}
            />
          );
        }
        return (
          <p key={index} className="text-caption tw:m-0 tw:text-destructive">
            {blockerText(blocker)}
          </p>
        );
      })}
    </>
  );
}

function subtitleItem(row: FileManagerRecognitionRow): BoundSubtitleItem {
  return {
    key: row.path,
    path: row.name,
    sizeBytes: row.size_bytes,
    included: isActionable(row) || row.disposition === "unchanged" || row.has_placeholder,
    target: row.target_path ?? row.target_name ?? null,
    targetNote: row.conflict ? t(`fileManager.recognize.preview.conflict.${row.conflict.occupied_by}`) : undefined,
  };
}

function blockerText(blocker: FileManagerRecognitionBlocker): string {
  if (blocker.code === "facts_missing") {
    return t("fileManager.recognize.preview.blocker.facts_missing_unnamed");
  }
  return t(`fileManager.recognize.preview.blocker.${blocker.code}`);
}

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

function automaticRecognitionNumber(row: FileManagerRecognitionRow, automaticOffset: number): number | null {
  const episode = row.episode;
  if (!episode) return null;
  if (episode.source === "manual" && episode.raw?.trim()) {
    const raw = Number(episode.raw);
    const automatic = raw + automaticOffset;
    if (Number.isInteger(raw) && Number.isInteger(automatic) && automatic >= 1) return automatic;
  }
  const number = Number(episode.number);
  return Number.isInteger(number) && number >= 1 ? number : null;
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
