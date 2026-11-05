import type { AnimeFileAction, AnimeFilesView } from "../../src/lib/api/client";

function action(available: boolean, reasonCode?: string): AnimeFileAction {
  return { available, ...(reasonCode ? { reason_code: reasonCode } : {}) };
}

const inactive = action(false, "file_not_missing");

// Placeholder groups and release-shaped paths only: no real feed rows or group
// identities are copied into the browser fixture.
export function animeFilesView(animeID: string): AnimeFilesView {
  const longName = "Fixture Show - 01 - A deliberately long filename for checking wrapped deletion copy.mkv";
  const videoPath = `/library/Fixture Show/Season 1/${longName}`;
  return {
    managed_anime_id: animeID,
    root: "/library",
    scan: { state: "idle", generation: 3, scanned_at: "2026-11-05T00:00:00Z", rescan_required: false },
    aligning: false,
    scan_action: action(true),
    align_all_action: action(false, "nothing_to_align"),
    files: [
      {
        placement_id: "fixture-delete",
        placement_version: 7,
        release_id: "fixture-release-delete",
        group_name: "Fixture Removed Group",
        media_kind: "tv",
        episodes: ["1"],
        path: videoPath,
        relative_path: `Fixture Show/Season 1/${longName}`,
        size_bytes: 1024,
        status: "aligned",
        actions: { align: action(false, "nothing_to_align"), rerun: inactive, release: action(true), delete: action(true) },
        companions: [
          {
            id: "fixture-companion",
            media_kind: "subtitle",
            path: videoPath.replace(/\.mkv$/, ".zh-CN.ass"),
            relative_path: `Fixture Show/Season 1/${longName.replace(/\.mkv$/, ".zh-CN.ass")}`,
            size_bytes: 256,
            status: "aligned",
          },
        ],
      },
      {
        placement_id: "fixture-selected",
        placement_version: 2,
        group_name: "Fixture Selected Group",
        media_kind: "tv",
        episodes: ["2"],
        path: "/library/Fixture Show/Season 1/Fixture Show - 02.mkv",
        relative_path: "Fixture Show/Season 1/Fixture Show - 02.mkv",
        status: "aligned",
        actions: { align: action(false, "nothing_to_align"), rerun: inactive, release: action(true), delete: action(false, "group_selected") },
        companions: [],
      },
      {
        placement_id: "fixture-custom",
        placement_version: 1,
        media_kind: "tv",
        episodes: ["3"],
        path: "/library/Fixture Show/Season 1/Fixture Show - 03.mkv",
        relative_path: "Fixture Show/Season 1/Fixture Show - 03.mkv",
        status: "aligned",
        actions: { align: action(false, "nothing_to_align"), rerun: inactive, release: action(true), delete: action(false, "group_selected") },
        companions: [],
      },
      {
        placement_id: "fixture-unavailable",
        placement_version: 4,
        group_name: "Fixture Removed Group",
        media_kind: "tv",
        episodes: ["4"],
        path: "/library/Fixture Show/Season 1/Fixture Show - 04.mkv",
        relative_path: "Fixture Show/Season 1/Fixture Show - 04.mkv",
        status: "missing",
        actions: { align: action(false, "nothing_to_align"), rerun: inactive, release: action(true), delete: action(false, "file_unavailable") },
        companions: [],
      },
    ],
  };
}
