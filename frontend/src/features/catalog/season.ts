import { t, type CopyKey } from "../../lib/i18n";

// Season domain helpers for the catalog season picker (design doc §5.1.2).
// Seasons are canonical `YYYY-term` strings; winter=Jan-Mar, spring=Apr-Jun,
// summer=Jul-Sep, fall=Oct-Dec.

export const SEASON_TERMS = ["winter", "spring", "summer", "fall"] as const;
export type SeasonTerm = (typeof SEASON_TERMS)[number];

const TERM_LABEL_KEY = {
  winter: "grid.seasonTerm.winter",
  spring: "grid.seasonTerm.spring",
  summer: "grid.seasonTerm.summer",
  fall: "grid.seasonTerm.fall",
} as const satisfies Record<SeasonTerm, CopyKey>;

export function parseSeason(season: string): { year: number; term: SeasonTerm } | undefined {
  const [yearText, term] = season.split("-");
  const year = Number(yearText);
  if (!Number.isInteger(year) || !(SEASON_TERMS as readonly string[]).includes(term)) return undefined;
  return { year, term: term as SeasonTerm };
}

/** Linear index used for stepping: year * 4 + termIndex. */
export function seasonIndex(season: string): number | undefined {
  const parsed = parseSeason(season);
  if (!parsed) return undefined;
  return parsed.year * 4 + SEASON_TERMS.indexOf(parsed.term);
}

export function seasonFromIndex(index: number): string {
  const year = Math.floor(index / 4);
  const term = SEASON_TERMS[((index % 4) + 4) % 4];
  return `${year}-${term}`;
}

export function shiftSeason(season: string, delta: number): string {
  const index = seasonIndex(season);
  if (index === undefined) return season;
  return seasonFromIndex(index + delta);
}

export function seasonLabel(season: string): string {
  const parsed = parseSeason(season);
  if (!parsed) return season;
  return `${parsed.year} ${t(TERM_LABEL_KEY[parsed.term])}`;
}

export function currentSeason(now: Date = new Date()): string {
  const month = now.getMonth(); // 0-based
  const term = SEASON_TERMS[Math.floor(month / 3)];
  return `${now.getFullYear()}-${term}`;
}

/** Inclusive list around the center season. */
export function listSeasonsAround(center: string, back: number, forward: number): string[] {
  const index = seasonIndex(center) ?? seasonIndex(currentSeason())!;
  const seasons: string[] = [];
  for (let i = -back; i <= forward; i += 1) seasons.push(seasonFromIndex(index + i));
  return seasons;
}

// The browse dropdown's window: the two coming seasons and two years back. It is a fixed set around
//   the current season, never derived from the season on screen; the arrows step past it, and older
//   works are found by search.
const BROWSE_SEASONS_FORWARD = 2;
const BROWSE_SEASONS_BACK = 8;

/** The seasons the browse dropdown offers, newest first. */
export function browseSeasons(current: string = currentSeason()): string[] {
  return listSeasonsAround(current, BROWSE_SEASONS_BACK, BROWSE_SEASONS_FORWARD).reverse();
}
