type OneShotDialogEntryModule = {
  default: typeof import("./OneShotDialogEntry").OneShotDialogEntry;
};

let loadPromise: Promise<OneShotDialogEntryModule> | undefined;

/** The dialog downloads only after the user clicks browse seeds (same lazy-load pattern as the config dialog). */
export function loadOneShotDialog(): Promise<OneShotDialogEntryModule> {
  if (!loadPromise) {
    loadPromise = import("./OneShotDialogEntry").then((module) => ({
      default: module.OneShotDialogEntry,
    }));
  }
  return loadPromise;
}
