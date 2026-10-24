import type { GlobalSectionView } from "../../lib/api/client";
import { t } from "../../lib/i18n";

// The "custom default / built-in default" badge semantics: GET /config/global-sections' `saved`
//   means the section has persisted rows (a first save's version is already 1, so version cannot
//   tell).
export function sectionSourceLabel(view: GlobalSectionView): string {
  return view.saved ? t("settings.badge.custom") : t("settings.badge.builtin");
}
