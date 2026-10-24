import { describe, expect, it } from "vitest";
import {
  artifactStateLabel,
  downloadTaskPill,
  downloadTaskStateLabel,
  isDownloaderRemoved,
  fileOperationStateLabel,
  metadataBlocking,
  releaseVerdictLabelWithHold,
  metadataMissingLabel,
  notificationTriggerKindLabel,
  evaluationReasonCode,
  formatTimeAgo,
  releaseVerdictLabel,
  routineKindLabel,
  sourceHealthLabel,
  subscriptionStateLabel,
} from "./labels";
import { t } from "./i18n";

// Regression guard for the product-language table: any drift in state words should fail here.
describe("FR-10 产品语言表", () => {
  it("Subscription", () => {
    expect(subscriptionStateLabel("active")).toBe("订阅中");
    expect(subscriptionStateLabel("stopped")).toBe("已停止");
  });

  it("Release verdict 来自文案表", () => {
    for (const verdict of ["discovered", "waiting_metadata", "subscription_stopped", "source_unavailable", "parse_failed", "accepted_pending_download", "filtered", "adopted_existing", "acquisition_failed", "submitted"] as const) {
      expect(releaseVerdictLabel(verdict)).toBe(t(`verdict.${verdict}`));
    }
    expect(releaseVerdictLabel("filtered", "原因")).toBe(t("verdict.filteredWithReason", { reason: "原因" }));
    expect(releaseVerdictLabel("submitted", "原因")).toBe(t("verdict.submitted"));
  });

  it("DownloadTask", () => {
    expect(downloadTaskStateLabel("queued")).toBe("提交中");
    expect(downloadTaskStateLabel("submitting")).toBe("提交中");
    expect(downloadTaskStateLabel("downloading")).toBe("下载中");
    expect(downloadTaskStateLabel("completed")).toBe("下载完成");
    expect(downloadTaskStateLabel("waiting_retry")).toBe("等待重试");
    expect(downloadTaskStateLabel("failed")).toBe("下载失败");
  });

  it("DownloadTask deleted in the download client is not a failure", () => {
    expect(downloadTaskPill({ state: "failed", failure_code: "DOWNLOADER_TASK_REMOVED" })).toEqual({ label: "已在下载器中删除", tone: "muted" });
    expect(downloadTaskPill({ state: "failed", failure_code: "SYNC_ERROR_BUDGET_EXHAUSTED" })).toEqual({ label: "下载失败", tone: "offline" });
    expect(isDownloaderRemoved({ state: "downloading", failure_code: "DOWNLOADER_TASK_REMOVED" })).toBe(false);
  });

  it("Artifact", () => {
    expect(artifactStateLabel("pending")).toBe("待校验");
    expect(artifactStateLabel("verified")).toBe("已校验");
    expect(artifactStateLabel("waiting_retry")).toBe("等待重试");
    expect(artifactStateLabel("failed")).toBe("校验失败");
  });

  it("FileOperation", () => {
    expect(fileOperationStateLabel("pending")).toBe("入库中");
    expect(fileOperationStateLabel("running")).toBe("入库中");
    expect(fileOperationStateLabel("completed")).toBe("已入库");
    expect(fileOperationStateLabel("waiting_retry")).toBe("等待重试");
    expect(fileOperationStateLabel("failed")).toBe("入库失败");
  });

  it("RSSSourceRuntime", () => {
    expect(sourceHealthLabel("unknown")).toBe("未知");
    expect(sourceHealthLabel("ready")).toBe("正常");
    expect(sourceHealthLabel("degraded")).toBe(t("status.health.degraded"));
    expect(sourceHealthLabel("failed")).toBe("失败");
  });

  it("metadata hold", () => {
    expect(metadataMissingLabel(["bgm"])).toBe(t("anime.metadata.missing", { names: "Bangumi" }));
    expect(metadataMissingLabel(["bgm", "tmdb"])).toBe(t("anime.metadata.missing", { names: "Bangumi、TMDB" }));
    const both = metadataBlocking(["tmdb", "bgm"]);
    expect(both.short).toBe("未关联 Bangumi、TMDB");
    expect(both.long).toBe("自动下载已暂停：未关联 Bangumi、TMDB");
    expect(both.seed).toBe("等待关联 Bangumi、TMDB");
    expect(both.fix).toEqual({ link: "bgm", label: "关联 Bangumi" });
    expect(metadataBlocking(["tmdb"]).fix?.label).toBe("关联 TMDB");
    // The server did not say which is missing: no name is called and no fix button appears.
    const unknown = metadataBlocking(undefined);
    expect(unknown.long).toBe("自动下载已暂停：未关联 Bangumi 或 TMDB");
    expect(unknown.fix).toBeUndefined();
    expect(releaseVerdictLabelWithHold("waiting_metadata", { held: true, missing: ["tmdb"] })).toBe("等待关联 TMDB");
    expect(releaseVerdictLabelWithHold("waiting_metadata", { held: false, missing: [] })).toBe(t("verdict.waiting_metadata"));
  });

  it("reason code 不是 verdict", () => {
    expect(evaluationReasonCode("filtered_by_rules", "release_must_contain_not_matched")).toBe("release_must_contain_not_matched");
    expect(evaluationReasonCode("filtered_by_rules", "free text")).toBe("filtered_by_rules");
    expect(evaluationReasonCode(null, null)).toBeUndefined();
  });

  // Routine kinds of execution records must go through the localized mapping, never leaking snake_case.
  it("routine kind 全部映射为中文标签", () => {
    expect(routineKindLabel("catalog_refresh")).toBe("目录刷新");
    expect(routineKindLabel("provider_refresh")).toBe("站点刷新");
    expect(routineKindLabel("rss_refresh")).toBe("RSS 刷新");
    expect(routineKindLabel("metadata_match")).toBe("元数据自动匹配");
    expect(routineKindLabel("automation_dispatch")).toBe("自动派发");
		 expect(routineKindLabel("retention_pass")).toBe("清理保留数据");
  });

  // Run-record plugin row titles go through the trigger's localized mapping, never leaking snake_case.
  it("notification trigger kind 全部映射为中文标签", () => {
    expect(notificationTriggerKindLabel("download_submitted")).toBe("下载已提交");
    expect(notificationTriggerKindLabel("file_operation_completed")).toBe("入库完成");
    expect(notificationTriggerKindLabel("file_operation_failed")).toBe("入库失败");
  });
});

describe("formatTimeAgo", () => {
  const now = Date.parse("2026-10-11T12:00:00Z");
  it("按分钟、小时、天输出相对时间，缺失或无效时省略", () => {
    expect(formatTimeAgo("2026-10-11T11:59:40Z", now)).toBe(t("common.timeAgo.justNow"));
    expect(formatTimeAgo("2026-10-11T11:15:00Z", now)).toBe(t("common.timeAgo.minutes", { n: 45 }));
    expect(formatTimeAgo("2026-10-11T07:00:00Z", now)).toBe(t("common.timeAgo.hours", { n: 5 }));
    expect(formatTimeAgo("2026-10-09T11:00:00Z", now)).toBe(t("common.timeAgo.days", { n: 2 }));
    expect(formatTimeAgo(undefined, now)).toBeUndefined();
    expect(formatTimeAgo("not-a-date", now)).toBeUndefined();
  });
});
