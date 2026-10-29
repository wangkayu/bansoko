import { useMemo } from "react";
import { t } from "../../lib/i18n";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button, Select } from "../../ui/base/controls";
import { browseSeasons, currentSeason, parseSeason, seasonIndex, seasonLabel, shiftSeason } from "./season";

// SeasonPicker — replaces the free-text season input (design doc §5.1.2):
// stepper arrows for adjacent seasons, ui/base Select for jumping. No more typing
// `2026-summer` by hand.

export function SeasonPicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (season: string) => void;
}) {
  // The options are the fixed browse window around the current season, never re-centered on the
  //   selected value: the current season is always one pick away, and the list keeps its items while a
  //   pick closes the Select (a list swapped mid-close made Base UI report `null`, which wrote
  //   `season=null` to the address on touch taps). The arrows step past the window; a season out there
  //   is appended so the trigger still shows it.
  const current = currentSeason();
  const seasons = useMemo(() => {
    const list = browseSeasons(current);
    if (seasonIndex(value) !== undefined && !list.includes(value)) list.push(value);
    return list.sort((a, b) => seasonIndex(b)! - seasonIndex(a)!);
  }, [current, value]);
  // Ignore anything that is not a season (a stray `null` from the Select) rather than writing it to the address.
  const pick = (next: string | null) => {
    if (next && parseSeason(next)) onChange(next);
  };

  return (
    <div className="season-picker">
      <Button size="icon" variant="ghost" aria-label={t("grid.seasonPrev")} onClick={() => onChange(shiftSeason(value, -1))}>
        <ChevronLeft className="season-picker-icon" />
      </Button>
      <Select
        label={t("grid.seasonPick")}
        value={value}
        options={seasons.map((season) => ({ value: season, label: seasonLabel(season) }))}
        onChange={pick}
      />
      <Button size="icon" variant="ghost" aria-label={t("grid.seasonNext")} onClick={() => onChange(shiftSeason(value, 1))}>
        <ChevronRight className="season-picker-icon" />
      </Button>
    </div>
  );
}
