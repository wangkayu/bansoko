import { useMemo, useState } from "react";
import { t } from "../../lib/i18n";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button, Select } from "../../ui/base/controls";
import { currentSeason, listSeasonsAround, parseSeason, seasonIndex, seasonLabel, shiftSeason } from "./season";

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
  // The option list is anchored where the picker mounted, not at the selected value. A list that
  //   re-centers on every pick swaps the Select's registered items while it closes; Base UI then
  //   finds the new value missing from the old registrations and reports `null`, which wrote
  //   `season=null` to the address and dropped the page back to the current season (touch taps).
  //   A season outside the window (stepped with the arrows) is appended so the trigger still shows it.
  const [anchor] = useState(() => (parseSeason(value) ? value : currentSeason()));
  const seasons = useMemo(() => {
    const list = listSeasonsAround(anchor, 6, 2);
    if (seasonIndex(value) !== undefined && !list.includes(value)) list.push(value);
    return list.sort((a, b) => seasonIndex(b)! - seasonIndex(a)!);
  }, [anchor, value]);
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
