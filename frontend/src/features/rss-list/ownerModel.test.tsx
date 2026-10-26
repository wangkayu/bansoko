import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AnimeWorkspace, ReleaseFile, RSSListGroup, RSSListItem, RSSListView } from "../../lib/api/client";
import { downloadTaskStateLabel, fileOperationKindLabel, fileOperationStateLabel, formatBytes } from "../../lib/labels";
import { knownReasonLabel } from "../../lib/i18n/reasons";
import { RssListViewPanel } from "./RssListViewPanel";
import { ownerViewModel, type OwnerViewHandlers } from "./ownerModel";
import { t } from "../../lib/i18n";

const ITEM: RSSListItem = {
  snapshot_id: "release:release-1",
  stable_seed_key: "stable-seed",
  seed: { title: "Server title", published_at: "2026-01-01T00:00:00Z", size_bytes: 123, locator_display: "https://example.test/feed" },
  torrent: {
    state: "available",
    manifest: {
      payload_digest: "digest",
      parser_version: "parser",
      torrent_version: "v1",
      torrent_name: "server torrent",
      total_size_bytes: 123,
      file_count: 1,
      projection_digest: "projection",
    },
  },
  evaluation: { state: "evaluated", verdict: "filtered", reason_code: "filtered_by_rules", reason_detail: "release_must_contain_not_matched", parsed_facts: { episode: 4, special: false } },
  files: [
    { file_ref: "file-1", ordinal: 0, source_path: "excluded.mkv", filename: "excluded.mkv", size_bytes: 123, media_kind: "video", included: false, reason_code: "file_not_in_matched", safety: "valid" },
  ],
  download_action: { allowed: false, kind: "retry", reason_code: null, reason: null },
  // The only file is not included and the write set is empty: for a seed with structure but no
//   writable file the server answers absent and 0/0, not undetermined.
  library_overlap: { verdict: "absent", present_files: 0, pending_files: 0 },
};

const GROUP: RSSListGroup = {
  scope: { provider: "anibt", provider_entry_ref: "entry", provider_group_ref: "group", rss_source_id: "source-1" },
  display_name: "Group",
  score: 77,
  selection: { source: "configured" as const, effective_selected: true, selection_mode: "manual" },
  offset: { value: 2, editable: true, source: "link" },
  feed: { state: "fresh" },
  items: [ITEM],
};

function view(mode: RSSListView["mode"], downloadAction?: RSSListItem["download_action"], evaluation?: RSSListItem["evaluation"]): RSSListView {
  return {
    mode,
    config_identity: "owner:managed-1@8",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
    generated_at: "2026-01-01T00:00:00Z",
    groups: [
      {
        ...GROUP,
        items: [{ ...ITEM, download_action: downloadAction ?? ITEM.download_action, evaluation: evaluation ?? ITEM.evaluation }],
      },
    ],
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
  };
}

const WORKSPACE: AnimeWorkspace = {
  managed_anime: { id: "managed-1" },
  subscription: { id: "sub-1" },
  metadata: { managed_anime_id: "managed-1", version: 1 },
  metadata_hold: { held: false, missing: [] },
  subgroups: { effective_selection: [] },
  rules: {},
  rss_sources: [
    { id: "source-1", subscription_id: "sub-1", source_kind: "custom_rss", locator_display: "https://example.test/feed", group_name: "Workspace source name", title: "Workspace title", episode_offset: 0, version: 1 },
  ],
  rss_source_runtimes: [
    { rss_source_id: "source-1", health: "failed", reason_code: "source_degraded", version: 1, failed_work_item_id: "work-1" },
  ],
  releases: [
    {
      id: "release-1",
      rss_source_id: "source-1",
      stable_item_key: "stable-seed",
      raw_facts: { title: "Workspace title", enclosure_length: 999 },
      source_facts: {},
      verdict: "accepted_pending_download",
      reason_code: "old_reason",
      download_task_id: "task-1",
      acquisition_id: "acquisition-1",
      release_files: [{ path: "Show/old.mkv", filename: "old.mkv", size_bytes: 123, media_kind: "video", target_path: "/library/Show/final.mkv", episode: "1", resolution: "1080p" }],
      acquisition_targets: [{ id: "target-1", release_id: "release-1", target_ref: "target-ref", source_path: "Show/old.mkv", media_kind: "video", target_path: "/library/Show/final.mkv", expected_size_bytes: 123, intent: "ensure_present", collision_policy: "block", spec_digest: "target-digest", version: 1 }],
      target_observations: [{ target_id: "target-1", state: "present_matched", observed_size_bytes: 123, observed_at: "2026-01-01T00:00:00Z", version: 1 }],
      version: 1,
    },
  ],
  download_tasks: [{ id: "task-1", state: "failed", failed_work_item_id: "work-download", version: 1 }],
  artifacts: [
    { id: "artifact-1", download_task_id: "task-1", files: [], fingerprints: [], state: "failed", failed_work_item_id: "work-verify", version: 1 },
    { id: "artifact-2", download_task_id: "task-1", files: [], fingerprints: [], state: "waiting_retry", version: 1 },
    { id: "artifact-3", download_task_id: "task-1", files: [{ ref: "file-ref-1", path: "Show/source.mkv", media_kind: "video", fingerprint: { algorithm: "sha256", value: "fingerprint", size_bytes: 123 }, subtitle_binding: { state: "not_applicable" } }], fingerprints: [], state: "verified", version: 1 },
  ],
  file_operations: [
    { id: "operation-1", artifact_id: "artifact-1", artifact_file_ref: "failed-ref", source_path: "Show/fail.mkv", source_fingerprint: { algorithm: "sha256", value: "failed", size_bytes: 123 }, state: "failed", failed_work_item_id: "work-place", operation: "copy", target_path: "/library/Show/EP01.mkv", version: 1 },
    { id: "operation-3", artifact_id: "artifact-3", artifact_file_ref: "file-ref-1", source_path: "Show/source.mkv", source_fingerprint: { algorithm: "sha256", value: "fingerprint", size_bytes: 123 }, state: "completed", operation: "copy", target_path: "/library/Show/final.mkv", version: 1 },
  ],
  placement_handoffs: [],
  logs: [],
  episode_progress: null,
} as unknown as AnimeWorkspace;

