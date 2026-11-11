export type FileEpisodeOverride = {
  path: string;
  /** Omitted means the file keeps the main season setting. */
  season?: number;
  episode: number;
};

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
  return new Map([...overrides].map(([path, override]) => [path, { season: override.season === undefined ? "" : String(override.season), episode: String(override.episode) }]));
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
  const episode = Number(value.episode);
  return value.episode.trim() !== "" && (value.season.trim() === "" || (Number.isInteger(season) && season >= 0)) && Number.isInteger(episode) && episode >= 1;
}

/** Applies only the checked batch to the state that was effective before the dialog opened. */
export function mergeCheckedOverrides(
  baseline: FileNumberingBaseline,
  drafts: FileNumberingDrafts,
  checked: ReadonlySet<string>,
): Map<string, FileEpisodeOverride> | undefined {
  const next = overrideMap([...baseline.values()]);
  for (const path of checked) {
    const draft = drafts.get(path);
    if (draft === undefined) {
      next.delete(path);
      continue;
    }
    if (!validDraft(draft)) return undefined;
    next.set(path, { path, ...(draft.season.trim() === "" ? {} : { season: Number(draft.season) }), episode: Number(draft.episode) });
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
    byPath.set(override.path, { ...override });
  }
  return overrideList(byPath);
}
