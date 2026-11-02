import { describe, expect, it } from "vitest";
import type { ObservabilityRecord } from "../lib/api/client";
import { batchCountsText, dedupeReplacements, entryView, groupEvents, runView, stepText, type BusinessEntry } from "./logRuns";

let seq = 0;
function entry(overrides: Partial<BusinessEntry>): BusinessEntry {
  seq += 1;
  return {
    id: `e${seq}`,
    subscription_id: "sub-1",
    managed_anime_id: "anime-1",
    anime_title: "Example",
    source: "subscription",
    kind: "subscription_created",
    outcome: "info",
    occurred_at: "2026-10-21T08:40:00Z",
    message_code: "subscription_created",
    message_params: {},
    dedupe_key: `d${seq}`,
    retention_until: "2026-11-21T08:40:00Z",
    ...overrides,
  };
}

const record = (value: BusinessEntry): ObservabilityRecord => ({ record_type: "business", entry: value });
const at = (minute: number, second = 0) => `2026-10-21T08:${String(minute).padStart(2, "0")}:${String(second).padStart(2, "0")}Z`;

function download(release: string, minute: number, source: BusinessEntry["source"] = "subscription") {
  const common = { release_id: release, source, details: { episodes: [2], group_name: "NEST", release_title: "[NEST] Example - 02.mkv" } };
  return [
    entry({ ...common, kind: "acquisition_settled", outcome: "success", message_code: "acquisition_succeeded", occurred_at: at(minute, 50) }),
    entry({ ...common, kind: "file_alignment_completed", outcome: "success", message_code: "file_imported", occurred_at: at(minute, 20) }),
    entry({ ...common, kind: "download_changed", outcome: "success", message_code: "download_completed", occurred_at: at(minute, 0) }),
  ];
}

describe("groupEvents", () => {
  it("one download is one run row with steps in ascending time", () => {
    const items = groupEvents(download("r-1", 40).map(record));
    expect(items).toHaveLength(1);
    const run = items[0]!;
    if (run.type !== "run") throw new Error("expected a run");
    expect(run.steps.map((step) => step.kind)).toEqual(["download_changed", "file_alignment_completed", "acquisition_settled"]);
    expect(runView(run.steps).state).toBe("success");
  });

  it("collapses adjacent identical singles and sorts rows newest first", () => {
    const singles = [entry({ occurred_at: at(10) }), entry({ occurred_at: at(9) })];
    const items = groupEvents([...download("r-1", 20), ...singles].map(record));
    expect(items.map((item) => item.type)).toEqual(["run", "group"]);
  });

  it("marks a run from a one-shot acquisition", () => {
    const manual = groupEvents(download("r-1", 10, "one_shot").map(record))[0]!;
    const auto = groupEvents(download("r-2", 10).map(record))[0]!;
    if (manual.type !== "run" || auto.type !== "run") throw new Error("expected runs");
    expect(runView(manual.steps).oneShot).toBe(true);
    expect(runView(auto.steps).oneShot).toBe(false);
  });

  it("a download cancelled before import says why", () => {
    const cancelled = (release: string, message: BusinessEntry["message_code"]) =>
      groupEvents([
        entry({ release_id: release, kind: "download_changed", outcome: "info", message_code: message, occurred_at: at(30) }),
        entry({ release_id: release, kind: "acquisition_settled", outcome: "info", message_code: "acquisition_cancelled", occurred_at: at(31) }),
      ].map(record))[0]!;
    const group = cancelled("r-5", "download_group_not_allowed");
    const user = cancelled("r-6", "download_cancelled");
    const removed = cancelled("r-7", "download_removed");
    if (group.type !== "run" || user.type !== "run" || removed.type !== "run") throw new Error("expected runs");
    expect(runView(group.steps).result).toBe("已取消：字幕组未选中");
    expect(runView(user.steps).result).toBe("已取消");
    expect(runView(removed.steps).result).toBe("已在下载器中删除");
  });

  it("a run without an acquisition result is in progress", () => {
    const [, , started] = download("r-3", 5);
    const items = groupEvents([record(started!)]);
    const run = items[0]!;
    if (run.type !== "run") throw new Error("expected a run");
    expect(runView(run.steps).state).toBe("in_progress");
  });

  it("an import waiting for configuration says so until the configuration step resumes it", () => {
    const common = { release_id: "r-4", details: { episodes: [2] } };
    const steps = [
      entry({ ...common, kind: "download_changed", outcome: "success", message_code: "download_completed", occurred_at: at(1) }),
      entry({ ...common, kind: "artifact_verified", outcome: "success", message_code: "artifact_verified", occurred_at: at(2) }),
      entry({
        ...common,
        kind: "import_waiting_config",
        message_code: "import_waiting_template_variable",
        message_params: { variables: "vars.a,vars.b", count: 2 },
        occurred_at: at(2),
      }),
    ];
    const waiting = groupEvents(steps.map(record))[0]!;
    if (waiting.type !== "run") throw new Error("expected a run");
    // Same second as the verification: the configuration step still sorts after it.
    expect(waiting.steps.map((step) => step.kind)).toEqual(["download_changed", "artifact_verified", "import_waiting_config"]);
    expect(runView(waiting.steps).result).toBe("入库等待配置");
    expect(stepText(waiting.steps[2]!)).toBe("入库等待配置：模板变量未定义：vars.a、vars.b（2 个文件）");

    const resumed = entry({ ...common, kind: "import_waiting_config", message_code: "import_config_resumed", occurred_at: at(3) });
    const continued = groupEvents([...steps, resumed].map(record))[0]!;
    if (continued.type !== "run") throw new Error("expected a run");
    expect(runView(continued.steps).result).toBe("入库中");
    expect(stepText(resumed)).toBe("配置已补全，继续入库");
  });
});

