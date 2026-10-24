import { describe, expect, it } from "vitest";
import type { ObservabilityRecord } from "../lib/api/client";
import { t } from "../lib/i18n";
import { formatBytes } from "../lib/labels";
import { groupEvents, policyChangeText, runView, stepText, type BusinessEntry } from "./logRuns";

let seq = 0;
function entry(overrides: Partial<BusinessEntry>): BusinessEntry {
  seq += 1;
  return {
    id: `e${seq}`,
    subscription_id: "sub-1",
    managed_anime_id: "anime-1",
    kind: "subscription_created",
    outcome: "info",
    occurred_at: "2026-10-21T08:40:00Z",
    title: "侦探已经死了。 第二季",
    summary: "摘要",
    dedupe_key: `d${seq}`,
    retention_until: "2026-11-21T08:40:00Z",
    ...overrides,
  };
}

const record = (value: BusinessEntry): ObservabilityRecord => ({ record_type: "business", entry: value });
const at = (minute: number, second = 0) => `2026-10-21T08:${String(minute).padStart(2, "0")}:${String(second).padStart(2, "0")}Z`;
const context = { group_name: "NEST", episodes: [2], release_title: "[NEST] 侦探已经死了。 第二季 - 02 [1080p].mkv" };

/** One download of release r-2, newest first like the API returns it. */
function download(release: string, minute: number, settled: "success" | "failure" = "success") {
  return [
    entry({ kind: "acquisition_settled", outcome: settled, release_id: release, occurred_at: at(minute, 50), summary: settled === "success" ? "获取完成" : "获取失败", reason_code: settled === "success" ? "acquisition_succeeded" : "qb_sync_failed", details: { ...context, item_count: 1 } }),
    entry({ kind: "replacement_decided", release_id: release, occurred_at: at(minute, 40), summary: "替换版本：保留所有版本", reason_code: "queue", details: { ...context, version_count: 3 } }),
    entry({ kind: "placement_changed", outcome: "success", release_id: release, occurred_at: at(minute, 30), summary: "已记录入库位置", reason_code: "admitted", details: { ...context, file_name: "S02E02.mkv", library_folder: "侦探/Season 2" } }),
    entry({ kind: "file_alignment_completed", outcome: "success", release_id: release, occurred_at: at(minute, 20), summary: "文件已入库：S02E02.mkv", reason_code: "file_alignment_completed", details: { ...context, file_name: "S02E02.mkv", library_folder: "侦探/Season 2", size_bytes: 1_200_000_000 } }),
    entry({ kind: "artifact_verified", outcome: "success", release_id: release, occurred_at: at(minute, 10), summary: "媒体文件核验完成", reason_code: "verified", details: { ...context, item_count: 1, size_bytes: 1_200_000_000 } }),
    entry({ kind: "download_changed", outcome: "success", release_id: release, occurred_at: at(minute, 0), summary: "下载完成", reason_code: "qb_sync_succeeded", details: { ...context, size_bytes: 1_200_000_000 } }),
  ];
}

const resultText = (value: BusinessEntry) => value.summary;

describe("groupEvents", () => {
  it("一次下载的事件合成一行，位于最新一步的位置，步骤按时间升序", () => {
    const settings = entry({ kind: "policy_changed", occurred_at: at(42), summary: "规则：命名", details: { changed_sections: ["naming_rules"] } });
    const steps = download("r-2", 40);
    const items = groupEvents([settings, ...steps].map(record), resultText);
    expect(items.map((item) => item.type)).toEqual(["record", "run"]);
    const run = items[1]!;
    if (run.type !== "run") throw new Error("expected a run");
    expect(run.steps.map((step) => step.kind)).toEqual([
      "download_changed",
      "artifact_verified",
      "file_alignment_completed",
      "placement_changed",
      "replacement_decided",
      "acquisition_settled",
    ]);
  });

  it("两个种子交错的事件各自成行；同一种子重新获取得到第二行", () => {
    const a = download("r-a", 40);
    const b = download("r-b", 40).map((step) => ({ ...step, occurred_at: step.occurred_at.replace(":40:", ":40:").replace(/:(\d\d)Z$/, (_, s) => `:${String(Number(s) + 5).padStart(2, "0")}Z`) }));
    const interleaved = [...a, ...b].sort((x, y) => Date.parse(y.occurred_at) - Date.parse(x.occurred_at));
    const retry = download("r-a", 50);
    const firstAttempt = download("r-a", 30, "failure");
    const items = groupEvents([...retry, ...interleaved, ...firstAttempt].map(record), resultText);
    expect(items.map((item) => item.type)).toEqual(["run", "run", "run", "run"]);
    const runs = items.flatMap((item) => (item.type === "run" ? [item.steps] : []));
    expect(runs.map((steps) => steps[0]!.release_id)).toEqual(["r-a", "r-b", "r-a", "r-a"]);
    expect(runs.every((steps) => steps.length === 6)).toBe(true);
    expect(runView(runs[3]!).outcome).toBe("failure");
  });

  it("只有入库位置或替换版本的序列不算一次下载；没有 release_id 的旧记录照常单行", () => {
    const decision = entry({ kind: "replacement_decided", release_id: "r-9", occurred_at: at(41), summary: "替换版本：保留现有文件", reason_code: "keep" });
    const legacy = entry({ kind: "placement_changed", outcome: "success", release_id: null, occurred_at: at(40), summary: "已记录入库位置" });
    const items = groupEvents([decision, legacy].map(record), resultText);
    expect(items.map((item) => item.type)).toEqual(["record", "record"]);
  });

  it("相邻且结果相同的单条仍折叠成组", () => {
    const one = entry({ kind: "metadata_applied", outcome: "success", occurred_at: at(41), summary: "已关联" });
    const two = entry({ kind: "metadata_applied", outcome: "success", occurred_at: at(40), summary: "已关联" });
    const items = groupEvents([one, two].map(record), resultText);
    expect(items).toHaveLength(1);
    expect(items[0]!.type).toBe("group");
  });

  it("跨页：加载更早的一页后，同一次下载就地合并，不出现两次", () => {
    const steps = download("r-2", 40);
    const firstPage = steps.slice(0, 2);
    expect(groupEvents(firstPage.map(record), resultText).filter((item) => item.type === "run")).toHaveLength(1);
    const both = groupEvents(steps.map(record), resultText);
    expect(both).toHaveLength(1);
    expect(both[0]!.type === "run" && both[0]!.steps).toHaveLength(6);
  });
});

