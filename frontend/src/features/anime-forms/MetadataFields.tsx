import { useState } from "react";
import { Check, Eraser, LoaderCircle, Pencil, Search, Sparkles } from "lucide-react";
import { toUserMessage, type AnimeMetadataDraft } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { metadataSearcher } from "../../lib/metadata/searcher";
import { useAuthedImage } from "../../lib/useAuthedImage";
import type { MetadataCandidate, MetadataProvider, MetadataSearcher } from "../../lib/metadata/types";
import { SegmentedControl, Button, Field, Input } from "../../ui";
import { Dialog, Select, type SelectOption } from "../../ui/base";

// The metadata binding fact rows: the resting state is only "linked ID with an external link /
// unmatched + clear". Search matching lives in MetadataMatchDialog (opened through a controlled
// matchProvider); callers outside it (e.g. the catalog card's standalone match dialog) may still
// omit matchProvider and only display/clear bindings. Applying a TMDB candidate (or an ID entry)
// writes the complete work_id/media_kind/season in one stroke, avoiding a half-made binding; the
// binding's season is TMDB's raw season number, used only for TMDB series queries.
// The season used by naming is the anime's own field (value.season, like year): with showSeason
// given, a separate "season" row renders — unset shows "not set" and can be cleared — and never
// rewrites the TMDB binding nor vice versa. Confirming a match pre-fills the chosen TMDB season
// only when the season is unset (a one-time prefill); an existing season is never overwritten.
// The TMDB row always shows the bound TMDB season inline (it may differ from the anime's
// season).