function renderOwner(mode: RSSListView["mode"], handlers: OwnerViewHandlers, item?: { downloadAction?: RSSListItem["download_action"]; evaluation?: RSSListItem["evaluation"] }) {
  render(<RssListViewPanel model={ownerViewModel(view(mode, item?.downloadAction, item?.evaluation), WORKSPACE, handlers)} showFiltered />);
  fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
}

/** An owner row with a single file: the file row merges the release's parsed files with the browse layer's verdict. */
function renderOwnerFiles(files: RSSListItem["files"], workspace: AnimeWorkspace = WORKSPACE) {
  const owner = view("owner");
  owner.groups[0].items[0].files = files;
  render(<RssListViewPanel model={ownerViewModel(owner, workspace, {})} showFiltered />);
  fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
  fireEvent.click(screen.getByRole("button", { name: t("rss.expandFiles") }));
}

function fileRow(path: string): HTMLElement {
  return screen.getByText(path).closest(".chain-file") as HTMLElement;
}

/** A release: files come from parsed release_files, target comparison follows the given observation state. */
function releaseWith(files: ReleaseFile[], observation: "present_matched" | "present_mismatched" = "present_matched"): AnimeWorkspace {
  const workspace = structuredClone(WORKSPACE);
  workspace.artifacts = [];
  workspace.file_operations = [];
  workspace.releases[0].release_files = files;
  workspace.releases[0].target_observations = [
    { target_id: "target-1", state: observation, observed_size_bytes: 100, observed_at: "2026-01-01T00:00:00Z", version: 1 },
  ];
  return workspace;
}

