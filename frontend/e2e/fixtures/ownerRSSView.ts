import type { AnimeWorkspace, RSSListView } from "../../src/lib/api/client";

/** HTTP fixture projection, not a parser/evaluator: verdicts are explicit fixture facts. */
export function ownerRSSView(workspace: AnimeWorkspace): RSSListView {
  return {
    mode: "owner", config_identity: `owner:${workspace.managed_anime.id}@1`,
    metadata_hold: workspace.metadata_hold,
    versions: { parser: "fixture-parser", evaluator: "fixture-evaluator", renderer: "fixture-renderer", safety: "fixture-safety", torrent_parser: "fixture-torrent" },
    generated_at: "2026-09-01T00:00:00Z", unused_feed_identities: [], truncated: false, limit_per_group: 100,
    groups: workspace.rss_sources.map((source) => ({
      scope: { provider: "anibt", provider_entry_ref: "anibt:stellar-mail-ella", provider_group_ref: "ranked-group", rss_source_id: source.id },
      display_name: source.group_name,
      // RSSListGroupSelection.source 是契约必填：configured 表示来自持久化选择，
      // observed_default 表示服务端默认补选（UI 据此渲染「默认组」徽标）。fixture
      // 语义是显式选中，故 configured。
      selection: { source: "configured", effective_selected: true, selection_mode: "auto", count_in_limit: true },
      offset: { value: source.episode_offset, editable: true, source: "link" },
      feed: { state: "fresh" },
      items: workspace.releases.filter((release) => release.rss_source_id === source.id).map((release) => ({
        snapshot_id: `release:${release.id}`, stable_seed_key: release.stable_item_key,
        seed: { title: release.raw_facts.title ?? "Fixture seed", published_at: release.raw_facts.published_at ?? null, locator_display: source.locator_display },
        torrent: { state: "not_applicable" },
        evaluation: { state: "evaluated", verdict: release.verdict, reason_code: release.reason_code, parsed_facts: release.parsed_facts },
        files: [],
        download_action: { allowed: !release.download_task_id, kind: release.download_task_id ? "none" : "retry" },
        // 服务端对「没有任何 file 行」的种子给出 undetermined，而不是 absent：
        // fixture 的 files 为空，因此结论必须与之一致。
        library_overlap: { verdict: "undetermined", present_files: null, pending_files: null, reason_code: "no_rendered_targets" },
      })),
    })),
  };
}