export function MetadataFields({
  value,
  onChange,
  idPrefix,
  title = "",
  searcher = metadataSearcher,
  matchProvider,
  onMatchProviderChange,
  busy = false,
  readOnly = false,
  showSeason = false,
  onSaveTotalEpisodes,
  onSaveMediaKind,
  mediaKindOverride,
  showYear = false,
  directEdit = false,
}: {
  value: AnimeMetadataDraft;
  onChange: (next: AnimeMetadataDraft) => void;
  idPrefix: string;
  title?: string;
  searcher?: MetadataSearcher;
  /** Controlled: which provider the match dialog is open for (undefined = closed). */
  matchProvider?: MetadataProvider;
  onMatchProviderChange?: (provider: MetadataProvider | undefined) => void;
  /** An inline-edit save is in flight (disables the season row's buttons, avoiding concurrency with the CAS save). */
  busy?: boolean;
  /**
   * 只读工作区（retired owner）：绑定/季度/总集数只展示生效值，不渲染匹配/清除/
   * 编辑入口。draft 与可编辑 owner 不受影响。
   */
  readOnly?: boolean;
  /**
   * 渲染独立季度行。季度是表单值（value.season，null = 未设置）：经 onChange 写回，
   * 随调用方的保存落库；与 TMDB 绑定的季号互不改写。
   */
  showSeason?: boolean;
  /** When given, renders a separate total-episodes row; returning true means the save succeeded (the row leaves editing). */
  onSaveTotalEpisodes?: (total: number | null) => Promise<boolean>;
  /** When given, renders a separate media-kind row; returning true means the save succeeded (the row leaves editing). */
  onSaveMediaKind?: (next: "tv" | "movie" | null) => Promise<boolean>;
  /**
   * 类型针的显示值（tv|movie|null）。刻意独立于 value：owner 模式的类型针与标题覆盖
   * 一样不进 AnimeMetadataDraft（否则「保存后 refetch 前」的本地值与服务端基线不等，
   * 会误报 dirty），由调用方用独立状态传入。draft 模式传草稿里的字段。
   */
  mediaKindOverride?: "tv" | "movie" | null;
  /**
   * 渲染年份行。年份是表单值（value.year）：输入直接经 onChange 写回，随调用方的
   * 保存落库（owner=底部保存，draft=草稿）；TMDB 匹配确认只在年份为空时填入候选年份。
   */
  showYear?: boolean;
  /**
   * 直接编辑：类型/季度/总集数行不再有「编辑 → 保存」，控件常显，合法取值即经
   * onSave* 写回调用方的草稿（配置弹窗的单一保存模型）。不合法输入只留在输入框
   * 并内联提示，不写回。缺省为旧的逐行保存交互（一次性下载等调用方不受影响）。
   */
  directEdit?: boolean;
}) {
  const season = value.season ?? null;
  const effectiveKind = mediaKindOverride ?? value.tmdb?.media_kind;
  return (
    <div className="metadata-stack">
      <MetadataBindingRow
        idPrefix={`${idPrefix}-bgm`}
        provider="bgm"
        label={t("forms.bangumi")}
        readOnly={readOnly}
        selected={value.bgm ? { workId: value.bgm.work_id } : undefined}
        onClear={() => onChange({ ...value, bgm: null })}
        onMatch={() => onMatchProviderChange?.("bgm")}
      />
      <MetadataBindingRow
        idPrefix={`${idPrefix}-tmdb`}
        provider="tmdb"
        label={t("forms.tmdb")}
        readOnly={readOnly}
        selected={
          value.tmdb
            ? { workId: value.tmdb.work_id, mediaKind: value.tmdb.media_kind, season: value.tmdb.season }
            : undefined
        }
        onClear={() => onChange({ ...value, tmdb: null })}
        onMatch={() => onMatchProviderChange?.("tmdb")}
      />
      {onSaveMediaKind && directEdit && (
        <DirectMediaKindRow
          idPrefix={idPrefix}
          mediaKindOverride={mediaKindOverride}
          tmdbMediaKind={value.tmdb?.media_kind}
          disabled={busy}
          onChange={(next) => void onSaveMediaKind(next)}
        />
      )}
      {onSaveMediaKind && !directEdit && (
        <MediaKindRow
          idPrefix={idPrefix}
          mediaKindOverride={mediaKindOverride}
          tmdbMediaKind={value.tmdb?.media_kind}
          busy={busy}
          onSaveMediaKind={onSaveMediaKind}
        />
      )}
      {showSeason && (readOnly || directEdit) && (
        <DirectSeasonRow
          idPrefix={idPrefix}
          season={season}
          mediaKind={effectiveKind}
          readOnly={readOnly}
          disabled={busy}
          onChange={(next) => onChange({ ...value, season: next })}
        />
      )}
      {showSeason && !readOnly && !directEdit && (
        <SeasonRow
          idPrefix={idPrefix}
          season={season}
          mediaKind={effectiveKind}
          busy={busy}
          onSaveSeason={async (next) => {
            onChange({ ...value, season: next });
            return true;
          }}
        />
      )}
      {showYear && (
        <YearRow
          idPrefix={idPrefix}
          year={value.year ?? null}
          readOnly={readOnly}
          busy={busy}
          onChange={(year) => onChange({ ...value, year })}
        />
      )}
      {onSaveTotalEpisodes && directEdit && (
        <DirectTotalEpisodesRow
          idPrefix={idPrefix}
          totalEpisodes={value.total_episodes ?? null}
          disabled={busy}
          onChange={(total) => void onSaveTotalEpisodes(total)}
        />
      )}
      {onSaveTotalEpisodes && !directEdit && (
        <TotalEpisodesRow
          idPrefix={idPrefix}
          totalEpisodes={value.total_episodes ?? null}
          busy={busy}
          onSaveTotalEpisodes={onSaveTotalEpisodes}
        />
      )}
      {matchProvider !== undefined && onMatchProviderChange && (
        <MetadataMatchDialog
          provider={matchProvider}
          title={title}
          searcher={searcher}
          preferredSeason={season ?? undefined}
          onClose={() => onMatchProviderChange(undefined)}
          onSelectBGM={(candidate) => {
            onChange({ ...value, bgm: { work_id: candidate.workId } });
            onMatchProviderChange(undefined);
          }}
          onSelectTMDB={(candidate, mediaKind, tmdbSeason) => {
            // An empty total-episodes pre-fills from the chosen season's count
            // (seasons[].episodeCount in the candidate detail), prefill only: the user can still
            // edit or clear it in the row. Movies are not prefilled.
            const seasonEpisodes =
              mediaKind === "tv" ? candidate.seasons?.find((s) => s.seasonNumber === tmdbSeason)?.episodeCount : undefined;
            const next: AnimeMetadataDraft = {
              ...value,
              tmdb: { work_id: candidate.workId, media_kind: mediaKind, season: tmdbSeason },
            };
            if (value.total_episodes == null && seasonEpisodes && seasonEpisodes > 0) {
              next.total_episodes = seasonEpisodes;
            }
            // The season pre-fills once from the chosen TMDB season only while unset (the same
            // fill-if-empty as year); an existing season is never overwritten and the two stay
            // independent afterwards. Movies have no season and are not prefilled.
            if (value.season == null && mediaKind === "tv") {
              next.season = tmdbSeason;
            }
            // Year follows the same rule as the server's backfill (fill-if-empty): the candidate
            // year fills only while the form's year is empty, an existing year is never
            // overwritten, and no year is invented when the candidate has none. It belongs to the
            // user afterwards.
            if (value.year == null && candidate.year !== undefined && validYear(candidate.year)) {
              next.year = candidate.year;
            }
            onChange(next);
            onMatchProviderChange(undefined);
          }}
        />
      )}
    </div>
  );
}

type SelectedMetadata = { workId: string; mediaKind?: "tv" | "movie"; season?: number };

function providerWorkUrl(provider: MetadataProvider, selected: SelectedMetadata): string {
  if (provider === "bgm") return `https://bgm.tv/subject/${selected.workId}`;
  return `https://www.themoviedb.org/${selected.mediaKind === "movie" ? "movie" : "tv"}/${selected.workId}`;
}

