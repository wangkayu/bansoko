import { describe, expect, it } from "vitest";
import {
  buildOwnerChain,
  isWorkspaceOwnerChainStable,
  type ReleaseChain,
} from "./ownerChain";
import type { AnimeWorkspace, Artifact, DownloadTask, FileOperation, Release } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { presetRuleSections } from "../anime-config/businessDraft";
import { preset } from "../../testing/businessPreset";

function workspaceFixture(): AnimeWorkspace {
  return {
    managed_anime: { id: "a1", title: "测试番", title_override: null, version: 1 },
    placement_handoffs: [],
    subscription: {
      id: "sub1",
      managed_anime_id: "a1",
      origin: "custom",
      origin_entry_ref: null,
      instance_label: "测试实例",
      state: "active",
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      version: 1,
      new_releases_only: false,
    },
    metadata: { managed_anime_id: "a1", bgm: null, tmdb: null, version: 1 },
    metadata_hold: { held: true, missing: ["bgm", "tmdb"] },
    subgroups: {
      managed_anime_id: "a1",
      subgroups: { max_rss: 3, entries: [] },
      effective_selection: [],
      rss_sources: [],
      version: 1,
    },
    rules: {
      managed_anime_id: "a1",
      sections: presetRuleSections(preset),
      effective: {
        parser: { episode_patterns: [] },
        release_filter: { must_contain: "", not_in: "" },
        file_filter: { must_contain: "", not_in: "" },
        naming: {
          language: "js-naming-1",
          unknown: { directory_template: "", video_filename_template: "" },
          tv: { directory_template: "{title}", video_filename_template: "{title}" },
          movie: { directory_template: "{title}", video_filename_template: "{title}" },
          subtitle_suffix_template: "",
        },
        file_effect: { operation: "hardlink", collision_policy: "versioned" },
      },
      effective_tag_filters: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } },
      has_anibt_source: false,
      version: 1,
    },
    rss_sources: [
      {
        id: "s1",
        subscription_id: "sub1",
        source_kind: "custom_rss",
        locator_display: "http://feed.local/rss",
        group_name: "测试字幕组",
        title: "测试番",
        episode_offset: 0,
        version: 1,
      },
    ],
    rss_source_runtimes: [
      { rss_source_id: "s1", health: "ready", version: 2 },
    ],
    releases: [
      {
        id: "r1",
        rss_source_id: "s1",
        stable_item_key: "k1",
        raw_facts: { title: "[Group] Test - 01" },
        source_facts: { identity_kind: "guid", identity_value: "k1" },
        verdict: "submitted",
        reason_code: "accepted",
        download_task_id: "t1",
        version: 1,
      },
      {
        id: "r2",
        rss_source_id: "s1",
        stable_item_key: "k2",
        raw_facts: { title: "[Group] Test - 02" },
        source_facts: { identity_kind: "guid", identity_value: "k2" },
        verdict: "filtered",
        reason_code: "exclude_keyword",
        version: 1,
      },
    ],
    download_tasks: [
      {
        id: "t1",
        release_id: "r1",
        frozen_input: { release_stable_item_key: "k1", source_locator_display: "http://feed.local/rss" },
        frozen_policy: {
          rules_version: 1,
          metadata_version: 1,
          effective_sections: {},
          section_versions: {},
          section_schemas: {},
          effective_digest: "sha256:" + "0".repeat(64),
          config_identity: { identity_kind: "instance_materialized", effective_digest: "sha256:" + "0".repeat(64) },
        },
        state: "completed",
        idempotency_key: "idem",
        version: 1,
      },
    ],
    artifacts: [
      {
        id: "art1",
        download_task_id: "t1",
        files: [],
        fingerprints: [],
        state: "verified",
        version: 1,
      },
    ],
    file_operations: [
      {
        id: "f1",
        artifact_id: "art1",
        artifact_file_ref: "file-1",
        operation: "hardlink",
        source_path: "/dl/test.mkv",
        target_path: "/library/test.mkv",
        source_fingerprint: { algorithm: "sha256", value: "a".repeat(64), size_bytes: 10 },
        collision_policy: "versioned",
        frozen_render_snapshot: { renderer_version: "v1", directory: "d", filename: "f" },
        frozen_config_snapshot: { rules_version: 1, safety_version: "v1" },
        state: "completed",
        idempotency_key: "fo-idem",
        version: 1,
      },
    ],
    logs: [],
    episode_progress: null,
    config_waiting_files: 0,
  };
}

