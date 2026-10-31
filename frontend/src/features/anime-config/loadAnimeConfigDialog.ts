// Shared preload of the config dialog's chunk: both the catalog grid and the global search use the
// dialog, and the hover/edit-intent preload calls this too. The raw module namespace is handed
// back so lazyEntry can pick the component (and keep the fallback when a stale chunk resolves
// undefined).
type AnimeConfigDialogEntryModule = typeof import("./AnimeConfigDialogEntry");

let loadPromise: Promise<AnimeConfigDialogEntryModule> | undefined;

export function loadAnimeConfigDialog(): Promise<AnimeConfigDialogEntryModule> {
  loadPromise ??= import("./AnimeConfigDialogEntry");
  return loadPromise;
}
