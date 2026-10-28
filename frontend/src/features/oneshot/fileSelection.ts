import type { RSSListManifestFile } from "../../lib/api/client";

// The one-shot file picker's model. Every decision comes from the server's manifest_files rows:
//   which files may be chosen (selectable), which start checked (default_selected) and which video a
//   subtitle follows (bound_video_source_path). The picker only keeps the user's checked set and
//   the one coupling the server enforces: a subtitle can be checked only while the video it follows
//   is checked, so checking a video brings its subtitles and unchecking it takes them away.
//
// A seed with no manual change has no entry at all: its download request carries no file_selection
//   and previews the default files, byte-identical to a request made without the picker.

/** The default checked set: what the download takes when the user changes nothing. */
export function defaultFileSelection(files: readonly RSSListManifestFile[]): ReadonlySet<string> {
  return new Set(files.filter((file) => file.default_selected).map((file) => file.source_path));
}

/** The checked set shown: the user's own set when there is one, otherwise the defaults. */
export function effectiveFileSelection(files: readonly RSSListManifestFile[], chosen: readonly string[] | undefined): ReadonlySet<string> {
  return chosen ? new Set(chosen) : defaultFileSelection(files);
}

/** Whether a row's checkbox is usable under the current checked set. */
export function fileCheckable(file: RSSListManifestFile, selection: ReadonlySet<string>): boolean {
  if (!file.selectable) return false;
  if (file.media_kind !== "subtitle") return true;
  return file.bound_video_source_path !== undefined && selection.has(file.bound_video_source_path);
}

/**
 * Toggles one file. A video carries its subtitles with it (checking brings every subtitle that
 * follows it, unchecking removes them). Returns undefined when the result equals the defaults, so
 * a seed toggled back to its default list leaves no manual selection behind.
 */
export function toggleFileSelection(
  files: readonly RSSListManifestFile[],
  chosen: readonly string[] | undefined,
  path: string,
): string[] | undefined {
  const current = effectiveFileSelection(files, chosen);
  const file = files.find((candidate) => candidate.source_path === path);
  if (!file || !fileCheckable(file, current)) return chosen ? [...chosen] : undefined;
  const next = new Set(current);
  const checking = !next.has(path);
  if (checking) next.add(path);
  else next.delete(path);
  if (file.media_kind === "video") {
    for (const subtitle of files) {
      if (subtitle.media_kind !== "subtitle" || subtitle.bound_video_source_path !== path || !subtitle.selectable) continue;
      if (checking) next.add(subtitle.source_path);
      else next.delete(subtitle.source_path);
    }
  }
  return normalizeFileSelection(files, next);
}

/** The videos a folder row toggles: every selectable video at any depth below it. */
function directoryVideos(files: readonly RSSListManifestFile[], directory: string): RSSListManifestFile[] {
  const prefix = `${directory}/`;
  return files.filter((file) => file.media_kind === "video" && file.selectable && file.source_path.startsWith(prefix));
}

/** A folder row's checkbox: all, some or none of its selectable videos are checked; none to check disables it. */
export type DirectoryState = "checked" | "mixed" | "unchecked" | "empty";

export function directoryState(files: readonly RSSListManifestFile[], selection: ReadonlySet<string>, directory: string): DirectoryState {
  const videos = directoryVideos(files, directory);
  if (videos.length === 0) return "empty";
  const checked = videos.filter((file) => selection.has(file.source_path)).length;
  return checked === videos.length ? "checked" : checked === 0 ? "unchecked" : "mixed";
}

/**
 * Toggles a folder: when every selectable video below it is checked they are all unchecked,
 * otherwise they are all checked. Each video carries its subtitles, as a single toggle does.
 */
export function toggleDirectorySelection(
  files: readonly RSSListManifestFile[],
  chosen: readonly string[] | undefined,
  directory: string,
): string[] | undefined {
  const current = effectiveFileSelection(files, chosen);
  const videos = directoryVideos(files, directory);
  if (videos.length === 0) return chosen ? [...chosen] : undefined;
  const checking = directoryState(files, current, directory) !== "checked";
  const paths = new Set(videos.map((file) => file.source_path));
  const next = new Set(current);
  for (const file of files) {
    const target = file.media_kind === "video" ? paths.has(file.source_path) : file.media_kind === "subtitle" && file.selectable && file.bound_video_source_path !== undefined && paths.has(file.bound_video_source_path);
    if (!target) continue;
    if (checking) next.add(file.source_path);
    else next.delete(file.source_path);
  }
  return normalizeFileSelection(files, next);
}

/** The manual set in request form (sorted paths), or undefined when it equals the defaults. */
export function normalizeFileSelection(files: readonly RSSListManifestFile[], selection: ReadonlySet<string>): string[] | undefined {
  const defaults = defaultFileSelection(files);
  const same = defaults.size === selection.size && [...selection].every((path) => defaults.has(path));
  return same ? undefined : [...selection].sort();
}

/**
 * A manual set re-checked against the newest rows (a re-read under edited file rules may make a
 * checked file unselectable): only selectable files survive, and a subtitle only with its video.
 * Returns undefined when nothing manual remains.
 */
