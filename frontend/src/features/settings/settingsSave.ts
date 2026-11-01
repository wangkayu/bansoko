import { useSaveParts } from "../../lib/saveParts";

// The settings sections' single save (styling-contract S4): sections edited in place register their
//   own endpoints as save parts, and the settings dialog's footer has one save that submits them in
//   this order. Sections living in card -> edit dialogs register no part, so no save appears in the
//   footer.
//
// - section: the global sections (subscription policy / site configuration share the subgroups section)
// - alignment: global automatic file alignment
// - metadata-refresh: the Global metadata sweep toggle
// - catalog: the site catalog's auto-refresh preference
// - unclassified: the default handling of files with unrecognized episodes
export const SETTINGS_SAVE_ORDER = ["section", "alignment", "metadata-refresh", "catalog", "unclassified"] as const;

export function useSettingsSaveParts() {
  return useSaveParts(SETTINGS_SAVE_ORDER);
}

export type SettingsSaveParts = ReturnType<typeof useSettingsSaveParts>;