describe("ownerViewModel", () => {
  it.each(["queued", "downloading", "completed"] as const)("keeps the download owner state %s visible without an Artifact", (state) => {
    const workspace = structuredClone(WORKSPACE);
    workspace.download_tasks[0].state = state;
    workspace.artifacts = [];
    workspace.file_operations = [];
    render(<RssListViewPanel model={ownerViewModel(view("owner"), workspace, {})} showFiltered />);
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
    expect(screen.getByText(downloadTaskStateLabel(state))).toBeVisible();
  });

  it("projects server seed and file decisions instead of workspace release facts", () => {
    // The score reads the effective selection's effective_score (the same value as the subgroups page), not the owner read model's group.score.
    renderOwner("owner", { groupScores: new Map([["anibt:group", 50]]) });
    expect(screen.getByText("Server title")).toBeTruthy();
    expect(screen.getByText(t("verdict.filteredWithReason", { reason: t("reason.release_must_contain_not_matched") }))).toBeTruthy();
    expect(screen.getByText(t("anime.sources.score", { n: "50" }))).toBeTruthy();
    expect(screen.queryByText(t("anime.sources.score", { n: "77" }))).toBeNull();
    expect(screen.getByText(t("rss.feed.fresh"))).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: t("rss.expandFiles") }));
    expect(screen.getByText("excluded.mkv")).toBeTruthy();
    // Filtering is a normal disposition: the fate badge on row ① takes its word from the code the
//     server sends (file_* means rule-excluded), with the more specific reason in the hover.
    expect(screen.getByText(t("rss.fileExcluded.excluded_by_rule"))).toHaveAttribute("title", t("reason.file_not_in_matched"));
    expect(screen.queryByText("Workspace title")).toBeNull();
  });

  it("does not list downloaded artifacts in the RSS row: file presence belongs to the files dialog", () => {
    renderOwner("owner", {});
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandFiles") }));

    expect(screen.queryByText("已下载文件")).toBeNull();
    expect(screen.queryByText(t("status.artifact.verified"))).toBeNull();
    expect(screen.queryByText("Show/source.mkv")).toBeNull();
    // A completed import no longer has its own mode-and-state row (a failed import still sits in the seed row's failure column).
    expect(screen.queryByText(`${fileOperationKindLabel("copy")} · ${fileOperationStateLabel("completed")}`)).toBeNull();
  });

  // Three rows are assigned by which question the verdict answers: filter belongs to ①, render/naming to ②, pathscan/diff to ③.
  it("合并后只有一个文件段，文件行没有折叠入口与目录/文件名行", () => {
    renderOwner("owner", {});
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandFiles") }));

    // Only one file section, "seed files", remains; "target paths and file states" no longer exists.
    expect(screen.getAllByText(t("rss.seedFiles"))).toHaveLength(1);
    const labels = Array.from(document.querySelectorAll(".chain-files-label")).map((node) => node.textContent);
    expect(labels).toEqual([t("rss.seedFiles")]);

    // The same batch of files no longer gets one row each: the source path appears once.
    expect(screen.getAllByText("Show/old.mkv")).toHaveLength(1);
    const row = fileRow("Show/old.mkv");
    // The ② expected-output line is its own row.
    expect(within(row).getByText("→ /library/Show/final.mkv")).toBeVisible();
    // The ③ actual-state line is a badge; no collapse entry and no directory/name detail rows.
    expect(within(row).getByText(t("rss.target.satisfied"))).toBeVisible();
    expect(row.querySelectorAll("[aria-expanded]")).toHaveLength(0);
    expect(row.querySelectorAll(".file-render-line")).toHaveLength(0);
  });

  it("被 filter 排除的文件只有 ① 行，命运徽章取受理层的处置词", () => {
    const workspace = releaseWith([
      { path: "Show/excluded.mkv", filename: "excluded.mkv", size_bytes: 12, media_kind: "video", outcome: "excluded_by_rule", outcome_reason: "file_not_in_matched" },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-2", ordinal: 0, source_path: "Show/excluded.mkv", filename: "excluded.mkv", size_bytes: 12, media_kind: "video", included: false, reason_code: "file_must_contain_not_matched", safety: "valid" },
    ], workspace);

    // When both layers carry a code the admission one wins, and only one badge renders.
    const badge = screen.getByText(t("rss.fileExcluded.excluded_by_rule"));
    expect(badge).toHaveAttribute("title", t("reason.file_not_in_matched"));
    expect(screen.queryByText(t("rss.fileExcluded.filtered"))).toBeNull();
    // Without an expected path there is no ② or ③: this record has a single row.
    const row = fileRow("Show/excluded.mkv");
    expect(row.querySelectorAll(":scope > .chain-file-row")).toHaveLength(1);
    expect(screen.queryByText(/^→ /)).toBeNull();
  });

  it("没通过硬闸门的文件不出行，获取层解析出来的同一条也不补回", () => {
    const workspace = releaseWith([
      { path: "Show/Subs/extra.ass", filename: "extra.ass", size_bytes: 12, media_kind: "subtitle", outcome: "ignored_nested" },
      { path: "Show/cover.jpg", filename: "cover.jpg", size_bytes: 12, media_kind: "other", outcome: "ignored_kind" },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/Show - 01.mkv", filename: "Show - 01.mkv", size_bytes: 12, media_kind: "video", included: true, safety: "valid" },
    ], workspace);

    expect(screen.getAllByText("Show/Show - 01.mkv")).toHaveLength(1);
    expect(screen.queryByText("Show/Subs/extra.ass")).toBeNull();
    expect(screen.queryByText("Show/cover.jpg")).toBeNull();
  });

  it("绑定成功的字幕不占独立行：视频行上是格式徽章，展开后给出自己的落点", () => {
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/Show - 01.mkv", filename: "Show - 01.mkv", size_bytes: 100, media_kind: "video", included: true, rendered_target: "/library/Show/Season 01/Show - 01.mkv", safety: "valid" },
      { file_ref: "file-2", ordinal: 1, source_path: "Show/Show - 01.zh.ass", filename: "Show - 01.zh.ass", size_bytes: 20, media_kind: "subtitle", language: "zh-Hans", included: true, rendered_target: "/library/Show/Season 01/Show - 01.ass", bound_video_source_path: "Show/Show - 01.mkv", safety: "valid" },
    ]);

    // Collapsed by default: the subtitle never gets its own row, only an ASS badge on the video row.
    expect(screen.queryByText("Show/Show - 01.zh.ass")).toBeNull();
    const row = fileRow("Show/Show - 01.mkv");
    expect(within(row).getByText("→ /library/Show/Season 01/Show - 01.mkv")).toBeVisible();
    const badges = Array.from(row.querySelectorAll<HTMLElement>("[data-subtitle-badge]"));
    expect(badges.map((badge) => [badge.dataset.subtitleBadge, badge.textContent])).toEqual([["placed", "ASS"]]);
    const toggle = within(row).getByRole("button", { name: t("ui.subtitles.expand", { name: "Show/Show - 01.mkv", n: 1 }) });
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    fireEvent.click(toggle);
    const subtitle = within(row).getByText("Show/Show - 01.zh.ass").closest("li") as HTMLElement;
    expect(subtitle).toHaveTextContent("zh-Hans");
    expect(within(subtitle).getByText("→ /library/Show/Season 01/Show - 01.ass")).toBeVisible();
    // Subtitles are not filtered separately and get no independent badge.
    expect(screen.queryByText(t("rss.fileExcluded.subtitle_binding_missing"))).toBeNull();
  });

  it("视频被排除时字幕随视频：中性徽章、不单独显示过滤，展开给出随视频排除、没有落点", () => {
    const workspace = releaseWith([
      { path: "Show/excluded.mkv", filename: "excluded.mkv", size_bytes: 100, media_kind: "video", outcome: "excluded_by_rule", outcome_reason: "file_not_in_matched" },
      { path: "Show/excluded.ass", filename: "excluded.ass", size_bytes: 20, media_kind: "subtitle", outcome: "subtitle_unbound", outcome_reason: "subtitle_video_excluded" },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/excluded.mkv", filename: "excluded.mkv", size_bytes: 100, media_kind: "video", included: false, reason_code: "file_not_in_matched", safety: "valid" },
      { file_ref: "file-2", ordinal: 1, source_path: "Show/excluded.ass", filename: "excluded.ass", size_bytes: 20, media_kind: "subtitle", included: false, reason_code: "subtitle_video_excluded", bound_video_source_path: "Show/excluded.mkv", safety: "valid" },
    ], workspace);

    expect(screen.queryByText("Show/excluded.ass")).toBeNull();
    expect(screen.queryByText(/^→ /)).toBeNull();
    expect(screen.getByText(t("rss.fileExcluded.excluded_by_rule"))).toHaveAttribute("title", t("reason.file_not_in_matched"));
    const row = fileRow("Show/excluded.mkv");
    const badge = row.querySelector<HTMLElement>("[data-subtitle-badge]")!;
    expect(badge.dataset.subtitleBadge).toBe("followsVideo");
    expect(badge.querySelector("s")).toBeNull();

    fireEvent.click(within(row).getByRole("button", { name: t("ui.subtitles.expand", { name: "Show/excluded.mkv", n: 1 }) }));
    const subtitle = within(row).getByText("Show/excluded.ass").closest("li") as HTMLElement;
    expect(subtitle).toHaveTextContent(t("reason.subtitle_video_excluded"));
    expect(subtitle).not.toHaveTextContent(t("verdict.filteredWithReason", { reason: t("reason.subtitle_video_excluded") }));
    expect(screen.queryByText(/^→ /)).toBeNull();
  });

  it("没绑定上视频的字幕独立成行并给出过滤原因，且没有 ②③", () => {
    const workspace = releaseWith([
      { path: "Show/notes.ass", filename: "notes.ass", size_bytes: 20, media_kind: "subtitle", outcome: "subtitle_unbound", outcome_reason: "subtitle_binding_missing" },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/notes.ass", filename: "notes.ass", size_bytes: 20, media_kind: "subtitle", included: false, reason_code: "subtitle_binding_missing", rendered_target: "/library/Show/Show - 01.ass", safety: "valid" },
    ], workspace);

    const badge = screen.getByText(t("rss.fileExcluded.subtitle_binding_missing"));
    expect(badge).toHaveAttribute("title", t("reason.subtitle_binding_missing"));
    const row = fileRow("Show/notes.ass");
    expect(row.querySelectorAll(":scope > .chain-file-row")).toHaveLength(1);
    // With no video to follow there is no expected target: not even the preview layer's rendered path is shown.
    expect(within(row).queryByText(/^→ /)).toBeNull();
    expect(within(row).queryByText(t("rss.target.pending"))).toBeNull();
  });

  it("未登记的过滤码退回「已过滤」，原因退回未说明", () => {
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/extra.mkv", filename: "Show/extra.mkv", included: false, reason_code: "a_new_filter_code", safety: "valid" },
    ]);

    const badge = screen.getByText(t("rss.fileExcluded.filtered"));
    expect(badge).toHaveAttribute("title", t("common.unspecifiedReason"));
    expect(badge.textContent).not.toContain("a_new_filter_code");
  });

  it("渲染域诊断 + 无落点：② 用该原因占位", () => {
    // When the naming engine rejects a relative directory the diagnostic keeps the raw code as sent (uppercase).
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_diagnostic: { code: "NAMING_TARGET_NOT_ABSOLUTE", message: "命名模板的输出目录不合法：必须渲染绝对路径" } },
    ]);
    // The render itself produced no target: neither a frozen target nor a browse-layer projection.
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid" },
    ], workspace);

    // Row ②: where render/naming verdicts live.
    expect(screen.getByText(t("reason.naming_target_not_absolute"))).toBeVisible();
    expect(screen.queryByText(/命名失败/)).toBeNull();
    // With no target there is no ③; this record has ① and ② only.
    const row = fileRow("Show/old.mkv");
    expect(row.querySelectorAll(":scope > .chain-file-row")).toHaveLength(1);
    expect(screen.queryByText(/^→ /)).toBeNull();
  });

  it("执行域诊断 + 无落点：② 中性「目标待定」，不显示执行原因", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_diagnostic: { code: "target_probe_unavailable", message: "无法检查目标位置" } },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid" },
    ], workspace);

    // ② degrades into the neutral placeholder; probe/execution-domain verdicts never enter ②.
    expect(screen.getByText(t("rss.targetPending"))).toBeVisible();
    expect(screen.queryByText(t("reason.target_probe_unavailable"))).toBeNull();
    // With no target there is no ③; this record has ① and ② only.
    const row = fileRow("Show/old.mkv");
    expect(row.querySelectorAll(":scope > .chain-file-row")).toHaveLength(1);
    expect(screen.queryByText(/^→ /)).toBeNull();
  });

  it("执行域失败不覆盖预期输出：② 保留渲染目标，③ 才说实际输出失败", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_diagnostic: { code: "target_probe_unavailable", message: "无法检查目标位置" } },
    ]);
    // An admission failure leaves no frozen target; the render result still comes down via the browse layer.
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid", rendered_target: "FX战士久留美/Season 01/FX战士久留美 - E01 [1080p].mp4" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    // ② The expected output: a successful render, unchanged by the library configuration.
    expect(within(row).getByText("→ FX战士久留美/Season 01/FX战士久留美 - E01 [1080p].mp4")).toBeVisible();
    // ③ The actual output: execution/probe failures belong here, replacing the old misleading "awaiting comparison".
    expect(within(row).getByText(t("reason.target_probe_unavailable"))).toBeVisible();
    expect(within(row).queryByText(t("rss.target.pending"))).toBeNull();
  });

  it("② 行只放预期输出：执行域诊断只出现在 ③ 行", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_diagnostic: { code: "target_probe_unavailable", message: "无法检查目标位置" } },
    ]);
    // A failed pre-placement check (e.g. an unusable library root) leaves no frozen target, but the