function MetadataBindingRow({
  idPrefix,
  provider,
  label,
  selected,
  readOnly = false,
  onClear,
  onMatch,
}: {
  idPrefix: string;
  provider: MetadataProvider;
  label: string;
  selected?: SelectedMetadata;
  /** A read-only workspace: no match/clear buttons render, only the bound values. */
  readOnly?: boolean;
  onClear: () => void;
  onMatch?: () => void;
}) {
  return (
    <section className="metadata-card" aria-labelledby={`${idPrefix}-label`}>
      <div className="metadata-header">
        <span id={`${idPrefix}-label`} className="metadata-title text-body">
          {label}
        </span>
        {selected ? (
          <a
            href={providerWorkUrl(provider, selected)}
            target="_blank"
            rel="noopener noreferrer"
            className="metadata-status-online text-caption"
          >
            <Check className="metadata-icon" />
            {selected.workId}
            {selected.mediaKind && <span>· {selected.mediaKind === "movie" ? t("forms.movie") : t("forms.tv")}</span>}
            {selected.mediaKind === "tv" && selected.season !== undefined && selected.season > 0 && (
              <span>· {t("forms.seasonN", { n: selected.season })}</span>
            )}
            {selected.mediaKind === "tv" && selected.season === 0 && <span>· SP</span>}
          </a>
        ) : (
          <span className="metadata-status-warning text-caption">{t("forms.unmatched")}</span>
        )}
        <div className="metadata-actions">
          {!readOnly && onMatch && (
            <Button size="sm" variant="secondary" onClick={onMatch}>
              {t("forms.matchProvider", { provider: provider === "bgm" ? t("forms.providerBangumi") : t("forms.providerTmdb") })}
            </Button>
          )}
          {!readOnly && selected && (
            <Button size="sm" variant="ghost" onClick={onClear} aria-label={t("forms.clearAria", { label })}>
              <Eraser className="metadata-icon" />
              {t("forms.clear")}
            </Button>
          )}
        </div>
      </div>
    </section>
  );
}

// --- The standalone season row (modeled on the v1 MatchSettingsCard) --------------------
//
// The season is the anime's own field (value.season, the only source of the anime.season naming
// fact) and is independent of the TMDB binding: unset shows "not set" (never faked as Season 1),
// and an empty input = clearing to unset. "Edit" enters editing: a number input + an SP shortcut
// + save/cancel, saving back into the form value. A movie has no season and the row degrades to
// explanatory text; an unknown media kind stays editable like a series.

/** The season's display text: not set, specials (season 0), or season n. */
function seasonLabel(season: number | null): string {
  if (season === null) return t("forms.seasonUnset");
  return season === 0 ? t("forms.seasonSpecials") : t("forms.seasonN", { n: season });
}

function SeasonRow({
  idPrefix,
  season,
  mediaKind,
  busy = false,
  onSaveSeason,
}: {
  idPrefix: string;
  /** The current season; null = not set. */
  season: number | null;
  /** The effective media kind (the kind pin or the TMDB binding's kind); a movie degrades the row to explanation. */
  mediaKind?: "tv" | "movie";
  busy?: boolean;
  onSaveSeason: (season: number | null) => Promise<boolean>;
}) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const [error, setError] = useState<string>();

  function startEdit() {
    setText(season === null ? "" : String(season));
    setError(undefined);
    setEditing(true);
  }

  async function save() {
    const trimmed = text.trim();
    const parsed = trimmed === "" ? null : Number(trimmed);
    if (parsed !== null && (!/^\d+$/.test(trimmed) || !validSeason(parsed))) {
      setError(t("forms.seasonInvalid"));
      return;
    }
    if (await onSaveSeason(parsed)) {
      setEditing(false);
      setError(undefined);
    }
  }

  return (
    <section className="metadata-card" aria-labelledby={`${idPrefix}-season-label`}>
      <div className="metadata-header">
        <span id={`${idPrefix}-season-label`} className="metadata-title text-body">
          {t("forms.season")}
        </span>
        {mediaKind === "movie" ? (
          <span className="metadata-muted text-caption">{t("forms.movieNoSeason")}</span>
        ) : editing ? (
          <>
            <Input
              aria-label={t("forms.seasonAria")}
              type="number"
              min={0}
              max={99}
              value={text}
              placeholder={t("forms.seasonPlaceholder")}
              aria-invalid={error !== undefined}
              onChange={(event) => {
                setText(event.target.value);
                setError(undefined);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void save();
                } else if (event.key === "Escape") {
                  setEditing(false);
                  setError(undefined);
                }
              }}
              controlSize="sm"
            />
            <Button
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => {
                setText("0");
                setError(undefined);
              }}
            >
              SP
            </Button>
            <Button size="sm" variant="primary" disabled={busy} onClick={() => void save()}>
              {busy ? t("act.saving") : t("forms.saveSeason")}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>
              {t("act.cancel")}
            </Button>
          </>
        ) : (
          <>
            <span className="metadata-row-value text-body">{seasonLabel(season)}</span>
            <Button size="sm" variant="ghost" className="metadata-edit-action" disabled={busy} onClick={startEdit} aria-label={t("forms.editSeasonAria")}>
              <Pencil className="metadata-icon" />
              {t("act.edit")}
            </Button>
          </>
        )}
      </div>
      {error && (
        <p role="alert" className="metadata-row-error text-caption">
          {error}
        </p>
      )}
    </section>
  );
}