describe("dedupeReplacements", () => {
  it("keeps the newest replacement decision per subject", () => {
    const older = entry({ kind: "replacement_decided", message_code: "replacement_kept_all", subject_id: "s1", occurred_at: at(1) });
    const newer = entry({ kind: "replacement_decided", message_code: "replacement_kept_all", subject_id: "s1", occurred_at: at(2) });
    const other = entry({ kind: "replacement_decided", message_code: "replacement_kept_all", subject_id: "s2", occurred_at: at(1) });
    const kept = dedupeReplacements([newer, older, other].map(record));
    expect(kept.map((item) => (item.record_type === "business" ? item.entry.id : ""))).toEqual([newer.id, other.id]);
  });
});

describe("覆盖与替换的身份", () => {
  it("a displaced file names what took its place", () => {
    const displaced = entry({
      kind: "placement_changed",
      message_code: "placement_displaced",
      details: {
        file_name: "EP01.mkv",
        cause: "overwrite",
        replaced_by_file: "EP01 [new].mkv",
        replaced_by_group: "GroupNew",
        replaced_by_title: "[GroupNew] Show - 01 [1080p]",
      },
    });
    expect(stepText(displaced)).toBe(
      "原文件已被覆盖 · EP01.mkv · 同一位置被新文件覆盖 · 被 EP01 [new].mkv（GroupNew · [GroupNew] Show - 01 [1080p]） 顶替",
    );
  });

  it("names a redownload as the cause", () => {
    const displaced = entry({
      kind: "placement_changed",
      message_code: "placement_displaced",
      details: { file_name: "EP02 [old].mkv", cause: "redownload", replaced_by_file: "EP02.mkv" },
    });
    expect(stepText(displaced)).toBe("原文件已被覆盖 · EP02 [old].mkv · 被重新下载的文件替换 · 被 EP02.mkv 顶替");
  });

  it("a replacement names the version it replaced, with its subgroup and title", () => {
    const replaced = entry({
      kind: "replacement_decided",
      message_code: "replacement_replaced",
      subject_id: "s1",
      details: {
        replaced_files: ["EP03.mkv"],
        replaced_targets: [{ file_name: "EP03.mkv", group_name: "OldGroup", release_title: "[OldGroup] Show - 03 [720p]" }],
      },
    });
    expect(stepText(replaced)).toBe("替换版本：已替换旧版本（EP03.mkv（OldGroup · [OldGroup] Show - 03 [720p]））");
  });

  it("falls back to the plain file name for entries written before the identity existed", () => {
    const replaced = entry({
      kind: "replacement_decided",
      message_code: "replacement_replaced",
      subject_id: "s2",
      details: { replaced_files: ["EP04.mkv"] },
    });
    expect(stepText(replaced)).toBe("替换版本：已替换旧版本（EP04.mkv）");
  });
});

describe("discoveryOutcomeText", () => {
  it("renders the feed outcome split of a discovery batch", () => {
    const line = entry({
      kind: "rss_new_content",
      message_code: "rss_new_content",
      message_params: { count: 3 },
      details: {
        item_count: 3,
        outcome_counts: { accepted_pending_download: 2, filtered: 1 },
        filtered_reasons: { release_not_in_matched: 1 },
      },
    });
    expect(entryView(line, undefined).meta).toBe(
      "待评价 2、被过滤 1（标题包含排除词 1）",
    );
  });

  it("keeps the single-line form for records written before the counts", () => {
    const line = entry({ kind: "rss_new_content", message_code: "rss_new_content", message_params: { count: 2 }, details: { item_count: 2 } });
    expect(entryView(line, undefined).meta).toBeUndefined();
  });

  it("says how many window releases keep their verdict", () => {
    const line = entry({
      kind: "rss_new_content",
      message_code: "rss_new_content",
      message_params: { count: 3 },
      details: { item_count: 3, window_total: 30, outcome_counts: { accepted_pending_download: 3 } },
    });
    expect(entryView(line, undefined).meta).toBe("待评价 3、其余 27 个种子结局不变");
  });
});

describe("release evaluation batches", () => {
  it("a batch head is its own row and never joins the adjacent-signature groups", () => {
    const head = entry({ kind: "release_batch_processed", message_code: "release_batch_first", message_params: { count: 4 }, subject_id: "bk-1", occurred_at: at(11) });
    const singles = [entry({ occurred_at: at(10) }), entry({ occurred_at: at(9) })];
    const items = groupEvents([...singles.map(record), record(head)]);
    expect(items.map((item) => item.type)).toEqual(["batch", "group"]);
  });

  it("renders the verdict distribution in a fixed order, filtered with its reasons", () => {
    const head = entry({
      kind: "release_batch_processed",
      message_code: "release_batch_reevaluated",
      message_params: { count: 9 },
      batch_counts: {
        total: 9,
        by_verdict: { start_baseline: 3, filtered: 4, awaiting_competition: 1, submitted: 1 },
        by_reason: { release_not_in_matched: 2, release_must_contain_not_matched: 2 },
      },
    });
    expect(batchCountsText(head)).toBe(
      "订阅前发布 3 · 被过滤 4（标题包含排除词 2、标题缺少必需词 2） · 等待比较 1 · 已提交下载 1",
    );
  });

  it("hides the distribution line while no detail has settled yet", () => {
    const head = entry({ kind: "release_batch_processed", message_code: "release_batch_first", message_params: { count: 3 }, batch_counts: { total: 0, by_verdict: {}, by_reason: {} } });
    expect(batchCountsText(head)).toBeUndefined();
    expect(entryView(head, undefined).meta).toBeUndefined();
  });
});