describe("buildOwnerChain", () => {
  it("把 source → release → task → artifact → file op 归组", () => {
    const chain = buildOwnerChain(workspaceFixture());
    expect(chain).toHaveLength(1);
    const source = chain[0]!;
    expect(source.source.id).toBe("s1");
    expect(source.runtime?.health).toBe("ready");
    expect(source.releases).toHaveLength(2);

    const submitted = source.releases.find((r) => r.release.id === "r1")!;
    expect(submitted.downloadTask?.id).toBe("t1");
    expect(submitted.artifacts.map((a) => a.id)).toEqual(["art1"]);
    expect(submitted.fileOperations.map((f) => f.id)).toEqual(["f1"]);

    const filtered = source.releases.find((r) => r.release.id === "r2")!;
    expect(filtered.downloadTask).toBeUndefined();
    expect(filtered.artifacts).toEqual([]);
    expect(filtered.fileOperations).toEqual([]);
  });

  it("没有 runtime 或 release 时保持空", () => {
    const fixture = workspaceFixture();
    fixture.rss_source_runtimes = [];
    fixture.releases = [];
    const chain = buildOwnerChain(fixture);
    expect(chain[0]?.runtime).toBeUndefined();
    expect(chain[0]?.releases).toEqual([]);
  });
});

const release: Release = {
  id: "release-1",
  rss_source_id: "s1",
  stable_item_key: "stable-1",
  raw_facts: { title: "测试发布" },
  source_facts: { identity_kind: "guid", identity_value: "stable-1" },
  verdict: "submitted",
  reason_code: "submitted",
  download_task_id: "task-1",
  version: 1,
};

function downloadTask(state: DownloadTask["state"]): DownloadTask {
  return {
    id: "task-1",
    release_id: release.id,
    frozen_input: { release_stable_item_key: release.stable_item_key, source_locator_display: "feed" },
    frozen_policy: {
          rules_version: 1,
          metadata_version: 1,
          effective_sections: {},
          section_versions: {},
          section_schemas: {},
          effective_digest: "sha256:" + "0".repeat(64),
          config_identity: { identity_kind: "instance_materialized", effective_digest: "sha256:" + "0".repeat(64) },
        },
    state,
    idempotency_key: "download-1",
    version: 1,
  };
}

function artifact(state: Artifact["state"]): Artifact {
  return {
    id: "artifact-1",
    download_task_id: "task-1",
    files: [],
    fingerprints: [],
    state,
    version: 1,
  };
}

function fileOperation(id: string, state: FileOperation["state"]): FileOperation {
  return {
    id,
    artifact_id: "artifact-1",
    artifact_file_ref: `file-${id}`,
    operation: "hardlink",
    source_path: `/downloads/${id}.mkv`,
    target_path: `/library/${id}.mkv`,
    source_fingerprint: { algorithm: "sha256", value: "a".repeat(64), size_bytes: 1 },
    collision_policy: "versioned",
    frozen_render_snapshot: { renderer_version: "1", directory: "library", filename: `${id}.mkv` },
    frozen_config_snapshot: { rules_version: 1, safety_version: "1" },
    state,
    idempotency_key: `file-operation-${id}`,
    version: 1,
  };
}

function releaseChain(overrides: Partial<ReleaseChain> = {}): ReleaseChain {
  return { release, artifacts: [], fileOperations: [], ...overrides };
}

