import { describe, expect, it } from "vitest";
import type { RSSListManifestFile } from "../../lib/api/client";
import {
  buildPickerTree,
  defaultFileSelection,
  directoryState,
  fileCheckable,
  folderSummary,
  initialExpandedFolders,
  sameFileChoice,
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
});

// A collection: season folders holding the episodes, a nested special folder, and folders of files
// the server will not process.
const pack: RSSListManifestFile[] = [
  { source_path: "Pack/S1/A - 01.mkv", size_bytes: 1, media_kind: "video", selectable: true, default_selected: true },
  { source_path: "Pack/S1/A - 01.sc.ass", size_bytes: 1, media_kind: "subtitle", selectable: true, default_selected: true, bound_video_source_path: "Pack/S1/A - 01.mkv" },
  { source_path: "Pack/S1/A - 01.tc.ass", size_bytes: 1, media_kind: "subtitle", selectable: true, default_selected: true, bound_video_source_path: "Pack/S1/A - 01.mkv" },
  { source_path: "Pack/S1/A - 02.mkv", size_bytes: 1, media_kind: "video", selectable: true, default_selected: true },
  { source_path: "Pack/S1/SPs/NCOP.mkv", size_bytes: 1, media_kind: "video", selectable: true, default_selected: false },
  { source_path: "Pack/S1/SPs/NCOP.ass", size_bytes: 1, media_kind: "subtitle", selectable: true, default_selected: false, bound_video_source_path: "Pack/S1/SPs/NCOP.mkv" },
  { source_path: "Pack/S1/SPs/Info.txt", size_bytes: 1, media_kind: "other", selectable: false, default_selected: false, reason_code: "ignored_kind" },
  { source_path: "Pack/S1/SPs/Notes.txt", size_bytes: 1, media_kind: "other", selectable: false, default_selected: false, reason_code: "ignored_kind" },
  { source_path: "Pack/S1/orphan.ass", size_bytes: 1, media_kind: "subtitle", selectable: false, default_selected: false, reason_code: "subtitle_binding_missing" },
  { source_path: "Pack/S2/A - 01.mkv", size_bytes: 1, media_kind: "video", selectable: true, default_selected: false },
  { source_path: "Pack/CDs/OST.flac", size_bytes: 1, media_kind: "other", selectable: false, default_selected: false, reason_code: "ignored_kind" },
  { source_path: "Pack/readme.txt", size_bytes: 1, media_kind: "other", selectable: false, default_selected: false, reason_code: "ignored_kind" },
];

describe("one-shot file picker tree", () => {
  it("lists a level's files before its subfolders and nests subtitles under the video they follow", () => {
    const root = buildPickerTree(pack);
    expect(root.folders.map((folder) => folder.path)).toEqual(["Pack"]);
    const top = root.folders[0]!;
    expect(top.videos).toEqual([]);
    expect(top.skipped.map((file) => file.source_path)).toEqual(["Pack/readme.txt"]);
    expect(top.folders.map((folder) => folder.name)).toEqual(["CDs", "S1", "S2"]);
    const s1 = top.folders.find((folder) => folder.name === "S1")!;
    expect(s1.videos.map((video) => [video.file.source_path, video.subtitles.map((subtitle) => subtitle.source_path)])).toEqual([
      ["Pack/S1/A - 01.mkv", ["Pack/S1/A - 01.sc.ass", "Pack/S1/A - 01.tc.ass"]],
      ["Pack/S1/A - 02.mkv", []],
    ]);
    expect(s1.folders.map((folder) => folder.path)).toEqual(["Pack/S1/SPs"]);
  });

  it("orders names numerically within a level", () => {
    const numbered = [10, 2, 1].map((n): RSSListManifestFile => ({ source_path: `Ep ${n}.mkv`, size_bytes: 1, media_kind: "video", selectable: true, default_selected: true }));
    expect(buildPickerTree(numbered).videos.map((video) => video.file.source_path)).toEqual(["Ep 1.mkv", "Ep 2.mkv", "Ep 10.mkv"]);
  });

  it("gathers the files that will not be processed into their own folder, a subtitle without a video among them", () => {
    const root = buildPickerTree(pack);
    const s1 = root.folders[0]!.folders.find((folder) => folder.name === "S1")!;
    expect(s1.skipped.map((file) => file.source_path)).toEqual(["Pack/S1/orphan.ass"]);
    const specials = s1.folders[0]!;
    expect(specials.skipped.map((file) => file.source_path)).toEqual(["Pack/S1/SPs/Info.txt", "Pack/S1/SPs/Notes.txt"]);
    const cds = root.folders[0]!.folders.find((folder) => folder.name === "CDs")!;
    expect(cds.videos).toEqual([]);
    expect(cds.skipped).toHaveLength(1);
  });

  it("opens a folder exactly when it holds a checked file, whatever it is called", () => {
    expect([...initialExpandedFolders(pack, defaultFileSelection(pack))].sort()).toEqual(["Pack", "Pack/S1"]);
    const withSpecials = new Set([...defaultFileSelection(pack), "Pack/S1/SPs/NCOP.mkv"]);
    expect([...initialExpandedFolders(pack, withSpecials)].sort()).toEqual(["Pack", "Pack/S1", "Pack/S1/SPs"]);
    expect([...initialExpandedFolders(pack, new Set(["Pack/S2/A - 01.mkv"]))].sort()).toEqual(["Pack", "Pack/S2"]);
    expect(initialExpandedFolders(pack, new Set())).toEqual(new Set());
    const renamed = pack.map((file) => ({ ...file, source_path: file.source_path.replace("/S1/", "/Specials/") }));
    expect([...initialExpandedFolders(renamed, defaultFileSelection(renamed))].sort()).toEqual(["Pack", "Pack/Specials"]);
  });

  it("summarizes every folder over everything below it", () => {
    const root = buildPickerTree(pack);
    const top = root.folders[0]!;
    const s1 = top.folders.find((folder) => folder.name === "S1")!;
    const defaults = defaultFileSelection(pack);
    expect(folderSummary(s1, defaults)).toEqual({ videos: 3, selectedVideos: 2, subtitles: 3, selectedSubtitles: 2 });
    expect(folderSummary(s1.folders[0]!, defaults)).toEqual({ videos: 1, selectedVideos: 0, subtitles: 1, selectedSubtitles: 0 });
    expect(folderSummary(top, defaults)).toEqual({ videos: 4, selectedVideos: 2, subtitles: 3, selectedSubtitles: 2 });
    const cds = top.folders.find((folder) => folder.name === "CDs")!;
    expect(folderSummary(cds, defaults)).toEqual({ videos: 0, selectedVideos: 0, subtitles: 0, selectedSubtitles: 0 });
    const all = new Set(pack.filter((file) => file.selectable).map((file) => file.source_path));
    expect(folderSummary(top, all)).toEqual({ videos: 4, selectedVideos: 4, subtitles: 3, selectedSubtitles: 3 });
  });

  it("compares two normalized manual choices", () => {
    expect(sameFileChoice(undefined, undefined)).toBe(true);
    expect(sameFileChoice(undefined, [])).toBe(false);
    expect(sameFileChoice(["a", "b"], ["a", "b"])).toBe(true);
    expect(sameFileChoice(["a"], ["a", "b"])).toBe(false);
    expect(sameFileChoice(["a", "c"], ["a", "b"])).toBe(false);
  });
});
