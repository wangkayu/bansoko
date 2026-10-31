import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { toast } from "../../ui/toast";
import {
  isVersionConflict,
  patchAnimeMetadata,
  toUserMessage,
  type AnimeMetadata,
  type AnimeMetadataDraft,
  type AnimeMetadataPreview,
  type TitleSuggestion,
} from "../../lib/api/client";
import type { MetadataCandidate } from "../../lib/metadata/types";
import { boundTitleSuggestions, candidateTitleSuggestions } from "./candidateTitles";
import { metadataDraftFromOwner, valuesEqual } from "../../lib/draft";
import { useSavePart } from "../../lib/saveParts";
import { MetadataFields } from "../anime-forms/MetadataFields";
import { TitleSuggestionBadges } from "../anime-forms/TitleSuggestionBadges";
import { Pencil } from "lucide-react";
import { StateBlock, ConflictBlock, Input, Button } from "../../ui";
import type { AnimeTabHost, DraftTabHost, OwnerTabHost } from "./host";
import { useOwnerEdit } from "./useOwnerEdit";
import { t } from "../../lib/i18n";

// The metadata tab's sole implementation: before subscribing it writes the shared in-memory draft
// (saved as a whole candidate); after subscribing it is the owner draft, committed by the dialog
// footer's single save as one PATCH (title, bindings, media kind, season, year, release format
// and total episodes together). Every control except the title edits the draft directly; the
// title is fixed and becomes editable only through "edit", with "save title" writing it back into
// the draft.

/** An external request to open the match dialog; seq increments each time so the same site can open again. */
export type MetadataMatchRequest = { provider: "bgm" | "tmdb"; seq: number };

export function MetadataTab({
  host,
  readOnly = false,
  matchRequest,
  draftTitleSuggestions,
  onPreviewChange,
}: {
  host: AnimeTabHost;
  /** Draft mode: title candidates from the draft's RSS read (an owner's come with the workspace). */
  draftTitleSuggestions?: TitleSuggestion[];
  /** A read-only workspace (a retired owner): shows the effective values, renders no editing entry. */
  readOnly?: boolean;
  matchRequest?: MetadataMatchRequest;
  /**
   * owner 模式：有未保存编辑时上报完整的未保存元数据（与保存提交的 PATCH 同形，
   * 不含版本），没有时上报 undefined。RSS tab 用它预览，不改变保存模型。
   */
  onPreviewChange?: (preview: AnimeMetadataPreview | undefined) => void;
}) {
  // The controlled matchProvider: MetadataFields's internal MetadataMatchDialog completes the search and the selection backfill.
  const [matchProvider, setMatchProvider] = useState<"bgm" | "tmdb">();
  useEffect(() => {
    if (matchRequest && !readOnly) setMatchProvider(matchRequest.provider);
  }, [matchRequest, readOnly]);
  if (host.mode === "draft") {
    return (
      <DraftMetadataTab
        host={host}
        suggestions={draftTitleSuggestions}
        matchProvider={matchProvider}
        onMatchProviderChange={setMatchProvider}
      />
    );
  }
  return (
    <OwnerMetadataTab
      host={host}
      readOnly={readOnly}
      matchProvider={matchProvider}
      onMatchProviderChange={setMatchProvider}
      onPreviewChange={onPreviewChange}
    />
  );
}

// --- Title row -----------------------------------------------------------------------
//
// The title is a fixed value: written at subscribing (a draft defaults to the site title) and
// afterwards untouched by site or metadata changes. It displays read-only at rest and becomes
// editable through "edit"; "save title" writes it back into the caller's draft (an owner commits
// through the footer's single save) and "cancel" drops the input. The title may not be empty.
// While editing, the title candidates show as badges; a click saves that text like a typed title.

