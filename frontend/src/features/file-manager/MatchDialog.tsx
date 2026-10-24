import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ApiError,
  listAnime,
  listAnimeTMDBSeasonEpisodes,
  listAnimeTMDBSeasons,
  matchFileManagerEntry,
  previewFileManagerMatch,
  toUserMessage,
  type FileManagerEntryDetail,
  type FileManagerMatchBlocker,
  type FileManagerMatchPreview,
  type FileManagerMatchRequest,
  type FileManagerOperationView,
} from "../../lib/api/client";
import { joinList, joinSentences } from "../../lib/i18n/format";
import { t, tryT } from "../../lib/i18n";
import { animeKeys, fileManagerKeys } from "../../lib/queryKeys";
import { Alert, Button, Dialog, Field, Input, Select, type SelectOption } from "../../ui/base";
import { useWriteFlow } from "./useWriteFlow";

// MatchDialog — matches an unmanaged video to a season/episode of some anime.
// One dialog: anime (defaulting to the one the file came from) -> season -> episode (pickable from
//   the TMDB list or typed by hand) -> preview of the organized location -> on conflict a choice of
//   "cancel / add a suffix / pick another episode" -> submit. The preview writes nothing; the
//   submission carries the revision number and target from the preview, and the server rejects it
//   if its re-render disagrees. The chosen episode is final, with no episode offset applied; the
//   result stays unmanaged.

const MANUAL = "__manual__";
const DEBOUNCE_MS = 300;

