import { describe, expect, it } from "vitest";
import type { ObservabilityRecord } from "../lib/api/client";
import { dedupeReplacements, groupEvents, runView, type BusinessEntry } from "./logRuns";

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

  it("a run without an acquisition result is in progress", () => {
    const [, , started] = download("r-3", 5);
    const items = groupEvents([record(started!)]);
    const run = items[0]!;
    if (run.type !== "run") throw new Error("expected a run");
    expect(runView(run.steps).state).toBe("in_progress");
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