function TitleRow({
  idPrefix,
  value,
  readOnly = false,
  suggestions,
  onChange,
}: {
  idPrefix: string;
  /** The current title (the value already written into the draft). */
  value: string;
  readOnly?: boolean;
  suggestions?: readonly TitleSuggestion[];
  onChange: (next: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const [error, setError] = useState<string>();
  const labelId = `${idPrefix}-title-label`;

  function startEdit() {
    setText(value);
    setError(undefined);
    setEditing(true);
  }

  function cancel() {
    setEditing(false);
    setError(undefined);
  }

  function save() {
    const trimmed = text.trim();
    if (trimmed === "") {
      setError(t("tabs.metadata.titleRequired"));
      return;
    }
    if (trimmed !== value) onChange(trimmed);
    setEditing(false);
    setError(undefined);
  }

  return (
    <section className="title-row" aria-labelledby={labelId}>
      <div className="title-row-inner">
        <span id={labelId} className="title-row-label text-body">
          {t("tabs.metadata.title")}
        </span>
        {editing ? (
          <>
            <Input
              aria-label={t("tabs.metadata.title")}
              value={text}
              aria-invalid={error !== undefined}
              autoFocus
              onChange={(event) => {
                setText(event.target.value);
                setError(undefined);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  save();
                } else if (event.key === "Escape") {
                  event.preventDefault();
                  event.stopPropagation();
                  cancel();
                }
              }}
              controlSize="sm"
              className="tw:flex-1"
            />
            <Button size="sm" variant="primary" onClick={save}>
              {t("tabs.metadata.saveTitle")}
            </Button>
            <Button size="sm" variant="ghost" onClick={cancel}>
              {t("act.cancel")}
            </Button>
          </>
        ) : (
          <>
            <span className="title-row-value text-body" title={value}>
              {value}
            </span>
            {!readOnly && (
              <Button
                size="sm"
                variant="ghost"
                className="title-row-edit"
                onClick={startEdit}
                aria-label={t("tabs.metadata.editTitleAria")}
              >
                <Pencil className="metadata-icon" />
                {t("act.edit")}
              </Button>
            )}
          </>
        )}
      </div>
      {editing && (
        <TitleSuggestionBadges
          className="title-row-suggestions"
          suggestions={suggestions}
          current={value}
          onPick={(text) => {
            onChange(text);
            setEditing(false);
            setError(undefined);
          }}
        />
      )}
      {error && (
        <p role="alert" className="title-row-error text-caption">
          {error}
        </p>
      )}
    </section>
  );
}

// --- Candidate titles -----------------------------------------------------------------

function useMatchedTitleSuggestions() {
  const [matched, setMatched] = useState<Record<string, TitleSuggestion[]>>({});
  const recordMatch = useCallback((candidate: MetadataCandidate) => {
    const titles = candidateTitleSuggestions(candidate);
    setMatched((prev) => ({ ...prev, [`${candidate.provider}:${candidate.workId}`]: titles }));
  }, []);
  return { matched, recordMatch };
}

function MetadataTabView({
  idPrefix,
  title,
  readOnly = false,
  value,
  onValueChange,
  titleOffer,
  onTotalEpisodesChange,
  onMediaKindChange,
  mediaKind,
  searchTitle,
  matchProvider,
  onMatchProviderChange,
  onMatchCandidate,
  blocked,
  busy = false,
}: {
  idPrefix: string;
  title: ReactNode;
  readOnly?: boolean;
  value: AnimeMetadataDraft;
  /** mediaKind / title are given when the change also sets them (a match confirm); store all in one update. */
  onValueChange: (next: AnimeMetadataDraft, mediaKind?: "tv" | "movie", title?: string) => void;
  /** The match dialog's "also set the title" choice (current title and the provider's). */
  titleOffer?: { current: string; provider: string };
  onTotalEpisodesChange: (total: number | null) => void;
  onMediaKindChange: (next: "tv" | "movie") => void;
  mediaKind?: "tv" | "movie" | null;
  searchTitle: string;
  matchProvider?: "bgm" | "tmdb";
  onMatchProviderChange: (provider: "bgm" | "tmdb" | undefined) => void;
  /** A confirmed match hands its candidate to the caller, which offers the candidate's titles. */
  onMatchCandidate?: (candidate: MetadataCandidate) => void;
  blocked?: ReactNode;
  busy?: boolean;
}) {
  const asSave =
    <A,>(fn: (arg: A) => void) =>
    async (arg: A) => {
      fn(arg);
      return true;
    };
  return (
    <div className="metadata-stack">
      {blocked}
      {title}
      <MetadataFields
        idPrefix={idPrefix}
        title={searchTitle}
        value={value}
        onChange={onValueChange}
        titleOffer={readOnly ? undefined : titleOffer}
        busy={busy}
        readOnly={readOnly}
        directEdit
        showSeason
        onSaveTotalEpisodes={readOnly ? undefined : asSave(onTotalEpisodesChange)}
        onSaveMediaKind={readOnly ? undefined : asSave(onMediaKindChange)}
        mediaKind={mediaKind}
        showYear
        matchProvider={readOnly ? undefined : matchProvider}
        onMatchProviderChange={readOnly ? undefined : onMatchProviderChange}
        onMatchCandidate={readOnly ? undefined : onMatchCandidate}
      />
    </div>
  );
}

// --- Draft: writes the shared in-memory draft directly -------------------------------------

function DraftMetadataTab({
  host,
  suggestions,
  matchProvider,
  onMatchProviderChange,
}: {
  host: DraftTabHost;
  suggestions?: TitleSuggestion[];
  matchProvider?: "bgm" | "tmdb";
  onMatchProviderChange: (provider: "bgm" | "tmdb" | undefined) => void;
}) {
  const { draft, onDraftChange } = host;
  const { matched, recordMatch } = useMatchedTitleSuggestions();
  // A draft's title defaults to the site title; subscribing writes it as the fixed title (title_override wins).
  const draftTitle = draft.title_override?.trim() || draft.title;
  const titleSuggestions = [...(suggestions ?? []), ...boundTitleSuggestions(matched, draft.metadata)];
  const setMetadata = (metadata: AnimeMetadataDraft) => onDraftChange({ ...draft, metadata });
  return (
    <MetadataTabView
      idPrefix="draft"
      title={
        <TitleRow
          idPrefix="draft"
          value={draftTitle}
          suggestions={titleSuggestions}
          onChange={(title) => onDraftChange({ ...draft, title, title_override: null })}
        />
      }
      searchTitle={draftTitle}
      value={draft.metadata}
      onValueChange={(metadata, mediaKind, title) => {
        const next = mediaKind ? { ...metadata, media_kind: mediaKind } : metadata;
        if (title === undefined) setMetadata(next);
        else onDraftChange({ ...draft, metadata: next, title, title_override: null });
      }}
      titleOffer={{ current: draftTitle, provider: suggestions?.find((item) => item.source === "provider")?.text ?? "" }}
      onTotalEpisodesChange={(total) => setMetadata({ ...draft.metadata, total_episodes: total })}
      onMediaKindChange={(next) => setMetadata({ ...draft.metadata, media_kind: next })}
      mediaKind={draft.metadata.media_kind}
      matchProvider={matchProvider}
      onMatchProviderChange={onMatchProviderChange}
      onMatchCandidate={recordMatch}
    />
  );
}

// --- Owner: one draft, one PATCH from the footer ---------------------------------------------

type OwnerMetadataEdit = {
  metadata: AnimeMetadataDraft;
  /** The fixed title (non-empty); sent as title_override on save. */
  titleOverride: string;
  mediaKind: "tv" | "movie" | null;
};

/** The fixed title lives in title_override, written at subscribing. When older data has it empty, the title at subscribe time is the fallback. */
function ownerEditFrom(metadata: AnimeMetadata, baseTitle: string): OwnerMetadataEdit {
  return {
    metadata: metadataDraftFromOwner(metadata),
    titleOverride: metadata.title_override?.trim() || baseTitle,
    mediaKind: metadata.media_kind ?? null,
  };
}

function normalizedTitle(value: string): string | null {
  const trimmed = value.trim();
  return trimmed === "" ? null : trimmed;
}

/** The unsaved field count (title, Bangumi, TMDB, media kind, year, season and total episodes each count as one). */
function metadataChangeCount(saved: OwnerMetadataEdit, edit: OwnerMetadataEdit): number {
  let n = 0;
  if (normalizedTitle(saved.titleOverride) !== normalizedTitle(edit.titleOverride)) n += 1;
  if (saved.mediaKind !== edit.mediaKind) n += 1;
  if (!valuesEqual(saved.metadata.bgm ?? null, edit.metadata.bgm ?? null)) n += 1;
  if (!valuesEqual(saved.metadata.tmdb ?? null, edit.metadata.tmdb ?? null)) n += 1;
  if ((saved.metadata.year ?? null) !== (edit.metadata.year ?? null)) n += 1;
  if ((saved.metadata.season ?? null) !== (edit.metadata.season ?? null)) n += 1;
  if ((saved.metadata.total_episodes ?? null) !== (edit.metadata.total_episodes ?? null)) n += 1;
  return n;
}

const editEqual = (a: OwnerMetadataEdit, b: OwnerMetadataEdit) => metadataChangeCount(a, b) === 0;

/** One edit's complete metadata value: the save's PATCH minus the version, also the RSS preview's input. */
function metadataPreviewFromEdit(edit: OwnerMetadataEdit): AnimeMetadataPreview {
  return {
    bgm: edit.metadata.bgm ?? null,
    tmdb: edit.metadata.tmdb ?? null,
    title_override: normalizedTitle(edit.titleOverride),
    media_kind: edit.mediaKind,
    total_episodes: edit.metadata.total_episodes ?? null,
    year: edit.metadata.year ?? null,
    season: edit.metadata.season ?? null,
  };
}

function OwnerMetadataTab({
  host,
  readOnly,
  matchProvider,
  onMatchProviderChange,
  onPreviewChange,
}: {
  host: OwnerTabHost;
  readOnly: boolean;
  matchProvider?: "bgm" | "tmdb";
  onMatchProviderChange: (provider: "bgm" | "tmdb" | undefined) => void;
  onPreviewChange?: (preview: AnimeMetadataPreview | undefined) => void;
}) {
  const { animeID, workspace, onChanged } = host;
  const baseTitle = workspace.managed_anime.title;
  const saved = useMemo(() => ownerEditFrom(workspace.metadata, baseTitle), [workspace.metadata, baseTitle]);
  const { edit, setEdit, baseline, markSaved } = useOwnerEdit<OwnerMetadataEdit>(saved, editEqual);
  const { matched, recordMatch } = useMatchedTitleSuggestions();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  // Between a successful save and the re-read, the CAS version follows the server's response.
  const [savedVersion, setSavedVersion] = useState<{ base: number; next: number }>();
  const metadataVersion =
    savedVersion && savedVersion.base === workspace.metadata.version ? savedVersion.next : workspace.metadata.version;

  const count = !readOnly && edit && baseline ? metadataChangeCount(baseline, edit) : 0;

  // Unsaved metadata serves only the RSS preview: saving and discarding still go through the footer's single save.
  const preview = count > 0 && edit ? metadataPreviewFromEdit(edit) : undefined;
  const previewKey = preview ? JSON.stringify(preview) : "";
  useEffect(() => {
    onPreviewChange?.(previewKey ? (JSON.parse(previewKey) as AnimeMetadataPreview) : undefined);
  }, [previewKey, onPreviewChange]);
  useEffect(() => () => onPreviewChange?.(undefined), [onPreviewChange]);

  // The server re-evaluates the unsubmitted releases in the same transaction when anything but the
  // title changed, and reports how many it scheduled.
  const save = useCallback(async () => {
    if (!edit || !baseline) return { ok: true as const };
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const result = await patchAnimeMetadata(animeID, {
        expected_version: metadataVersion,
        ...metadataPreviewFromEdit(edit),
      });
      setSavedVersion({ base: workspace.metadata.version, next: result.version });
      markSaved(edit);
      onChanged();
      const message =
        result.reevaluated_releases === undefined
          ? t("tabs.metadata.saved")
          : t("tabs.metadata.savedReevaluated", { n: result.reevaluated_releases });
      return { ok: true as const, message };
    } catch (cause) {
      if (isVersionConflict(cause)) setConflict(cause.currentVersion);
      setError(toUserMessage(cause, t("tabs.metadata.saveFailed")));
      return { ok: false as const };
    } finally {
      setBusy(false);
    }
  }, [animeID, baseline, edit, markSaved, metadataVersion, onChanged, workspace.metadata.version]);

  useSavePart("metadata", { count, label: t("tabs.names.metadata"), busy, save });

  // A 409 refresh: re-read the workspace (along onChanged's refetch path), keeping local edits.
  function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    onChanged();
  }

  if (!edit) return <StateBlock kind="busy" title={t("tabs.metadata.reading")} />;
  const effectiveTitle = normalizedTitle(edit.titleOverride) ?? baseTitle;
  const titleSuggestions = [
    ...(workspace.metadata.title_suggestions ?? []),
    ...boundTitleSuggestions(matched, edit.metadata),
  ];
  return (
    <MetadataTabView
      idPrefix="owner"
      title={
        <TitleRow
          idPrefix="owner"
          value={readOnly ? (normalizedTitle(saved.titleOverride) ?? baseTitle) : effectiveTitle}
          readOnly={readOnly}
          suggestions={titleSuggestions}
          onChange={(titleOverride) => setEdit({ ...edit, titleOverride })}
        />
      }
      searchTitle={effectiveTitle}
      readOnly={readOnly}
      busy={busy}
      value={edit.metadata}
      onValueChange={(metadata, mediaKind, title) =>
        setEdit({
          ...edit,
          metadata,
          mediaKind: mediaKind ?? edit.mediaKind,
          titleOverride: title ?? edit.titleOverride,
        })
      }
      titleOffer={{
        current: effectiveTitle,
        provider: workspace.metadata.title_suggestions?.find((item) => item.source === "provider")?.text ?? "",
      }}
      onTotalEpisodesChange={(total) => setEdit({ ...edit, metadata: { ...edit.metadata, total_episodes: total } })}
      onMediaKindChange={(mediaKind) => setEdit({ ...edit, mediaKind })}
      mediaKind={edit.mediaKind}
      matchProvider={matchProvider}
      onMatchProviderChange={onMatchProviderChange}
      onMatchCandidate={recordMatch}
      blocked={
        <>
          {conflict !== undefined && (
            <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
          )}
          {conflict === undefined && error && <StateBlock kind="error" title={error} />}
        </>
      }
    />
  );
}