// --- The standalone media-kind row (series / movie) --------------------------------
//
// media_kind_override is the pin of the naming branch: it decides whether files land on the
// "series" or "movie" naming path. While unset it follows the TMDB binding's media_kind, and
// with no TMDB binding it is treated as a series (the backend FrozenMediaBinding's fallback
// chain; the frontend never re-derives it). Row interaction matches the season/total-episodes
// rows: view -> edit -> save/cancel. Editing is a three-way segment: follow the match (saving
// null = clearing the pin) / series / movie. Viewing presents only the server's value: with a
// pin, series or movie; unset shows "follow match", plus the binding's kind as reference when
// TMDB is bound (the binding kind is server data).

function MediaKindRow({
  idPrefix,
  mediaKindOverride,
  tmdbMediaKind,
  busy = false,
  onSaveMediaKind,
}: {
  idPrefix: string;
  /** The set pin; undefined/null = not set (follow the match). */
  mediaKindOverride?: "tv" | "movie" | null;
  /** The kind the TMDB binding itself carries, shown only as reference while unset. */
  tmdbMediaKind?: "tv" | "movie";
  busy?: boolean;
  onSaveMediaKind: (next: "tv" | "movie" | null) => Promise<boolean>;
}) {
  const [editing, setEditing] = useState(false);
  const [choice, setChoice] = useState<"auto" | "tv" | "movie">("auto");

  function startEdit() {
    setChoice(mediaKindOverride ?? "auto");
    setEditing(true);
  }

  async function save() {
    if (await onSaveMediaKind(choice === "auto" ? null : choice)) {
      setEditing(false);
    }
  }

  const kindLabel = (kind: "tv" | "movie") => (kind === "movie" ? t("forms.movie") : t("forms.tv"));
  const pinned = mediaKindOverride ?? undefined;

  return (
    <section className="metadata-card" aria-labelledby={`${idPrefix}-media-kind-label`}>
      <div className="metadata-header">
        <span id={`${idPrefix}-media-kind-label`} className="metadata-title text-body">
          {t("forms.mediaKind")}
        </span>
        {editing ? (
          <>
            <SegmentedControl
              ariaLabel={t("forms.mediaKindAria")}
              compact
              value={choice}
              onChange={setChoice}
              options={[
                { value: "auto", label: t("forms.mediaKindFollow") },
                { value: "tv", label: t("forms.tv") },
                { value: "movie", label: t("forms.movie") },
              ]}
            />
            <Button size="sm" variant="primary" disabled={busy} onClick={() => void save()}>
              {busy ? t("act.saving") : t("forms.saveMediaKind")}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>
              {t("act.cancel")}
            </Button>
          </>
        ) : (
          <>
            <span className="metadata-row-value text-body">
              {pinned ? kindLabel(pinned) : t("forms.mediaKindFollow")}
            </span>
            {!pinned && tmdbMediaKind && (
              <span className="metadata-muted text-caption">{kindLabel(tmdbMediaKind)}</span>
            )}
            <Button size="sm" variant="ghost" className="metadata-edit-action" disabled={busy} onClick={startEdit} aria-label={t("forms.editMediaKindAria")}>
              <Pencil className="metadata-icon" />
              {t("act.edit")}
            </Button>
          </>
        )}
      </div>
    </section>
  );
}

// --- The standalone total-episodes row (modeled on SeasonRow) ------------------------
//
// total_episodes is the anime's planned total, used by the progress bar and the completion
// judgment. metadata_match auto-backfills it from the BGM subject eps while the column is NULL;
// manual edits override it. Row interaction matches the season row: view -> edit -> save/cancel.
// An empty input = clearing to NULL (back to unknown, so the next metadata_match backfills
// again). 0 is a valid value (specials collections etc.).

function TotalEpisodesRow({
  idPrefix,
  totalEpisodes,
  busy = false,
  onSaveTotalEpisodes,
}: {
  idPrefix: string;
  /** The stored total episodes; null = unknown. */
  totalEpisodes: number | null;
  busy?: boolean;
  onSaveTotalEpisodes: (total: number | null) => Promise<boolean>;
}) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const [error, setError] = useState<string>();

  function startEdit() {
    setText(totalEpisodes !== null ? String(totalEpisodes) : "");
    setError(undefined);
    setEditing(true);
  }

  async function save() {
    const trimmed = text.trim();
    // An empty input = clearing to NULL (back to unknown, letting metadata_match backfill again).
    if (trimmed === "") {
      if (await onSaveTotalEpisodes(null)) {
        setEditing(false);
        setError(undefined);
      }
      return;
    }
    const parsed = Number(trimmed);
    if (!Number.isInteger(parsed) || parsed < 0) {
      setError(t("forms.totalInvalid"));
      return;
    }
    if (await onSaveTotalEpisodes(parsed)) {
      setEditing(false);
      setError(undefined);
    }
  }

  return (
    <section className="metadata-card" aria-labelledby={`${idPrefix}-total-eps-label`}>
      <div className="metadata-header">
        <span id={`${idPrefix}-total-eps-label`} className="metadata-title text-body">
          {t("forms.totalEpisodes")}
        </span>
        {editing ? (
          <>
            <Input
              aria-label={t("forms.totalEpisodes")}
              type="number"
              min={0}
              value={text}
              placeholder={t("forms.totalPlaceholder")}
              aria-invalid={error !== undefined}
              onChange={(event) => {
                setText(event.target.value);
                setError(undefined);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void save();
                } else if (event.key === "Escape") {
                  setEditing(false);
                  setError(undefined);
                }
              }}
              controlSize="sm"
              fullWidth={false}
              className="tw:w-24"
            />
            <Button size="sm" variant="primary" disabled={busy} onClick={() => void save()}>
              {busy ? t("act.saving") : t("forms.saveTotal")}
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>
              {t("act.cancel")}
            </Button>
          </>
        ) : (
          <>
            <span className="metadata-row-value text-body">
              {totalEpisodes !== null ? t("forms.episodesCount", { n: totalEpisodes }) : t("forms.unknown")}
            </span>
            <Button size="sm" variant="ghost" className="metadata-edit-action" disabled={busy} onClick={startEdit} aria-label={t("forms.editTotalAria")}>
              <Pencil className="metadata-icon" />
              {t("act.edit")}
            </Button>
          </>
        )}
      </div>
      {error && (
        <p role="alert" className="metadata-row-error text-caption">
          {error}
        </p>
      )}
    </section>
  );
}


