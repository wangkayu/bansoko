export type FileEpisodeOverride = {
  path: string;
  /** Omitted means the file keeps the main season setting. */
  season?: number;
  episode: number;
  /**
   * Consecutive range end for a combined-episode file (`1-2` covers episodes 1
   * and 2). Omitted or equal to `episode` is the single-episode form.
   */
  episode_end?: number;
};

/** One file may cover at most this many consecutive episodes by manual input. */
export const maxManualEpisodesPerFile = 12;

export type ParsedEpisodeInput = { episode: number; episode_end?: number };

/**
 * Parses the per-row EP input: a single integer (`1`) or a consecutive range
 * (`1-2`). Anything else — inverted ends, a span over the manual maximum,
 * prefixed forms like `ep1-2` — is invalid. An equal start and end collapses
 * to the single-episode form so its request stays identical to omitting it.
 */
export function parseEpisodeInput(text: string): ParsedEpisodeInput | undefined {
  const match = /^(\d+)(?:-(\d+))?$/.exec(text.trim());
  if (!match) return undefined;
  const episode = Number(match[1]);
  if (!Number.isInteger(episode) || episode < 1) return undefined;
  if (match[2] === undefined) return { episode };
  const end = Number(match[2]);
  if (!Number.isInteger(end) || end < episode || end - episode + 1 > maxManualEpisodesPerFile) return undefined;
  return end === episode ? { episode } : { episode, episode_end: end };
}

/** Pads an episode to two digits for the row label (`E01`, `E01-E02`). */
export function episodeInputLabel(text: string): string | undefined {
  const parsed = parseEpisodeInput(text);
  if (!parsed) return undefined;
  const start = `E${String(parsed.episode).padStart(2, "0")}`;
  return parsed.episode_end === undefined ? start : `${start}-E${String(parsed.episode_end).padStart(2, "0")}`;
}

/** Fills the checked batch sequentially: row i covers `start + i*span`, `span` episodes each. */
export function sequenceEpisodeInput(start: number, index: number, span: number): string {
  const episode = start + index * span;
  return span > 1 ? `${episode}-${episode + span - 1}` : String(episode);
}

export type FileEpisodeDraft = {
  season: string;
  episode: string;
};

export type FileNumberingItem = {
  path: string;
  sizeBytes?: number;
  automaticSeason: number | null;
  /** The first automatic episode is the starting value when a row becomes an override. */
  automaticEpisode: number | null;
  automaticLabel: string;
  subtitleCount: number;
};

export type FileNumberingBaseline = ReadonlyMap<string, FileEpisodeOverride>;
export type FileNumberingDrafts = ReadonlyMap<string, FileEpisodeDraft>;

export function overrideMap(overrides: readonly FileEpisodeOverride[]): Map<string, FileEpisodeOverride> {
  return new Map(overrides.map((override) => [override.path, { ...override }]));
}

export function overrideList(overrides: ReadonlyMap<string, FileEpisodeOverride>): FileEpisodeOverride[] {
  return [...overrides.values()].map((override) => ({ ...override })).sort((a, b) => a.path.localeCompare(b.path));
}

export function draftMap(overrides: FileNumberingBaseline): Map<string, FileEpisodeDraft> {
  return new Map([...overrides].map(([path, override]) => [path, {
    season: override.season === undefined ? "" : String(override.season),
    episode: override.episode_end !== undefined && override.episode_end > override.episode ? `${override.episode}-${override.episode_end}` : String(override.episode),
  }]));
}

export function automaticDraft(item: FileNumberingItem): FileEpisodeDraft {
  return {
    season: item.automaticSeason === null ? "" : String(item.automaticSeason),
    episode: item.automaticEpisode === null ? "" : String(item.automaticEpisode),
  };
}

export function validDraft(value: FileEpisodeDraft | undefined): boolean {
  if (!value) return false;
  const season = Number(value.season);
  return (value.season.trim() === "" || (Number.isInteger(season) && season >= 0)) && parseEpisodeInput(value.episode) !== undefined;
}

/** Converts every visible override draft into the exact state confirmation applies. */
export function overridesFromDrafts(
  drafts: FileNumberingDrafts,
): Map<string, FileEpisodeOverride> | undefined {
  const next = new Map<string, FileEpisodeOverride>();
  for (const [path, draft] of drafts) {
    if (!validDraft(draft)) return undefined;
    const parsed = parseEpisodeInput(draft.episode);
    if (!parsed) return undefined;
    next.set(path, {
      path,
      ...(draft.season.trim() === "" ? {} : { season: Number(draft.season) }),
      episode: parsed.episode,
      ...(parsed.episode_end === undefined ? {} : { episode_end: parsed.episode_end }),
    });
  }
  return next;
}

/** Overrides are meaningful only for videos that remain in the current file selection. */
export function sanitizeOverrides(
  overrides: readonly FileEpisodeOverride[] | undefined,
  selectedVideos: ReadonlySet<string>,
): FileEpisodeOverride[] {
  if (!overrides) return [];
  const byPath = new Map<string, FileEpisodeOverride>();
  for (const override of overrides) {
    if (!selectedVideos.has(override.path) || (override.season !== undefined && (!Number.isInteger(override.season) || override.season < 0)) || !Number.isInteger(override.episode) || override.episode < 1) continue;
    const cleaned: FileEpisodeOverride = { path: override.path, episode: override.episode };
    if (override.season !== undefined) cleaned.season = override.season;
    if (override.episode_end !== undefined && Number.isInteger(override.episode_end) && override.episode_end > override.episode && override.episode_end - override.episode + 1 <= maxManualEpisodesPerFile) {
      cleaned.episode_end = override.episode_end;
    }
    byPath.set(override.path, cleaned);
  }
  return overrideList(byPath);
}