describe("isWorkspaceOwnerChainStable", () => {
  it.each([
    ["unknown", false],
    ["ready", true],
    ["degraded", true],
    ["failed", true],
  ] as const)("treats source runtime health %s without releases as stable=%s", (health, stable) => {
    const fixture = workspaceFixture();
    fixture.releases = [];
    fixture.download_tasks = [];
    fixture.artifacts = [];
    fixture.file_operations = [];
    fixture.rss_source_runtimes = [{ rss_source_id: "s1", health, version: 1 }];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(stable);
  });

  it("keeps polling when a source has no runtime facts", () => {
    const fixture = workspaceFixture();
    fixture.releases = [];
    fixture.download_tasks = [];
    fixture.artifacts = [];
    fixture.file_operations = [];
    fixture.rss_source_runtimes = [];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(false);
  });

  it("does not treat a workspace without sources as stable", () => {
    const fixture = workspaceFixture();
    fixture.rss_sources = [];
    fixture.rss_source_runtimes = [];
    fixture.releases = [];
    fixture.download_tasks = [];
    fixture.artifacts = [];
    fixture.file_operations = [];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(false);
  });

  it("checks no-release runtime facts per source when another source has stable releases", () => {
    const fixture = workspaceFixture();
    fixture.rss_sources.push({
      ...fixture.rss_sources[0]!,
      id: "s2",
      group_name: "第二来源",
    });
    fixture.rss_source_runtimes.push({ rss_source_id: "s2", health: "unknown", version: 1 });
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(false);

    fixture.rss_source_runtimes[1] = { rss_source_id: "s2", health: "ready", version: 2 };
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(true);
  });

  it.each([
    ["discovered", false],
    ["accepted_pending_download", false],
    ["submitted", false],
    ["waiting_metadata", true],
    ["subscription_stopped", true],
    ["source_unavailable", true],
    ["parse_failed", true],
    ["filtered", true],
    ["adopted_existing", true],
    ["acquisition_failed", true],
  ] as const)("treats release verdict %s without downstream owners as stable=%s", (verdict, stable) => {
    const fixture = workspaceFixture();
    fixture.releases = [{ ...release, verdict, download_task_id: undefined }];
    fixture.download_tasks = [];
    fixture.artifacts = [];
    fixture.file_operations = [];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(stable);
  });

  it.each([
    ["queued", false],
    ["submitting", false],
    ["downloading", false],
    ["completed", false],
    ["waiting_retry", false],
    ["failed", true],
  ] as const)("treats deepest DownloadTask state %s as stable=%s", (state, stable) => {
    const fixture = workspaceFixture();
    fixture.releases = [release];
    fixture.download_tasks = [downloadTask(state)];
    fixture.artifacts = [];
    fixture.file_operations = [];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(stable);
  });

  it.each([
    ["pending", false],
    ["verified", false],
    ["waiting_retry", false],
    ["failed", true],
  ] as const)("treats deepest Artifact state %s as stable=%s", (state, stable) => {
    const fixture = workspaceFixture();
    fixture.releases = [release];
    fixture.download_tasks = [downloadTask("completed")];
    fixture.artifacts = [artifact(state)];
    fixture.file_operations = [];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(stable);
  });

  it.each([
    ["pending", false],
    ["running", false],
    ["waiting_retry", false],
    ["completed", true],
    ["failed", true],
  ] as const)("treats deepest FileOperation state %s as stable=%s", (state, stable) => {
    const fixture = workspaceFixture();
    fixture.releases = [release];
    fixture.download_tasks = [downloadTask("completed")];
    fixture.artifacts = [artifact("verified")];
    fixture.file_operations = [fileOperation("one", state)];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(stable);
  });

  it("requires every release and file operation to be stable", () => {
    const fixture = workspaceFixture();
    fixture.file_operations = [fileOperation("one", "completed"), fileOperation("two", "running")];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(false);
  });

  it("keeps polling until every verified artifact has a file operation", () => {
    const fixture = workspaceFixture();
    fixture.artifacts = [
      artifact("verified"),
      { ...artifact("verified"), id: "artifact-2" },
    ];
    fixture.file_operations = [fileOperation("one", "completed")];
    expect(isWorkspaceOwnerChainStable(fixture)).toBe(false);
  });
});
