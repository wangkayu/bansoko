import { describe, expect, it } from "vitest";
import { animeOwnerCapability, editableCapability, workspaceCapability, type ManagedAnimeLifecycleRef, type WorkspaceMutation } from "./capability";

// capability is the single authority for the workspace's read-only gate: this locks down which
//   mutations read-only turns off, and every surface (tabs / RSS actions / card entries) consumes
//   only this one verdict.
const MUTATIONS: WorkspaceMutation[] = [
  "metadata.edit",
  "subgroups.edit",
  "rules.edit",
  "rules.preview",
  "parser_policy.save",
  "file_effect.save",
  "rss.refresh",
  "targets.scan",
  "owner.retry",
  "release.retry",
  "rss.offset",
];

describe("workspaceCapability", () => {
  it("active（或未知）生命周期允许全部 mutation", () => {
    for (const state of ["active", undefined] as const) {
      const capability = workspaceCapability(state);
      expect(capability.readOnly).toBe(false);
      expect(capability.lifecycleState).toBe("active");
      for (const mutation of MUTATIONS) {
        expect(capability.can(mutation)).toBe(true);
      }
    }
  });

  it("retired 生命周期是只读：全部 mutation 关闭", () => {
    const capability = workspaceCapability("retired");
    expect(capability.readOnly).toBe(true);
    expect(capability.lifecycleState).toBe("retired");
    for (const mutation of MUTATIONS) {
      expect(capability.can(mutation)).toBe(false);
    }
  });

  it("editableCapability 用于没有生命周期 owner 的 draft 面", () => {
    const capability = editableCapability();
    expect(capability.readOnly).toBe(false);
    expect(capability.can("rules.edit")).toBe(true);
  });
});

// Logs (with no workspace read model) can only look up the lifecycle by taking the release
//   record's managed_anime_id back through listAnime: without an owner that is explicitly active
//   the answer must fail closed.
describe("animeOwnerCapability", () => {
  const active: ManagedAnimeLifecycleRef = {
    managed_anime: { id: "anime-1" },
    subscription: { lifecycle_state: "active" },
  };
  const retired: ManagedAnimeLifecycleRef = {
    managed_anime: { id: "anime-1" },
    subscription: { lifecycle_state: "retired" },
  };

  it("明确 active 的 owner 返回可写 capability", () => {
    const capability = animeOwnerCapability([active], "anime-1");
    expect(capability?.readOnly).toBe(false);
    expect(capability?.can("owner.retry")).toBe(true);
  });

  it("retired owner 是只读", () => {
    const capability = animeOwnerCapability([retired], "anime-1");
    expect(capability?.readOnly).toBe(true);
    expect(capability?.can("owner.retry")).toBe(false);
  });

  it("查不到 owner、owner id 缺失或列表未加载都返回 undefined（fail closed）", () => {
    expect(animeOwnerCapability([active], "anime-2")).toBeUndefined();
    expect(animeOwnerCapability([active], "")).toBeUndefined();
    expect(animeOwnerCapability([active], undefined)).toBeUndefined();
    expect(animeOwnerCapability(undefined, "anime-1")).toBeUndefined();
    expect(animeOwnerCapability([], "anime-1")).toBeUndefined();
  });

  it("生命周期字段缺失时不当成 active（fail closed）", () => {
    const unknown: ManagedAnimeLifecycleRef = { managed_anime: { id: "anime-1" }, subscription: {} };
    const capability = animeOwnerCapability([unknown], "anime-1");
    expect(capability?.readOnly).toBe(true);
  });
});