describe("runView", () => {
  it("标题取字幕组与集数，结果取获取结果", () => {
    const steps = [...download("r-2", 40)].reverse();
    const view = runView(steps);
    expect(view.groupName).toBe("NEST");
    expect(view.episodes).toBe(t("records.episode", { n: 2 }));
    expect(view.result).toBe(t("log.run.result.imported"));
    expect(view.outcome).toBe("success");
  });

  it("没有获取结果时按最新一步给出进行中的结果", () => {
    const submitted = entry({ kind: "download_changed", release_id: "r-1", reason_code: "submitted", summary: "已提交下载" });
    expect(runView([submitted]).result).toBe(t("log.run.result.downloading"));
    const failed = entry({ kind: "download_changed", outcome: "failure", release_id: "r-1", reason_code: "qb_sync_failed", summary: "下载失败" });
    const view = runView([submitted, failed]);
    expect(view.result).toBe(t("log.run.result.downloadFailed"));
    expect(view.outcome).toBe("failure");
    expect(view.decisive).toBe(failed);
  });

  it("在下载器中删除的获取是已删除，不是失败", () => {
    const removed = entry({ kind: "download_changed", release_id: "r-1", reason_code: "downloader_task_removed", summary: "已在下载器中删除" });
    const settled = entry({ kind: "acquisition_settled", release_id: "r-1", reason_code: "downloader_task_removed", summary: "获取已取消" });
    expect(runView([removed, settled]).result).toBe(t("log.run.result.removed"));
  });
});

describe("stepText", () => {
  const [settled, decision, placement, file, verified, downloaded] = download("r-2", 40);

  it("每一步写具体内容：文件名、位置、大小、替换结果", () => {
    const size = formatBytes(1_200_000_000);
    expect(stepText(downloaded!)).toBe(`${t("log.step.downloaded")} · ${size}`);
    expect(stepText(verified!)).toBe(`${t("log.step.verified")} · ${t("log.step.fileCount", { n: 1 })} · ${size}`);
    expect(stepText(file!)).toBe(`${t("log.step.imported", { name: "S02E02.mkv" })} · 侦探/Season 2 · ${size}`);
    expect(stepText(placement!)).toBe(`${t("log.step.placement")} · 侦探/Season 2/S02E02.mkv`);
    expect(stepText(decision!)).toBe(t("log.step.versions", { n: 3 }));
    expect(stepText(settled!)).toBe(`${t("log.step.settled")} · ${t("log.step.importedCount", { n: 1 })}`);
  });

  it("替换版本写替换了哪个文件或保留了哪个版本", () => {
    expect(stepText(entry({ kind: "replacement_decided", reason_code: "replace", details: { replaced_files: ["old.mkv"] } }))).toBe(
      t("log.step.replaced", { names: "old.mkv" }),
    );
    expect(stepText(entry({ kind: "replacement_decided", reason_code: "retain", details: { kept_file: "old.mkv" } }))).toBe(
      t("log.step.kept", { name: "old.mkv" }),
    );
  });

  it("旧记录没有新事实时退回摘要", () => {
    expect(stepText(entry({ kind: "artifact_verified", summary: "媒体文件核验完成" }))).toBe("媒体文件核验完成");
    expect(stepText(entry({ kind: "file_alignment_completed", summary: "文件已入库：a.mkv" }))).toBe("文件已入库：a.mkv");
    expect(stepText(entry({ kind: "replacement_decided", summary: "替换版本：保留所有版本" }))).toBe("替换版本：保留所有版本");
  });
});

describe("policyChangeText", () => {
  it("规则变化列出改动的分段；旧记录没有分段时不覆盖摘要", () => {
    expect(policyChangeText(entry({ kind: "policy_changed", details: { changed_sections: ["release_rules", "naming_rules"] } }))).toBe(
      t("log.rulesChanged", { sections: `${t("tabs.sectionName.release_rules")}、${t("tabs.sectionName.naming_rules")}` }),
    );
    expect(policyChangeText(entry({ kind: "policy_changed", summary: "规则策略已更新" }))).toBeUndefined();
    expect(policyChangeText(entry({ kind: "group_changed" }))).toBeUndefined();
  });
});