//     render result has already come down: the diagnostic belongs to the execution domain, so it can
//     neither push out ②'s expected output nor take ②'s place.
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid", rendered_target: "Show/Season 01/Show - E03 [1080p].mp4" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    // The ② layer holds only the target row; diagnostic text never appears in it.
    const expected = Array.from(row.querySelectorAll(":scope > .chain-file-path-pending"));
    expect(expected.map((node) => node.textContent)).toEqual(["→ Show/Season 01/Show - E03 [1080p].mp4"]);
    // The ③ layer: the verdict sits on the second row, a different row from ①.
    const rows = Array.from(row.querySelectorAll(":scope > .chain-file-row"));
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).not.toContain(t("reason.target_probe_unavailable"));
    expect(rows[1]?.textContent).toContain(t("reason.target_probe_unavailable"));
  });

  it("只有命名缺信息的码才说「命名失败」", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", media_kind: "video", target_diagnostic: { code: "naming_fact_missing_anime_tmdb_workid", message: "命名失败：缺少 TMDB 编号" } },
    ]);
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", media_kind: "video", included: true, safety: "valid" },
    ], workspace);

    expect(screen.getByText(knownReasonLabel("naming_fact_missing_anime_tmdb_workid") ?? "")).toBeVisible();
  });

  it("③ 行在落点一致时只留状态徽章，不重复路径", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_path: "/library/Show/Show - 03.mkv", outcome: "planned" },
    ]);
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText("→ /library/Show/Show - 03.mkv")).toBeVisible();
    expect(within(row).getByText(t("rss.target.satisfied"))).toBeVisible();
    expect(within(row).queryByText("/library/Show/Show - 03.mkv")).toBeNull();
  });

  it("已入库的文件 ② 显示实际入库位置；模板渲染出新路径时再给一行待对齐的新路径", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_path: "/library/Show 2021/Season 01/Show - 03.mkv", outcome: "planned" },
    ]);
    renderOwnerFiles([
      {
        file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid",
        library_path: "Show 2021/Season 01/Show - 03.mkv", library_new_path: "Show （2021）/Season 01/Show - 03.mkv",
      },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText(t("rss.target.libraryAt", { path: "Show 2021/Season 01/Show - 03.mkv" }))).toBeVisible();
    expect(within(row).getByText(t("rss.target.libraryNewPath", { path: "Show （2021）/Season 01/Show - 03.mkv" }))).toBeVisible();
    // The frozen target no longer appears as ②.
    expect(within(row).queryByText("→ /library/Show 2021/Season 01/Show - 03.mkv")).toBeNull();
  });

  // After an alignment move the observation on the frozen target no longer matches; ③ follows the server's in_library and never says "awaiting import" again.
  it("对齐移动后的已入库文件 ③ 显示已入库，不显示待入库", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_path: "/library/Show/Show - 03.mkv", outcome: "planned" },
    ]);
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      {
        file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid",
        in_library: true, library_path: "Show 2021/Season 01/Show - 03.mkv",
      },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText(t("rss.target.inLibrary"))).toBeVisible();
    expect(within(row).queryByText(t("rss.target.pending"))).toBeNull();
    expect(within(row).getByText(t("rss.target.libraryAt", { path: "Show 2021/Season 01/Show - 03.mkv" }))).toBeVisible();
  });

  it("没入库的文件照旧显示目标路径", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", target_path: "/library/Show/Show - 03.mkv", outcome: "planned" },
    ]);
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: true, safety: "valid" },
    ], workspace);
    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText("→ /library/Show/Show - 03.mkv")).toBeVisible();
    expect(within(row).queryByText(t("rss.target.libraryAt", { path: "Show/Show - 03.mkv" }))).toBeNull();
  });

  // On a size mismatch the ③ row does not repeat ②'s target, it says where the difference lies:
//   the actual size in the library versus the expected size. When only one side's size is available
//   only the state badge remains, never a half sentence.
  it("③ 行在尺寸不符时给出库中尺寸与期望尺寸", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 200, media_kind: "video", target_path: "/library/Show/Show - 03.mkv", outcome: "planned" },
    ], "present_mismatched");
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 200, media_kind: "video", included: true, safety: "valid" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText(t("rss.target.mismatchSizes", { observed: formatBytes(100), expected: formatBytes(200) }))).toBeVisible();
    // The target appears exactly once, on row ②.
    expect(within(row).getByText("→ /library/Show/Show - 03.mkv")).toBeVisible();
    expect(within(row).queryByText("/library/Show/Show - 03.mkv")).toBeNull();
  });

  it("③ 行在拿不到期望尺寸时只留状态徽章", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", media_kind: "video", target_path: "/library/Show/Show - 03.mkv", outcome: "planned" },
    ], "present_mismatched");
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", media_kind: "video", included: true, safety: "valid" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText(t("rss.target.mismatch"))).toBeVisible();
    expect(within(row).queryByText(/尺寸不符（/)).toBeNull();
  });

  it("计划时就有的库比对结论归 ③，不出 ②", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", outcome: "covered_existing", outcome_reason: "covered_existing" },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: false, reason_code: "covered_existing", safety: "valid" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText(t("reason.covered_existing"))).toBeVisible();
    expect(screen.queryByText(/^→ /)).toBeNull();
    expect(screen.queryByText(t("rss.fileExcluded.filtered"))).toBeNull();
  });

  it("目标重复是冲突（③），不是渲染失败（②）", () => {
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", outcome: "target_collision", outcome_reason: "target_duplicate" },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: 100, media_kind: "video", included: false, reason_code: "target_duplicate", safety: "valid" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    expect(within(row).getByText(t("reason.target_duplicate"))).toBeVisible();
    expect(screen.queryByText(/^→ /)).toBeNull();
  });

  it("未知尺寸与类型不渲染 0 B 与「其他」，退化为文件名扩展名", () => {
    const workspace = releaseWith([]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/mystery.bin", filename: "Show/mystery.bin", included: false, reason_code: "ignored_kind", safety: "valid" },
    ], workspace);

    expect(screen.queryByText("0 B")).toBeNull();
    expect(screen.queryByText(t("rss.fileKind.other"))).toBeNull();
    expect(screen.getByText("bin")).toBeTruthy();
  });

  it("一个文件一行：真值来自 release 已解析的文件，事实徽章各只出现一次", () => {
    const size = 748_486_656;
    const workspace = releaseWith([
      { path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: size, media_kind: "video", target_path: "/library/Show/Show - 03.mkv", episode: "3", resolution: "1080p", revision: "v2", language: "zh-Hans", outcome: "planned" },
    ]);
    workspace.releases[0].acquisition_targets = [];
    workspace.releases[0].target_observations = [];
    renderOwnerFiles([
      { file_ref: "file-1", ordinal: 0, source_path: "Show/old.mkv", filename: "Show - 03.mkv", size_bytes: size, media_kind: "video", included: true, safety: "valid", rendered_target: "/library/Show/Show - 03.mkv" },
    ], workspace);

    const row = fileRow("Show/old.mkv");
    // Row ①: source path, size, type and the episode/resolution/revision/language badges (no expansion needed).
    expect(screen.getAllByText("Show/old.mkv")).toHaveLength(1);
    expect(within(row).getAllByText(formatBytes(size))).toHaveLength(1);
    expect(within(row).getAllByText("mkv")).toHaveLength(1);
    expect(within(row).getByText(t("rss.episode", { n: "3" }))).toBeVisible();
    expect(within(row).getByText("1080p")).toBeVisible();
    expect(within(row).getByText("v2")).toBeVisible();
    expect(within(row).getByText("zh-Hans")).toBeVisible();
    // Row ②: the target path is its own row.
    expect(within(row).getByText("→ /library/Show/Show - 03.mkv")).toBeVisible();
    // Row ③: the state badge.
    expect(within(row).getByText(t("rss.target.pending"))).toBeVisible();
  });

  it("AniBT 标签与标题解析标签按来源各占一行，全部显示；命中必须的加强，命中排除的用警示色", () => {
    const owner = view("owner");
    owner.groups[0].items[0].seed = {
      ...owner.groups[0].items[0].seed,
      anibt_tags: ["resolution:1080p", "language:CHT"],
      title_tags: ["resolution:1080p", "source_type:WEB-DL"],
    };
    const workspace = {
      ...WORKSPACE,
      rules: {
        ...WORKSPACE.rules,
        effective_tag_filters: { anibt: { require: [], exclude: ["Language:cht"] }, title: { require: ["resolution:1080P"], exclude: [] } },
      },
    };
    render(<RssListViewPanel model={ownerViewModel(owner, workspace, {})} showFiltered />);
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));

    const tags = screen.getByLabelText(t("tagFilters.seedRowAria"));
    expect(within(tags).queryByRole("button")).toBeNull();
    const anibt = within(tags).getByLabelText(t("anibtTags.rowAria"));
    const title = within(tags).getByLabelText(t("titleTags.rowAria"));
    expect(within(anibt).getByText(t("tagFilters.sourceAniBT"))).toBeVisible();
    expect(within(title).getByText(t("tagFilters.sourceTitle"))).toBeVisible();
    // No cross-source dedup: one per row for the two 1080p entries; the hover spells out the type.
    const anibt1080 = within(anibt).getByText("1080p");
    const title1080 = within(title).getByText("1080p");
    expect(anibt1080.getAttribute("title")).toContain("1080p");
    expect(anibt1080).not.toHaveClass("chain-file-badge-strong");
    // Matched by each row's own source rule, case-insensitively.
    expect(title1080).toHaveClass("chain-file-badge-strong");
    expect(within(anibt).getByText("CHT")).toHaveClass("chain-file-badge-excluded");
    expect(within(title).getByText("WEB-DL")).toHaveClass("chain-file-badge");
    expect(within(title).getByText("WEB-DL")).not.toHaveClass("chain-file-badge-strong");
  });

  it("只有一个来源有标签时也写出来源", () => {
    const owner = view("owner");
    owner.groups[0].items[0].seed = { ...owner.groups[0].items[0].seed, anibt_tags: [], title_tags: ["resolution:720p"] };
    render(<RssListViewPanel model={ownerViewModel(owner, WORKSPACE, {})} showFiltered />);
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
    const tags = screen.getByLabelText(t("tagFilters.seedRowAria"));
    expect(within(tags).getByText(t("tagFilters.sourceTitle"))).toBeVisible();
    expect(within(tags).queryByText(t("tagFilters.sourceAniBT"))).toBeNull();
    expect(within(tags).getByText("720p")).toBeVisible();
  });

  it("等待元数据时状态已表达原因，不再重复显示原因行", () => {
    renderOwner("owner", {}, { evaluation: { state: "evaluated", verdict: "waiting_metadata", reason_code: "metadata_incomplete" } });
    expect(screen.getByText(t("verdict.waiting_metadata"))).toBeTruthy();
    expect(screen.queryByText(t("reason.metadata_incomplete"))).toBeNull();
    expect(screen.queryByText(t("common.unknownReason"))).toBeNull();
  });

  it("等待元数据的种子按读模型的 hold 写出等的是哪项关联", () => {
    const held = view("owner", undefined, { state: "evaluated", verdict: "waiting_metadata", reason_code: "metadata_incomplete" });
    held.metadata_hold = { held: true, missing: ["tmdb"] };
    render(<RssListViewPanel model={ownerViewModel(held, WORKSPACE, {})} showFiltered />);
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
    expect(screen.getByText(t("anime.metadata.waiting", { names: "TMDB" }))).toBeTruthy();
  });

  it("未登记的原因码仍显示“未知原因”，原始代码放进 tooltip", () => {
    renderOwner("owner", {}, { evaluation: { state: "evaluated", verdict: "waiting_metadata", reason_code: "never_registered_code" } });
    const unknown = screen.getByText(t("common.unknownReason"));
    expect(unknown).toHaveAttribute("title", "never_registered_code");
  });

  it("同名字幕组在名称后显示短标识，名称唯一时不显示", () => {
    const other: RSSListGroup = {
      ...GROUP,
      scope: { ...GROUP.scope, provider_group_ref: "group-other-ref-999", rss_source_id: undefined },
    };
    const model = ownerViewModel({ ...view("owner"), groups: [GROUP, other] }, WORKSPACE, {});
    render(<RssListViewPanel model={model} showFiltered />);
    expect(screen.getAllByText(/^Group（#/)).toHaveLength(2);
    expect(screen.queryByText("Group")).toBeNull();
  });

  it("发布时间缺失时不显示占位文字，只保留大小", () => {
    const missingTime = { ...ITEM, seed: { ...ITEM.seed, published_at: null } };
    const model = ownerViewModel({ ...view("owner"), groups: [{ ...GROUP, items: [missingTime] }] }, WORKSPACE, {});
    render(<RssListViewPanel model={model} showFiltered />);
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
    expect(screen.queryByText(/未知时间/)).toBeNull();
    expect(screen.getByText("123 B")).toBeTruthy();
  });

  it("renders the server reevaluating state as its own status", () => {
    renderOwner("owner", {}, { evaluation: { state: "reevaluating", reason_code: "rules_preview" } });
    expect(screen.getByText(t("verdict.reevaluating"))).toBeTruthy();
  });

  it("keeps capability-gated owner actions live", () => {
    const onRetryRelease = vi.fn();
    const onOpenRecord = vi.fn();
    const onOpenOffset = vi.fn();
    const onOpenStructure = vi.fn();
    const onRetry = vi.fn();
    renderOwner(
      "owner",
      { onRetryRelease, onOpenRecord, onOpenOffset, onOpenStructure, onRetry },
      { downloadAction: { allowed: true, kind: "retry", reason_code: null, reason: null } },
    );

    fireEvent.click(screen.getByLabelText(t("rss.retryRelease")));
    expect(onRetryRelease).toHaveBeenCalledWith(WORKSPACE.releases[0]);
    fireEvent.click(screen.getByLabelText(t("records.openFromRelease")));
    expect(onOpenRecord).toHaveBeenCalledWith("acquisition-1");
    fireEvent.click(screen.getByLabelText(t("rss.structure.open")));
    expect(onOpenStructure).toHaveBeenCalledWith(WORKSPACE.releases[0]);
    fireEvent.click(screen.getByLabelText(t("rss.offsetTitle", { name: "Group" })));
    expect(onOpenOffset).toHaveBeenCalledWith(WORKSPACE.rss_sources[0]);
    fireEvent.click(screen.getByRole("button", { name: t("rss.retryRefresh") }));
    expect(onRetry).toHaveBeenCalledWith({ ownerType: "rss_source_runtime", ownerId: "source-1", failedWorkItemId: "work-1" });

    expect(screen.getByText("下载失败")).toBeTruthy();
    expect(screen.getByText(t("status.artifact.failed"))).toBeTruthy();
    expect(screen.getByText(t("settings.notifications.trigger.file_operation_failed"))).toBeTruthy();
    expect(screen.getByText(/复制 · EP01.mkv/)).toBeTruthy();
    expect(screen.getAllByText(t("settings.delivery.state.waiting_retry"))).toHaveLength(1);

    // The three stage owners' retry buttons are retired: no entry left in the row, only state, reason and the replacement-entry note.
    for (const label of ["重试下载", "重试校验", "重试入库"]) {
      expect(screen.queryByRole("button", { name: label })).toBeNull();
    }
    // Each of the three failure rows carries one line of note; the waiting_retry row carries none (it never had a manual entry).
    expect(screen.queryByText("重新获取将在后续版本提供")).not.toBeInTheDocument();
    // RSS site refresh is unaffected: onRetry still fires only for rss_source_runtime, once.
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("offers 查看处理过程 only for a release with an execution record", () => {
    const onOpenRecord = vi.fn();
    const onOpenStructure = vi.fn();
    const workspace = { ...WORKSPACE, releases: [{ ...WORKSPACE.releases[0], acquisition_id: undefined }] };
    render(<RssListViewPanel model={ownerViewModel(view("owner"), workspace, { onOpenRecord, onOpenStructure })} showFiltered />);
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
    expect(screen.queryByLabelText(t("records.openFromRelease"))).toBeNull();
    // The structure preview reads the release itself, not its execution record.
    expect(screen.getByLabelText(t("rss.structure.open"))).toBeTruthy();
  });

  it("honors the server gate when retry is not allowed", () => {
    renderOwner("owner", { onRetryRelease: vi.fn() });
    expect(screen.getByLabelText(t("rss.retryRelease"))).toBeDisabled();
  });

  it("renders every action disabled in owner_draft and never dispatches a handler", () => {
    const onRetryRelease = vi.fn();
    const onOpenRecord = vi.fn();
    const onOpenOffset = vi.fn();
    const onOpenStructure = vi.fn();
    const onRetry = vi.fn();
    renderOwner("owner_draft", { onRetryRelease, onOpenRecord, onOpenOffset, onOpenStructure, onRetry });

    for (const label of [t("rss.retryRelease"), t("records.openFromRelease"), t("rss.structure.open"), t("rss.offsetTitle", { name: "Group" }), t("rss.retryRefresh")]) {
      const control = screen.getByLabelText(`${label}：预览模式下不可操作`);
      expect(control).toBeDisabled();
      fireEvent.click(control);
    }

    // Preview mode likewise renders no retired stage-retry button.
    for (const label of ["重试下载", "重试校验", "重试入库"]) {
      expect(screen.queryByRole("button", { name: label })).toBeNull();
    }

    expect(onRetryRelease).not.toHaveBeenCalled();
    expect(onOpenRecord).not.toHaveBeenCalled();
    expect(onOpenOffset).not.toHaveBeenCalled();
    expect(onOpenStructure).not.toHaveBeenCalled();
    expect(onRetry).not.toHaveBeenCalled();
  });
});