// --- Direct-edit rows (the single-save model: values write the draft; the dialog footer saves once) ---

function DirectMediaKindRow({
  idPrefix,
  mediaKindOverride,
  tmdbMediaKind,
  disabled,
  onChange,
}: {
  idPrefix: string;
  mediaKindOverride?: "tv" | "movie" | null;
  tmdbMediaKind?: "tv" | "movie";
  disabled: boolean;
  onChange: (next: "tv" | "movie" | null) => void;
}) {
  const labelId = `${idPrefix}-media-kind-label`;
  const choice = mediaKindOverride ?? "auto";
  return (
    <section className="metadata-card" aria-labelledby={labelId}>
      <div className="metadata-header">
        <span id={labelId} className="metadata-title text-body">
          {t("forms.mediaKind")}
        </span>
        <SegmentedControl
          ariaLabel={t("forms.mediaKindAria")}
          value={choice}
          onChange={(next) => {
            if (!disabled) onChange(next === "auto" ? null : next);
          }}
          options={[
            { value: "auto", label: t("forms.mediaKindFollow") },
            { value: "tv", label: t("forms.tv") },
            { value: "movie", label: t("forms.movie") },
          ]}
        />
        {choice === "auto" && tmdbMediaKind && (
          <span className="metadata-muted text-caption">{tmdbMediaKind === "movie" ? t("forms.movie") : t("forms.tv")}</span>
        )}
      </div>
    </section>
  );
}

// The season's direct-edit row: a valid input (or clearing = unset) writes back into the form
// value immediately, while an invalid one stays in the input and reports on blur. A read-only
// workspace shows only the season or "not set".
function DirectSeasonRow({
  idPrefix,
  season,
  mediaKind,
  readOnly = false,
  disabled,
  onChange,
}: {
  idPrefix: string;
  /** The current season; null = not set. */
  season: number | null;
  mediaKind?: "tv" | "movie";
  readOnly?: boolean;
  disabled: boolean;
  onChange: (season: number | null) => void;
}) {
  const [pendingText, setPendingText] = useState<string>();
  const [error, setError] = useState<string>();
  const text = pendingText ?? (season === null ? "" : String(season));
  const labelId = `${idPrefix}-season-label`;

  function change(next: string) {
    setError(undefined);
    const trimmed = next.trim();
    if (trimmed === "") {
      setPendingText(undefined);
      onChange(null);
      return;
    }
    const parsed = Number(trimmed);
    if (/^\d+$/.test(trimmed) && validSeason(parsed)) {
      setPendingText(undefined);
      onChange(parsed);
      return;
    }
    setPendingText(next);
  }

  return (
    <section className="metadata-card" aria-labelledby={labelId}>
      <div className="metadata-header">
        <span id={labelId} className="metadata-title text-body">
          {t("forms.season")}
        </span>
        {mediaKind === "movie" ? (
          <span className="metadata-muted text-caption">{t("forms.movieNoSeason")}</span>
        ) : readOnly ? (
          <span className="metadata-row-value text-body">{seasonLabel(season)}</span>
        ) : (
          <>
            <Input
              aria-label={t("forms.seasonAria")}
              inputMode="numeric"
              value={text}
              placeholder={t("forms.seasonUnset")}
              aria-invalid={error !== undefined}
              disabled={disabled}
              onChange={(event) => change(event.target.value)}
              onBlur={() => {
                if (pendingText !== undefined) setError(t("forms.seasonInvalid"));
              }}
              controlSize="sm"
              fullWidth={false}
              className="tw:w-24"
            />
            <Button size="sm" variant="secondary" disabled={disabled} onClick={() => change("0")}>
              SP
            </Button>
          </>
        )}
      </div>
      {error && (
        <p role="alert" className="metadata-row-error text-caption">
          {error}
        </p>
      )}
    </section>
  );
}