function wholeNumber(text: string, min: number, max: number): number | null {
  if (!/^\d{1,5}$/.test(text.trim())) return null;
  const value = Number(text.trim());
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

function factLabel(path: string): string {
  return tryT(`facts.${path}.label`) ?? t("fileManager.match.fact.unknown");
}

function BlockerNotice({ blocker }: { blocker: FileManagerMatchBlocker }) {
  const facts = blocker.facts ?? [];
  const text =
    blocker.code === "facts_missing"
      ? facts.length > 0
        ? t("fileManager.match.blocker.facts_missing", { facts: joinList(facts.map(factLabel)) })
        : t("fileManager.match.blocker.facts_missing_unnamed")
      : t(`fileManager.match.blocker.${blocker.code}`);
  return <Alert tone="warning" title={text} />;
}

function PreviewBody({ preview }: { preview: FileManagerMatchPreview }) {
  const shown = preview.subtitles_left_behind.slice(0, 3);
  const more = preview.subtitles_left_behind.length - shown.length;
  return (
    <div className="tw:grid tw:gap-2 tw:text-sm">
      {preview.target_path ? (
        <div className="tw:grid tw:gap-1">
          <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.targetLabel")}</span>
          <code className="tw:break-all tw:rounded-control tw:bg-muted tw:px-2 tw:py-1 tw:text-xs">{preview.target_path}</code>
        </div>
      ) : null}
      {preview.directories_to_create.length > 0 ? (
        <div className="tw:grid tw:gap-1">
          <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.createFolders")}</span>
          <ul className="tw:m-0 tw:grid tw:gap-0.5 tw:pl-4 tw:text-xs">
            {preview.directories_to_create.map((directory) => (
              <li key={directory} className="tw:break-all">
                {directory}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {shown.length > 0 ? (
        <div className="tw:grid tw:gap-0.5 tw:text-xs tw:text-muted-foreground">
          <span>{t("fileManager.match.subtitlesStay")}</span>
          {shown.map((name) => (
            <span key={name} className="tw:break-all">
              {name}
            </span>
          ))}
          {more > 0 ? <span>{t("fileManager.match.subtitlesStayMore", { n: more })}</span> : null}
        </div>
      ) : null}
      <span className="tw:text-xs tw:text-muted-foreground">{preview.from_lineage ? t("fileManager.match.fromFile") : t("fileManager.match.withoutFile")}</span>
    </div>
  );
}

export function MatchDialog({
  detail,
  onClose,
  onDone,
}: {
  detail: FileManagerEntryDetail;
  onClose: () => void;
  onDone: (view: FileManagerOperationView) => void;
}) {
  const path = detail.entry.path;
  const flow = useWriteFlow();
  const episodeRef = useRef<HTMLInputElement>(null);

  const animeList = useQuery({ queryKey: animeKeys.list, queryFn: listAnime });
  const animeOptions: SelectOption<string>[] = (animeList.data ?? [])
    .filter((item) => item.subscription.state === "active" || item.subscription.state === "stopped")
    .map((item) => ({ value: item.managed_anime.id, label: item.managed_anime.title_override ?? item.managed_anime.title }));
  const defaultAnime = detail.match_default_anime;
  if (defaultAnime && !animeOptions.some((option) => option.value === defaultAnime.managed_anime_id)) {
    animeOptions.unshift({ value: defaultAnime.managed_anime_id, label: defaultAnime.title });
  }

  const [animeID, setAnimeID] = useState(defaultAnime?.managed_anime_id ?? "");
  const [seasonText, setSeasonText] = useState("");
  const [episodeText, setEpisodeText] = useState("");
  const [seasonManual, setSeasonManual] = useState(false);
  const [episodeManual, setEpisodeManual] = useState(false);
  const [raced, setRaced] = useState(false);

  const season = wholeNumber(seasonText, 0, 9999);
  const episode = wholeNumber(episodeText, 1, 99999);

  const seasons = useQuery({
    queryKey: fileManagerKeys.tmdbSeasons(animeID),
    queryFn: () => listAnimeTMDBSeasons(animeID),
    enabled: animeID !== "",
    retry: false,
  });
  const seasonList = seasons.data?.seasons ?? [];
  const seasonsFailed = seasons.isError;
  const seasonListed = !seasonsFailed && seasonList.length > 0;
  const showSeasonInput = seasonManual || !seasonListed;

  const episodes = useQuery({
    queryKey: fileManagerKeys.tmdbEpisodes(animeID, season ?? -1),
    queryFn: () => listAnimeTMDBSeasonEpisodes(animeID, season ?? 0),
    enabled: animeID !== "" && season !== null && seasonListed && !seasonManual,
    retry: false,
  });
  const episodeList = episodes.data?.episodes ?? [];
  const episodeListed = !episodes.isError && episodeList.length > 0 && season !== null && !seasonManual;
  const showEpisodeInput = episodeManual || !episodeListed;

  const request = useDebounced({ animeID, season, episode }, DEBOUNCE_MS);
  const ready = request.animeID !== "" && request.season !== null && request.episode !== null;
  const settled = ready && request.animeID === animeID && request.season === season && request.episode === episode;
  const preview = useQuery({
    queryKey: fileManagerKeys.matchPreview(path, request.animeID, request.season ?? -1, request.episode ?? -1),
    queryFn: () => previewFileManagerMatch({ path, managed_anime_id: request.animeID, season: request.season ?? 0, episode: request.episode ?? 1 }),
    enabled: ready,
    retry: false,
    staleTime: 0,
    gcTime: 0,
  });

  const data = settled ? preview.data : undefined;
  const previewError = settled && preview.isError ? toUserMessage(preview.error, t("fileManager.match.previewFailed")) : null;
  const loadingPreview = ready && (!settled || preview.isFetching) && !data;
  const blocked = (data?.blockers.length ?? 0) > 0 || !data?.target_path;
  const conflict = data?.conflict;

  const seasonOptions: SelectOption<string>[] = [
    { value: "", label: t("fileManager.match.season"), disabled: true },
    ...seasonList.map((item) => ({
      value: String(item.season),
      label: t(item.season === 0 ? "fileManager.match.specialsOption" : "fileManager.match.seasonOption", { n: item.season, count: item.episode_count }),
    })),
    { value: MANUAL, label: t("fileManager.match.manual") },
  ];
  const episodeOptions: SelectOption<string>[] = [
    { value: "", label: t("fileManager.match.episode"), disabled: true },
    ...episodeList.map((item) => ({
      value: String(item.episode),
      label: [t("fileManager.match.episodeOption", { n: item.episode }), item.title, item.air_date].filter(Boolean).join(" · "),
    })),
    { value: MANUAL, label: t("fileManager.match.manual") },
  ];

  const chooseAnime = (next: string) => {
    setAnimeID(next);
    setSeasonText("");
    setEpisodeText("");
    setSeasonManual(false);
    setEpisodeManual(false);
    setRaced(false);
    flow.reset();
  };
  const chooseSeason = (next: string) => {
    setEpisodeText("");
    setEpisodeManual(false);
    setRaced(false);
    flow.reset();
    if (next === MANUAL) {
      setSeasonManual(true);
      setSeasonText("");
    } else {
      setSeasonManual(false);
      setSeasonText(next);
    }
  };
  const chooseEpisode = (next: string) => {
    setRaced(false);
    flow.reset();
    if (next === MANUAL) {
      setEpisodeManual(true);
      setEpisodeText("");
    } else {
      setEpisodeManual(false);
      setEpisodeText(next);
    }
  };

  const send = async (choice: "block" | "version") => {
    if (!data?.target_path || season === null || episode === null) return;
    const body: FileManagerMatchRequest = {
      path,
      expected_revision: data.revision,
      managed_anime_id: animeID,
      season,
      episode,
      expected_target: data.target_path,
      conflict: choice,
    };
    const result = await flow.submit(body, (key) => matchFileManagerEntry(body, key));
    if (result.ok) {
      onDone(result.value);
      return;
    }
    if (result.code === "FILE_MANAGER_TARGET_EXISTS") setRaced(true);
    if (result.code === "FILE_MANAGER_TARGET_EXISTS" || result.code === "FILE_MANAGER_MATCH_TARGET_CHANGED" || result.code === "FILE_MANAGER_ENTRY_CHANGED") {
      void preview.refetch();
    }
  };

  // "Pick another episode": returns to episode selection and clears the current one.
  const changeEpisode = () => {
    flow.reset();
    setRaced(false);
    setEpisodeManual(false);
    setEpisodeText("");
    requestAnimationFrame(() => episodeRef.current?.focus());
  };

  const tmdbMessage = animeID === "" || !seasonsFailed
    ? null
    : seasons.error instanceof ApiError && seasons.error.code === "TMDB_BINDING_REQUIRED"
      ? t("fileManager.match.tmdbRequired")
      : t("fileManager.match.tmdbUnavailable");
  const seasonEmpty = animeID !== "" && !seasonsFailed && !seasons.isLoading && seasonList.length === 0;
  const episodeEmpty = seasonListed && !seasonManual && season !== null && !episodes.isLoading && !episodes.isError && episodeList.length === 0;

  const occupiedText =
    conflict?.occupied_by === "managed"
      ? t("fileManager.match.conflictManaged")
      : conflict?.occupied_by === "in_progress"
        ? t("fileManager.match.conflictInProgress")
        : t("fileManager.match.conflictFile");

  const canSubmit = Boolean(data) && !blocked && !flow.busy && settled && !preview.isFetching;

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="M"
      blocking
      title={t("fileManager.match.title")}
      footer={
        conflict ? (
          <>
            <Button onClick={onClose} disabled={flow.busy}>
              {t("fileManager.conflict.cancel")}
            </Button>
            <Button onClick={changeEpisode} disabled={flow.busy}>
              {t("fileManager.match.changeEpisode")}
            </Button>
            <Button variant="primary" onClick={() => void send("version")} loading={flow.busy} disabled={!canSubmit || blocked}>
              {t("fileManager.match.submitVersion")}
            </Button>
          </>
        ) : (
          <>
            <Button onClick={onClose} disabled={flow.busy}>
              {t("act.cancel")}
            </Button>
            <Button variant="primary" onClick={() => void send("block")} loading={flow.busy} disabled={!canSubmit}>
              {t("fileManager.match.submit")}
            </Button>
          </>
        )
      }
    >
      <div className="tw:grid tw:gap-3 tw:text-sm">
        <span className="tw:text-muted-foreground">{t("fileManager.match.intro")}</span>

        <Field label={t("fileManager.match.anime")}>
          <Select
            label={t("fileManager.match.anime")}
            value={animeID}
            options={animeOptions.length > 0 ? [{ value: "", label: t("fileManager.match.animePlaceholder"), disabled: true }, ...animeOptions] : [{ value: "", label: t("fileManager.match.animeEmpty"), disabled: true }]}
            onChange={chooseAnime}
            disabled={flow.busy}
          />
        </Field>

        {tmdbMessage ? <Alert tone="info" title={tmdbMessage} /> : null}

        <div className="tw:grid tw:gap-3 tw:sm:grid-cols-2">
          <div className="tw:grid tw:content-start tw:gap-2">
            {seasonListed ? (
              <Field label={t("fileManager.match.season")}>
                <Select
                  label={t("fileManager.match.season")}
                  value={showSeasonInput ? MANUAL : seasonText}
                  options={seasonOptions}
                  onChange={chooseSeason}
                  disabled={flow.busy || animeID === ""}
                />
              </Field>
            ) : null}
            {showSeasonInput ? (
              <Field label={seasonListed ? undefined : t("fileManager.match.season")}>
                <Input
                  inputMode="numeric"
                  aria-label={t("fileManager.match.season")}
                  value={seasonText}
                  onChange={(event) => {
                    setSeasonText(event.target.value);
                    setRaced(false);
                    flow.reset();
                  }}
                  disabled={flow.busy || animeID === ""}
                  aria-invalid={seasonText !== "" && season === null ? true : undefined}
                  autoComplete="off"
                />
              </Field>
            ) : null}
            {seasonText !== "" && season === null && showSeasonInput ? <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.seasonInvalid")}</span> : null}
            {seasons.isLoading ? <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.seasonLoading")}</span> : null}
          </div>

          <div className="tw:grid tw:content-start tw:gap-2">
            {episodeListed ? (
              <Field label={t("fileManager.match.episode")}>
                <Select
                  label={t("fileManager.match.episode")}
                  value={showEpisodeInput ? MANUAL : episodeText}
                  options={episodeOptions}
                  onChange={chooseEpisode}
                  disabled={flow.busy}
                />
              </Field>
            ) : null}
            {showEpisodeInput ? (
              <Field label={episodeListed ? undefined : t("fileManager.match.episode")}>
                <Input
                  ref={episodeRef}
                  inputMode="numeric"
                  aria-label={t("fileManager.match.episode")}
                  value={episodeText}
                  onChange={(event) => {
                    setEpisodeText(event.target.value);
                    setRaced(false);
                    flow.reset();
                  }}
                  disabled={flow.busy || animeID === ""}
                  aria-invalid={episodeText !== "" && episode === null ? true : undefined}
                  autoComplete="off"
                />
              </Field>
            ) : null}
            {episodeText !== "" && episode === null && showEpisodeInput ? <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.episodeInvalid")}</span> : null}
            {episodes.isLoading ? <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.episodeLoading")}</span> : null}
          </div>
        </div>
        {seasonEmpty || episodeEmpty ? <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.seasonEmpty")}</span> : null}
        <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.manualHint")}</span>

        {ready ? (
          <section aria-label={t("fileManager.match.previewHeading")} className="tw:grid tw:gap-2 tw:border-t tw:border-border tw:pt-3">
            <h4 className="tw:m-0 tw:text-sm tw:font-semibold">{t("fileManager.match.previewHeading")}</h4>
            {loadingPreview ? <span className="tw:text-xs tw:text-muted-foreground">{t("fileManager.match.previewLoading")}</span> : null}
            {previewError ? <Alert tone="error" title={previewError} /> : null}
            {data ? <PreviewBody preview={data} /> : null}
            {data?.blockers.map((blocker) => <BlockerNotice key={`${blocker.code}:${(blocker.facts ?? []).join(",")}`} blocker={blocker} />)}
            {conflict ? (
              <Alert
                tone="warning"
                title={t("fileManager.match.conflictTitle")}
                description={
                  conflict.version_path ? joinSentences(occupiedText, t("fileManager.match.conflictVersionPath", { path: conflict.version_path })) : occupiedText
                }
              />
            ) : null}
            {raced && !conflict ? <Alert tone="warning" title={t("error.FILE_MANAGER_TARGET_EXISTS")} /> : null}
          </section>
        ) : null}
        {flow.error ? <Alert tone="error" title={flow.error} /> : null}
      </div>
    </Dialog>
  );
}
