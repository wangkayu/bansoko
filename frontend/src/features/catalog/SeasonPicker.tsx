import { t } from "../../lib/i18n";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button, Select } from "../../ui/base/controls";
import { currentSeason, listSeasonsAround, seasonLabel, shiftSeason } from "./season";

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
  const seasons = listSeasonsAround(value || currentSeason(), 6, 2).reverse();

  return (
    <div className="season-picker">
      <Button size="icon" variant="ghost" aria-label={t("grid.seasonPrev")} onClick={() => onChange(shiftSeason(value, -1))}>
        <ChevronLeft className="season-picker-icon" />
      </Button>
      <Select
        label={t("grid.seasonPick")}
        value={value}
        options={seasons.map((season) => ({ value: season, label: seasonLabel(season) }))}
        onChange={onChange}
      />
      <Button size="icon" variant="ghost" aria-label={t("grid.seasonNext")} onClick={() => onChange(shiftSeason(value, 1))}>
        <ChevronRight className="season-picker-icon" />
      </Button>
    </div>
  );
}