function DirectTotalEpisodesRow({
  idPrefix,
  totalEpisodes,
  disabled,
  onChange,
}: {
  idPrefix: string;
  totalEpisodes: number | null;
  disabled: boolean;
  onChange: (total: number | null) => void;
}) {
  const [pendingText, setPendingText] = useState<string>();
  const [error, setError] = useState<string>();
  const text = pendingText ?? (totalEpisodes !== null ? String(totalEpisodes) : "");
  const labelId = `${idPrefix}-total-eps-label`;

  // An empty input = clearing to unknown (a later metadata match backfills again); 0 is valid (specials collections etc.).
  function change(next: string) {
    setError(undefined);
    const trimmed = next.trim();
    if (trimmed === "") {
      setPendingText(undefined);
      onChange(null);
      return;
    }
    if (/^\d+$/.test(trimmed)) {
      setPendingText(undefined);
      onChange(Number(trimmed));
      return;
    }
    setPendingText(next);
  }

  return (
    <section className="metadata-card" aria-labelledby={labelId}>
      <div className="metadata-header">
        <span id={labelId} className="metadata-title text-body">
          {t("forms.totalEpisodes")}
        </span>
        <Input
          aria-labelledby={labelId}
          inputMode="numeric"
          value={text}
          placeholder={t("forms.totalPlaceholder")}
          aria-invalid={error !== undefined}
          disabled={disabled}
          onChange={(event) => change(event.target.value)}
          onBlur={() => {
            if (pendingText !== undefined) setError(t("forms.totalInvalid"));
          }}
          controlSize="sm"
          fullWidth={false}
          className="tw:w-24"
        />
      </div>
      {error && (
        <p role="alert" className="metadata-row-error text-caption">
          {error}
        </p>
      )}
    </section>
  );
}

const MIN_YEAR = 1900;
const MAX_YEAR = 2100;

function validYear(value: number): boolean {
  return Number.isInteger(value) && value >= MIN_YEAR && value <= MAX_YEAR;
}

// The year row: a form field with no row save. A valid input (or a clearing) writes back into
// the form value immediately via onChange; an invalid one stays in the input and reports on
// blur, never written back.
function YearRow({
  idPrefix,
  year,
  readOnly,
  busy,
  onChange,
}: {
  idPrefix: string;
  year: number | null;
  readOnly: boolean;
  busy: boolean;
  onChange: (year: number | null) => void;
}) {
  // The invalid text being typed; undefined means the input mirrors the form value directly (including a match-filled year).
  const [pendingText, setPendingText] = useState<string>();
  const [error, setError] = useState<string>();
  const text = pendingText ?? (year !== null ? String(year) : "");
  const labelId = `${idPrefix}-year-label`;

  function change(next: string) {
    setError(undefined);
    const trimmed = next.trim();
    if (trimmed === "") {
      setPendingText(undefined);
      onChange(null);
      return;
    }
    const parsed = Number(trimmed);
    if (/^\d+$/.test(trimmed) && validYear(parsed)) {
      setPendingText(undefined);
      onChange(parsed);
      return;
    }
    setPendingText(next);
  }

  return (
    <section className="metadata-card" aria-labelledby={labelId}>
      <div className="metadata-header">
        <span id={labelId} className="metadata-title text-body">
          {t("forms.year")}
        </span>
        {readOnly ? (
          <span className="metadata-row-value text-body">{year !== null ? String(year) : t("forms.unknown")}</span>
        ) : (
          <Input
            aria-labelledby={labelId}
            inputMode="numeric"
            value={text}
            placeholder={t("forms.yearPlaceholder")}
            aria-invalid={error !== undefined}
            disabled={busy}
            onChange={(event) => change(event.target.value)}
            onBlur={() => {
              if (pendingText !== undefined) setError(t("forms.yearInvalid"));
            }}
            controlSize="sm"
            fullWidth={false}
            className="tw:w-24"
          />
        )}
      </div>
      {error && (
        <p role="alert" className="metadata-row-error text-caption">
          {error}
        </p>
      )}
    </section>
  );
}

// --- Match dialog --------------------------------------------------------------
//
// The standalone match dialog (MatchDialog): title search → candidate list → confirm selection.
// The TMDB section additionally carries the media type (tv/movie) and TMDB season editing: the
// initial value prefers the anime's own set season (preferredSeason), otherwise the
// suggestedSeason among the candidate's real seasons; a movie has no season (season 0 is sent).
// tv groups real seasons and custom seasons, with invalid input reported inline. Confirming the
// match writes the TMDB season back with the binding tuple (the anime's season is only prefilled
// while unset, see above). TMDB also supports "enter by ID": after a local positive-integer
// check it is selected as a candidate that is never verified online; pick the media type and the
// TMDB season the same way, then confirm — saving works even with no configured TMDB key.

/** A TMDB work ID: a positive integer (local format check, never verified online). */
const TMDB_ID_PATTERN = /^[1-9]\d{0,11}$/;

const PROVIDER_DIALOG_LABELS: Record<MetadataProvider, string> = { bgm: "Bangumi", tmdb: "TMDB" };

function validSeason(value: number): boolean {
  return Number.isInteger(value) && value >= 0 && value <= 99;
}

/** The candidate poster: the server gives a same-origin proxy path that must be fetched as a blob through the session; a placeholder shows when there is no image or the load fails. */
function CandidatePoster({ url }: { url?: string }) {
  const src = useAuthedImage(url);
  return src ? (
    <img src={src} alt="" className="metadata-match-poster" />
  ) : (
    <div className="metadata-match-poster metadata-match-poster-placeholder" aria-hidden />
  );
}

