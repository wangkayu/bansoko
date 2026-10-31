// Shared preload of the one-shot dialog's chunk: the catalog grid and the global search both open
// it, and the dialog downloads only after the user clicks browse seeds. The raw module namespace is
// handed back so lazyEntry can pick the component (and keep the fallback when a stale chunk
// resolves undefined).
type OneShotDialogEntryModule = typeof import("./OneShotDialogEntry");

let loadPromise: Promise<OneShotDialogEntryModule> | undefined;

export function loadOneShotDialog(): Promise<OneShotDialogEntryModule> {
  loadPromise ??= import("./OneShotDialogEntry");
  return loadPromise;
}
