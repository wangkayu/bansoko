// Episode length is shown in whole minutes. Providers disagree on how they say
// it: Bangumi sends a clock string ("00:24:00", "23:40") or a suffixed number
// ("24m", "24 min"), TMDB sends integer minutes. Text that parses to nothing is
// still shown as sent — an unreadable value beats a made-up one.

const CLOCK = /^(\d{1,3}):([0-5]?\d)(?::([0-5]?\d))?$/;
const MINUTES = /^(\d{1,4}(?:[.,]\d+)?)\s*(?:m|min|mins|minute|minutes)$/i;
const SECONDS = /^(\d{1,5})\s*(?:s|sec|secs|second|seconds)$/i;

function minutesFromSeconds(seconds: number): number {
  if (seconds <= 0) return 0;
  return Math.max(1, Math.round(seconds / 60));
}

/** Parses a provider duration string into whole minutes; undefined when it does not parse. */
export function parseEpisodeMinutes(raw: string): number | undefined {
  const value = raw.trim();
  if (!value) return undefined;
  const clock = CLOCK.exec(value);
  if (clock) {
    const hours = Number(clock[1]);
    const middle = Number(clock[2]);
    const tail = clock[3] === undefined ? undefined : Number(clock[3]);
    // Two parts are minutes:seconds, three are hours:minutes:seconds.
    return tail === undefined ? minutesFromSeconds(hours * 60 + middle) : minutesFromSeconds(hours * 3600 + middle * 60 + tail);
  }
  const minutes = MINUTES.exec(value);
  if (minutes) return minutesFromSeconds(Number(minutes[1].replace(",", ".")) * 60);
  const seconds = SECONDS.exec(value);
  if (seconds) return minutesFromSeconds(Number(seconds[1]));
  return undefined;
}

/** The row's duration cell: whole minutes, the raw text when unreadable, "—" when absent. */
export function formatEpisodeDuration(episode: { duration?: string; runtimeMinutes?: number }): string {
  if (episode.runtimeMinutes !== undefined && Number.isInteger(episode.runtimeMinutes) && episode.runtimeMinutes >= 0) {
    return `${episode.runtimeMinutes}m`;
  }
  const raw = episode.duration?.trim() ?? "";
  if (!raw) return "—";
  const minutes = parseEpisodeMinutes(raw);
  return minutes === undefined ? raw : `${minutes}m`;
}
