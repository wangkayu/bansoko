type AnimeConfigDialogEntryModule = {
  default: typeof import("./AnimeConfigDialogEntry").AnimeConfigDialogEntry;
};

let loadPromise: Promise<AnimeConfigDialogEntryModule> | undefined;

export function loadAnimeConfigDialog(): Promise<AnimeConfigDialogEntryModule> {
  if (!loadPromise) {
    loadPromise = import("./AnimeConfigDialogEntry").then((module) => ({
      default: module.AnimeConfigDialogEntry,
    }));
  }
  return loadPromise;
}
