import type { RSSListManifestFile } from "../../lib/api/client";

// The one-shot file picker's model. Every decision comes from the server's manifest_files rows:
//   which files may be chosen (selectable), which start checked (default_selected) and which video a
//   subtitle follows (bound_video_source_path). The picker only keeps the user's checked set and
//   the one coupling the server enforces: a subtitle can be checked only while the video it follows
//   is checked, so checking a video brings its subtitles and unchecking it takes them away.
//
// A seed with no manual change has no entry at all: its download request carries no file_selection
//   and previews the default files, byte-identical to a request made without the picker.

export type FileTreeNode =
  | { kind: "dir"; name: string; path: string; children: FileTreeNode[] }
  | { kind: "file"; name: string; file: RSSListManifestFile };

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

/** The torrent's folder structure: folders first, then files, each in path order. */
export function buildFileTree(files: readonly RSSListManifestFile[]): FileTreeNode[] {
  type Dir = { name: string; path: string; dirs: Map<string, Dir>; files: FileTreeNode[] };
  const root: Dir = { name: "", path: "", dirs: new Map(), files: [] };
  for (const file of [...files].sort((a, b) => a.source_path.localeCompare(b.source_path))) {
    const segments = file.source_path.split("/");
    const name = segments.pop() ?? file.source_path;
    let dir = root;
    for (const segment of segments) {
      const path = dir.path ? `${dir.path}/${segment}` : segment;
      let child = dir.dirs.get(segment);
      if (!child) {
        child = { name: segment, path, dirs: new Map(), files: [] };
        dir.dirs.set(segment, child);
      }
      dir = child;
    }
    dir.files.push({ kind: "file", name, file });
  }
  const flatten = (dir: Dir): FileTreeNode[] => [
    ...[...dir.dirs.values()].map((child): FileTreeNode => ({ kind: "dir", name: child.name, path: child.path, children: flatten(child) })),
    ...dir.files,
  ];
  return flatten(root);
}
