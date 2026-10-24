import { describe, it, expect } from "vitest";
import { assertReadOnlySnapshots, type DisposableSnapshot } from "../e2e/disposable-db-snapshot";

const baseline: DisposableSnapshot = {
  database: "bansoko_harness_test", read_only: "on", isolation: "repeatable read", target: null,
  owners: [{ type: "release", id: "r1", state: "submitted", version: 1, updated_at: "fixed", fence_epoch: null }, { type: "lifecycle", id: "a1", state: "active", version: 1, updated_at: "fixed", fence_epoch: 1 }],
  work_items: [{ id: "w1", owner_type: "release", owner_id: "r1", kind: "release_evaluate", state: "succeeded", fencing_token: 1, attempt: 1, updated_at: "fixed", managed_anime_id: "a1", owner_lifecycle_epoch: 1, external_effect_admitted_at: null }],
  candidates: ["c1", "c2"].map((id) => ({ id, version: 1, updated_at: "fixed", status: "working", activated_managed_anime_id: null, current_revision: 1, current_revision_digest: "digest", draft_digest: "draft" })),
  candidate_revisions: ["c1", "c2"].map((candidate_id) => ({ candidate_id, revision: 1, draft_digest: "draft", effective_digest: "digest", parser_policy_digest: "parser", created_at: "fixed" })),
  scoped_owners: null, scoped_work_items: null,
};

describe("disposable DB keyed comparison (offline)", () => {
  it("accepts identical observations", () => assertReadOnlySnapshots(baseline, structuredClone(baseline)));
  it.each(["owners", "work_items"] as const)("rejects same-count %s PK replacement", (field) => {
    const after = structuredClone(baseline); after[field][0].id = "replacement";
    expect(() => assertReadOnlySnapshots(baseline, after)).toThrow();
  });
  it("rejects state, version and WorkItem fence changes", () => {
    for (const change of [(a: DisposableSnapshot) => { a.owners[0].state = "failed"; }, (a: DisposableSnapshot) => { a.owners[0].version++; }, (a: DisposableSnapshot) => { a.work_items[0].fencing_token++; }]) {
      const after = structuredClone(baseline); change(after);
      expect(() => assertReadOnlySnapshots(baseline, after)).toThrow();
    }
  });
  it("permits only named columns of an existing candidate", () => {
    const after = structuredClone(baseline); after.candidates[0].version++;
    assertReadOnlySnapshots(baseline, after, { id: "c1", columns: ["version"] });
    after.candidates[0].updated_at = "changed";
    expect(() => assertReadOnlySnapshots(baseline, after, { id: "c1", columns: ["version"] })).toThrow();
  });
  it("rejects unrelated candidate and nonexistent allowed target", () => {
    const after = structuredClone(baseline); after.candidates[1].version++;
    expect(() => assertReadOnlySnapshots(baseline, after, { id: "c1", columns: ["version"] })).toThrow();
    expect(() => assertReadOnlySnapshots(baseline, baseline, { id: "absent", columns: ["version"] })).toThrow();
  });
  it("rejects duplicate identity and missing scoped target", () => {
    const after = structuredClone(baseline); after.owners.push(after.owners[0]);
    expect(() => assertReadOnlySnapshots(baseline, after)).toThrow();
    const scoped = { ...baseline, target: { managed_id: "a1", source_id: "s1", release_id: "r1" }, scoped_owners: baseline.owners, scoped_work_items: baseline.work_items };
    expect(() => assertReadOnlySnapshots(scoped, { ...scoped, target: null })).toThrow();
  });
  it("rejects lifecycle-only fencing and WorkItem lifecycle/admission changes", () => {
    for (const change of [(a: DisposableSnapshot) => { a.owners[1].fence_epoch = 2; }, (a: DisposableSnapshot) => { a.work_items[0].owner_lifecycle_epoch = 2; }, (a: DisposableSnapshot) => { a.work_items[0].external_effect_admitted_at = "changed"; }]) {
      const after = structuredClone(baseline); change(after);
      expect(() => assertReadOnlySnapshots(baseline, after)).toThrow();
    }
  });
  it("rejects revision insertion, deletion, same-count replacement and digest rewrite", () => {
    for (const change of [(a: DisposableSnapshot) => { a.candidate_revisions.push({ ...a.candidate_revisions[0], revision: 2 }); }, (a: DisposableSnapshot) => { a.candidate_revisions.pop(); }, (a: DisposableSnapshot) => { a.candidate_revisions[0].revision = 2; }, (a: DisposableSnapshot) => { a.candidate_revisions[0].effective_digest = "rewritten"; }]) {
      const after = structuredClone(baseline); change(after);
      expect(() => assertReadOnlySnapshots(baseline, after)).toThrow();
    }
  });
  it("rejects candidate pointer/status changes even with a version allowance", () => {
    for (const change of [(a: DisposableSnapshot) => { a.candidates[0].current_revision = 2; }, (a: DisposableSnapshot) => { a.candidates[0].status = "archived"; }, (a: DisposableSnapshot) => { a.candidates[0].draft_digest = "changed"; }]) {
      const after = structuredClone(baseline); change(after);
      expect(() => assertReadOnlySnapshots(baseline, after, { id: "c1", columns: ["version", "updated_at"] })).toThrow();
    }
  });
});
