import { describe, expect, it } from "vitest";
import type { RSSListManifestFile } from "../../lib/api/client";
import {
  buildFileTree,
  defaultFileSelection,
  directoryState,
  fileCheckable,
  sanitizeFileSelection,
  selectedVideoCount,
  toggleDirectorySelection,
  toggleFileSelection,
} from "./fileSelection";

const files: RSSListManifestFile[] = [
  { source_path: "Pack/A - 01.mkv", size_bytes: 1, media_kind: "video", selectable: true, default_selected: true, recognition: "episode", episodes: [1] },
  { source_path: "Pack/A - 01.ass", size_bytes: 1, media_kind: "subtitle", selectable: true, default_selected: true, bound_video_source_path: "Pack/A - 01.mkv" },
  { source_path: "Pack/S2/A - 01.mkv", size_bytes: 1, media_kind: "video", selectable: true, default_selected: false, recognition: "episode", episodes: [1] },
  { source_path: "Pack/S2/A - 01.ass", size_bytes: 1, media_kind: "subtitle", selectable: true, default_selected: false, bound_video_source_path: "Pack/S2/A - 01.mkv" },
  { source_path: "Pack/NCOP.mkv", size_bytes: 1, media_kind: "video", selectable: false, default_selected: false, reason_code: "file_not_in_matched" },
  { source_path: "Pack/cover.jpg", size_bytes: 1, media_kind: "other", selectable: false, default_selected: false, reason_code: "ignored_kind" },
];

describe("one-shot file selection model", () => {
  it("starts from the server's default list and leaves no manual set while it is unchanged", () => {
    expect([...defaultFileSelection(files)].sort()).toEqual(["Pack/A - 01.ass", "Pack/A - 01.mkv"]);
    const deep = toggleFileSelection(files, undefined, "Pack/S2/A - 01.mkv");
    expect(deep).toEqual(["Pack/A - 01.ass", "Pack/A - 01.mkv", "Pack/S2/A - 01.ass", "Pack/S2/A - 01.mkv"]);
    expect(toggleFileSelection(files, deep, "Pack/S2/A - 01.mkv")).toBeUndefined();
  });

  it("couples a subtitle to the video it follows and never toggles an unselectable file", () => {
    const selection = defaultFileSelection(files);
    expect(fileCheckable(files[3]!, selection)).toBe(false);
    expect(toggleFileSelection(files, undefined, "Pack/S2/A - 01.ass")).toBeUndefined();
    expect(toggleFileSelection(files, undefined, "Pack/NCOP.mkv")).toBeUndefined();
    const noVideo = toggleFileSelection(files, undefined, "Pack/A - 01.mkv");
    expect(noVideo).toEqual([]);
    expect(selectedVideoCount(files, new Set(noVideo))).toBe(0);
  });

  it("drops choices the newest rows no longer allow", () => {
    const chosen = ["Pack/S2/A - 01.mkv", "Pack/S2/A - 01.ass"];
    const reread = files.map((file) => (file.source_path === "Pack/S2/A - 01.mkv" ? { ...file, selectable: false } : file));
    expect(sanitizeFileSelection(reread, chosen)).toEqual([]);
    expect(sanitizeFileSelection(files, chosen)).toEqual(chosen.slice().sort());
    expect(sanitizeFileSelection(files, ["Pack/A - 01.mkv", "Pack/A - 01.ass"])).toBeUndefined();
  });

  it("toggles a folder's selectable videos together, subtitles following, and reads all/some/none", () => {
    expect(directoryState(files, defaultFileSelection(files), "Pack")).toBe("mixed");
    expect(directoryState(files, defaultFileSelection(files), "Pack/S2")).toBe("unchecked");
    const all = toggleDirectorySelection(files, undefined, "Pack");
    expect(all).toEqual(["Pack/A - 01.ass", "Pack/A - 01.mkv", "Pack/S2/A - 01.ass", "Pack/S2/A - 01.mkv"]);
    expect(directoryState(files, new Set(all), "Pack")).toBe("checked");
    expect(toggleDirectorySelection(files, all, "Pack")).toEqual([]);
    expect(toggleDirectorySelection(files, undefined, "Pack/S2")).toEqual(all);
    const onlyRules: RSSListManifestFile[] = [files[4]!, files[5]!].map((file) => ({ ...file, source_path: `Empty/${file.source_path}` }));
    expect(directoryState(onlyRules, new Set(), "Empty")).toBe("empty");
    expect(toggleDirectorySelection(onlyRules, undefined, "Empty")).toBeUndefined();
  });

  it("builds the folder tree with folders first", () => {
    const tree = buildFileTree(files);
    expect(tree).toHaveLength(1);
    const pack = tree[0]!;
    expect(pack.kind === "dir" && pack.children.map((node) => (node.kind === "dir" ? `dir:${node.name}` : node.name))).toEqual([
      "dir:S2",
      "A - 01.ass",
      "A - 01.mkv",
      "cover.jpg",
      "NCOP.mkv",
    ]);
  });
});