function MetadataMatchDialog({
  provider,
  title,
  searcher,
  preferredSeason,
  onClose,
  onSelectBGM,
  onSelectTMDB,
}: {
  provider: MetadataProvider;
  title: string;
  searcher: MetadataSearcher;
  /** The season row's unbound staging preference: when present it wins over the candidate inference as the season editor's initial value. */
  preferredSeason?: number;
  onClose: () => void;
  onSelectBGM: (candidate: MetadataCandidate) => void;
  onSelectTMDB: (candidate: MetadataCandidate, mediaKind: "tv" | "movie", season: number) => void;
}) {
  const [query, setQuery] = useState(title);
  const [candidates, setCandidates] = useState<MetadataCandidate[]>([]);
  const [selected, setSelected] = useState<MetadataCandidate>();
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState(false);
  const [error, setError] = useState<string>();
  // TMDB season editors (drafts; commit on confirm).
  const [mediaKind, setMediaKind] = useState<"tv" | "movie">("tv");
  const [seasonSelect, setSeasonSelect] = useState("custom");
  const [seasonCustom, setSeasonCustom] = useState("");
  // Enter by ID (TMDB only).
  const [manualId, setManualId] = useState("");
  const [manualError, setManualError] = useState<string>();

  async function search(queryOverride = query) {
    const nextQuery = queryOverride.trim();
    if (!nextQuery || searching) return;
    setQuery(nextQuery);
    setSearching(true);
    setSearched(true);
    setError(undefined);
    setSelected(undefined);
    try {
      setCandidates(await searcher.search(provider, nextQuery));
    } catch (cause) {
      setCandidates([]);
      setError(toUserMessage(cause, t("forms.queryFailed")));
    } finally {
      setSearching(false);
    }
  }

  function applyManualId() {
    const id = manualId.trim();
    if (!TMDB_ID_PATTERN.test(id)) {
      setManualError(t("forms.tmdbIdInvalid"));
      return;
    }
    setManualError(undefined);
    pick({ provider: "tmdb", workId: id, title: t("forms.manualTmdbTitle", { id }), detailsStatus: "not_applicable" });
  }

  function pick(candidate: MetadataCandidate) {
    setSelected(candidate);
    const kind = candidate.mediaKind ?? "tv";
    setMediaKind(kind);
    const realSeasonNumbers = new Set((candidate.seasons ?? []).map((season) => season.seasonNumber));
    const inferred = preferredSeason ?? candidate.suggestedSeason;
    if (inferred !== undefined && realSeasonNumbers.has(inferred)) {
      setSeasonSelect(String(inferred));
      setSeasonCustom("");
    } else if (preferredSeason !== undefined || inferred !== undefined) {
      setSeasonSelect("custom");
      setSeasonCustom(String(inferred));
    } else {
      setSeasonSelect("custom");
      setSeasonCustom("");
    }
  }

  const seasonParsed =
    seasonSelect === "sp"
      ? 0
      : seasonSelect === "custom"
        ? seasonCustom.trim() === ""
          ? Number.NaN
          : Number(seasonCustom)
        : Number(seasonSelect);
  const seasonValid = provider !== "tmdb" || mediaKind === "movie" || validSeason(seasonParsed);
  const seasonOptions: SelectOption<string>[] = [
    ...(selected?.seasons ?? []).map((season) => ({
      value: String(season.seasonNumber),
      label: t("forms.seasonOption", { n: season.seasonNumber, name: season.name || t("forms.unnamed"), count: season.episodeCount }),
      group: t("forms.realSeasons"),
    })),
    { value: "custom", label: t("forms.customSeason"), group: t("forms.customSeason") },
  ];

  function confirm() {
    if (!selected) return;
    if (provider === "bgm") {
      onSelectBGM(selected);
      return;
    }
    if (!seasonValid) return;
    onSelectTMDB(selected, mediaKind, mediaKind === "movie" ? 0 : seasonParsed);
  }

  return (
    <Dialog
      open
      onRequestClose={onClose}
      tier="M"
      title={t("forms.matchTitle", { provider: PROVIDER_DIALOG_LABELS[provider] })}
      footer={
        <>
          <Button onClick={onClose}>{t("act.cancel")}</Button>
          <Button variant="primary" disabled={!selected || !seasonValid} onClick={confirm}>
            {t("forms.confirmMatch")}
          </Button>
        </>
      }
    >
      <div className="metadata-match-stack">
        <p className="metadata-match-description">
          {provider === "tmdb" ? t("forms.matchDescTmdb") : t("forms.matchDescBgm")}
        </p>

        <div className="metadata-match-search">
          <Field label={t("forms.titleQuery")} className="tw:flex-1">
            <Input
              aria-label={t("forms.titleQuery")}
              value={query}
              placeholder={t("forms.titlePlaceholder")}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void search();
                }
              }}
            />
          </Field>
          <Button
            className="metadata-match-search-action"
            size="sm"
            onClick={() => void search()}
            disabled={searching || query.trim() === ""}
          >
            {searching ? <LoaderCircle className="metadata-match-action-icon metadata-match-spinner" /> : <Search className="metadata-match-action-icon" />}
            {t("forms.search")}
          </Button>
          <Button
            className="metadata-match-search-action"
            size="sm"
            variant="ghost"
            onClick={() => void search(title)}
            disabled={searching || title.trim() === ""}
          >
            <Sparkles className="metadata-match-action-icon" />
            {t("forms.autoSearch")}
          </Button>
        </div>

        {provider === "tmdb" && (
          <div className="metadata-match-search">
            <Field label={t("forms.manualTmdbId")} className="tw:flex-1">
              <Input
                aria-label={t("forms.tmdbIdAria")}
                inputMode="numeric"
                value={manualId}
                placeholder={t("forms.tmdbIdPlaceholder")}
                aria-invalid={manualError !== undefined}
                onChange={(event) => {
                  setManualId(event.target.value);
                  setManualError(undefined);
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    applyManualId();
                  }
                }}
              />
            </Field>
            <Button className="metadata-match-search-action" size="sm" onClick={applyManualId} disabled={manualId.trim() === ""}>
              {t("forms.useTmdbId")}
            </Button>
          </div>
        )}
        {manualError && (
          <p role="alert" className="metadata-match-error">
            {manualError}
          </p>
        )}

        {error && (
          <p role="alert" className="metadata-match-error">
            {error}
          </p>
        )}
        {searched && !searching && !error && candidates.length === 0 && (
          <p className="metadata-match-empty">
            {t("forms.noCandidates")}
          </p>
        )}

        {candidates.length > 0 && (
          <div className="metadata-match-candidates" role="listbox" aria-label={t("forms.candidatesAria")}>
            {candidates.map((candidate) => {
              const active = selected?.workId === candidate.workId;
              return (
                <button
                  key={`${candidate.provider}:${candidate.workId}`}
                  type="button"
                  role="option"
                  aria-selected={active}
                  onClick={() => pick(candidate)}
                  className={active ? "metadata-match-option metadata-match-option-active" : "metadata-match-option"}
                >
                  <CandidatePoster url={candidate.posterUrl} />
                  <span className="metadata-match-option-content">
                    <span className="metadata-match-title">
                      {candidate.title}
                    </span>
                    {candidate.originalTitle && (
                      <span className="metadata-match-original-title">
                        {candidate.originalTitle}
                      </span>
                    )}
                    <span className="metadata-match-meta">
                      {candidate.year || t("forms.yearUnknown")}
                      {candidate.mediaKind ? ` · ${candidate.mediaKind === "movie" ? t("forms.movie") : t("forms.tv")}` : ""}
                      {provider === "tmdb" && candidate.mediaKind !== "movie" && candidate.suggestedSeason !== undefined
                        ? ` · ${t("forms.suggestedSeason", { n: candidate.suggestedSeason })}`
                        : ""}
                    </span>
                  </span>
                  {active && <Check className="metadata-match-selected-icon" />}
                </button>
              );
            })}
          </div>
        )}

        {provider === "tmdb" && selected && (
          <div className="metadata-match-selected">
            <span className="metadata-match-selected-summary">
              {t("forms.selectedPrefix")} <span className="metadata-match-selected-title">{selected.title}</span>{t("forms.selectedSuffix")}
            </span>
            {selected.detailsStatus === "not_applicable" && (
              <p className="metadata-match-season-status">{t("forms.manualTmdbNote")}</p>
            )}
            <div className="metadata-match-editors">
              <SegmentedControl
                ariaLabel={t("forms.mediaKindAria")}
                value={mediaKind}
                onChange={setMediaKind}
                options={[
                  { value: "tv", label: t("forms.tv") },
                  { value: "movie", label: t("forms.movie") },
                ]}
              />
              {mediaKind === "tv" ? (
                <>
                  {selected.detailsStatus === "empty" && (
                    <p className="metadata-match-season-status">{t("forms.noSeasonInfo")}</p>
                  )}
                  {selected.detailsStatus === "error" && (
                    <p role="alert" className="metadata-match-season-status">
                      {t("forms.seasonInfoFailed")}
                    </p>
                  )}
                  <label className="metadata-match-season-field">
                    {t("forms.tmdbSeason")}
                    <Select
                      label={t("forms.applySeason")}
                      value={seasonSelect}
                      onChange={setSeasonSelect}
                      options={seasonOptions}
                    />
                  </label>
                  {seasonSelect === "custom" && (
                    <Input
                      aria-label={t("forms.customSeasonAria")}
                      type="number"
                      min={0}
                      max={99}
                      value={seasonCustom}
                      aria-invalid={!seasonValid}
                      onChange={(event) => setSeasonCustom(event.target.value)}
                      controlSize="sm"
                      fullWidth={false}
                      className="tw:w-20"
                    />
                  )}
                </>
              ) : (
                <span className="metadata-match-movie-note">{t("forms.movieNoSeason")}{t("common.format.sentenceEnd")}</span>
              )}
            </div>
            {mediaKind === "tv" && !seasonValid && (
              <p role="alert" className="metadata-match-validation-error">
                {t("forms.seasonInvalid")}
              </p>
            )}
          </div>
        )}
      </div>
    </Dialog>
  );
}
