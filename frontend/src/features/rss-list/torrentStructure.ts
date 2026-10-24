import type { ReleaseTorrentStructureFile } from "../../lib/api/client";

// The pure projection behind the release structure preview: the server sends paths relative to
//   the torrent root, sorted; this module groups them into the entries directly under that root.

export type TorrentStructureEntry = {
  name: string;
  /** A folder when the path has a further `/`; a root-level file otherwise. */
  directory: boolean;
  /** A folder's recursive size; a root-level file's own size. */
  sizeBytes: number;
  /** The number of files under a folder; 1 for a root-level file. */
  fileCount: number;
};

/** Groups the payload's paths into the entries directly under the torrent root, sorted by name. */
export function torrentStructureEntries(files: readonly ReleaseTorrentStructureFile[]): TorrentStructureEntry[] {
  const entries = new Map<string, TorrentStructureEntry>();
  for (const file of files) {
    const separator = file.path.indexOf("/");
    const directory = separator >= 0;
    const name = directory ? file.path.slice(0, separator) : file.path;
    if (name === "") continue;
    const entry = entries.get(name);
    if (entry) {
      entry.sizeBytes += file.size_bytes;
      entry.fileCount += 1;
    } else {
      entries.set(name, { name, directory, sizeBytes: file.size_bytes, fileCount: 1 });
    }
  }
  return [...entries.values()].sort((left, right) => (left.name < right.name ? -1 : left.name > right.name ? 1 : 0));
}

const BINARY_UNITS = ["KiB", "MiB", "GiB", "TiB"] as const;

/** Binary units with one decimal; bytes below 1 KiB keep whole bytes. */
export function formatStructureBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < BINARY_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${BINARY_UNITS[unit]}`;
}

// Monospace cells a label occupies, so a Chinese folder name lines up with the sizes after it.
//   Same ranges as the notification renderer's runeCells: CJK, Hangul and fullwidth forms take two.
function runeCells(char: string): number {
  const code = char.codePointAt(0) ?? 0;
  if (
    (code >= 0x1100 && code <= 0x115f) ||
    (code >= 0x2e80 && code <= 0xa4cf) ||
    (code >= 0xa960 && code <= 0xa97f) ||
    (code >= 0xac00 && code <= 0xd7a3) ||
    (code >= 0xf900 && code <= 0xfaff) ||
    (code >= 0xfe10 && code <= 0xfe19) ||
    (code >= 0xfe30 && code <= 0xfe6f) ||
    (code >= 0xff00 && code <= 0xff60) ||
    (code >= 0xffe0 && code <= 0xffe6) ||
    (code >= 0x1f300 && code <= 0x1f64f) ||
    (code >= 0x1f900 && code <= 0x1f9ff) ||
    (code >= 0x20000 && code <= 0x3fffd)
  ) {
    return 2;
  }
  return 1;
}

/** The display width of a label in monospace cells. */
export function displayCells(text: string): number {
  let cells = 0;
  for (const char of text) cells += runeCells(char);
  return cells;
}