export function sanitizeFileSelection(files: readonly RSSListManifestFile[], chosen: readonly string[] | undefined): string[] | undefined {
  if (!chosen || files.length === 0) return undefined;
  const rows = new Map(files.map((file) => [file.source_path, file]));
  const kept = new Set(chosen.filter((path) => rows.get(path)?.selectable === true && rows.get(path)?.media_kind === "video"));
  for (const path of chosen) {
    const file = rows.get(path);
    if (file?.selectable && file.media_kind === "subtitle" && file.bound_video_source_path !== undefined && kept.has(file.bound_video_source_path)) kept.add(path);
  }
  return normalizeFileSelection(files, kept);
}

/** How many checked videos the set holds; the server refuses a manual set without one. */
export function selectedVideoCount(files: readonly RSSListManifestFile[], selection: ReadonlySet<string>): number {
  return files.filter((file) => file.media_kind === "video" && selection.has(file.source_path)).length;
}

/** One processable video with the subtitles that follow it (they render nested under it). */
export type PickerVideo = { file: RSSListManifestFile; subtitles: RSSListManifestFile[] };

/**
 * One folder of the picker's tree. `videos` are the selectable videos directly in the folder;
 * `skipped` are the folder's own files that will not be processed (not selectable, or a subtitle
 * with no selectable video to follow); `folders` are the subfolders. A level lists its files
 * before its subfolders.
 */
export type PickerFolder = {
  name: string;
  path: string;
  videos: PickerVideo[];
  skipped: RSSListManifestFile[];
  folders: PickerFolder[];
};

const byName = (a: string, b: string): number => a.localeCompare(b, undefined, { numeric: true });

/**
 * The torrent's folder structure for the picker, from the manifest rows alone: a selectable video
 * carries the selectable subtitles that follow it; every other file is "not processed" in the folder
 * it sits in. The root has an empty name and path.
 */
export function buildPickerTree(files: readonly RSSListManifestFile[]): PickerFolder {
  const root: PickerFolder = { name: "", path: "", videos: [], skipped: [], folders: [] };
  const folders = new Map<string, PickerFolder>([["", root]]);
  const folderOf = (path: string): PickerFolder => {
    const known = folders.get(path);
    if (known) return known;
    const cut = path.lastIndexOf("/");
    const parent = folderOf(cut < 0 ? "" : path.slice(0, cut));
    const folder: PickerFolder = { name: cut < 0 ? path : path.slice(cut + 1), path, videos: [], skipped: [], folders: [] };
    folders.set(path, folder);
    parent.folders.push(folder);
    return folder;
  };
  const dirOf = (path: string): string => path.slice(0, Math.max(0, path.lastIndexOf("/")));

  const sorted = [...files].sort((a, b) => byName(a.source_path, b.source_path));
  const videos = new Map<string, PickerVideo>();
  for (const file of sorted) {
    if (file.media_kind === "video" && file.selectable) {
      const video: PickerVideo = { file, subtitles: [] };
      videos.set(file.source_path, video);
      folderOf(dirOf(file.source_path)).videos.push(video);
    }
  }
  for (const file of sorted) {
    if (file.media_kind === "video" && file.selectable) continue;
    const owner = file.media_kind === "subtitle" && file.selectable && file.bound_video_source_path !== undefined ? videos.get(file.bound_video_source_path) : undefined;
    if (owner) owner.subtitles.push(file);
    else folderOf(dirOf(file.source_path)).skipped.push(file);
  }
  const order = (folder: PickerFolder): void => {
    folder.folders.sort((a, b) => byName(a.name, b.name));
    folder.folders.forEach(order);
  };
  order(root);
  return root;
}

/** The folders open when the picker opens: every folder holding a currently checked file, found from the data alone. */
export function initialExpandedFolders(files: readonly RSSListManifestFile[], selection: ReadonlySet<string>): Set<string> {
  const open = new Set<string>();
  for (const file of files) {
    if (!selection.has(file.source_path)) continue;
    const segments = file.source_path.split("/");
    segments.pop();
    let path = "";
    for (const segment of segments) {
      path = path ? `${path}/${segment}` : segment;
      open.add(path);
    }
  }
  return open;
}

/** What a folder row reports about everything below it, at any depth. */
export type FolderSummary = { videos: number; selectedVideos: number; subtitles: number; selectedSubtitles: number };

export function folderSummary(folder: PickerFolder, selection: ReadonlySet<string>): FolderSummary {
  const total: FolderSummary = { videos: 0, selectedVideos: 0, subtitles: 0, selectedSubtitles: 0 };
  const walk = (level: PickerFolder): void => {
    for (const { file, subtitles } of level.videos) {
      total.videos += 1;
      if (selection.has(file.source_path)) total.selectedVideos += 1;
      total.subtitles += subtitles.length;
      total.selectedSubtitles += subtitles.filter((subtitle) => selection.has(subtitle.source_path)).length;
    }
    level.folders.forEach(walk);
  };
  walk(folder);
  return total;
}

/** Whether two manual choices (normalized: undefined = the defaults, else sorted paths) are the same. */
export function sameFileChoice(a: readonly string[] | undefined, b: readonly string[] | undefined): boolean {
  if (a === undefined || b === undefined) return a === b;
  return a.length === b.length && a.every((path, index) => path === b[index]);
}
