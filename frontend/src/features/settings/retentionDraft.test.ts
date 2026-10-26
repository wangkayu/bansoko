import { describe, expect, it } from "vitest";
import type { RetentionPolicyDefaultView } from "../../lib/api/client";
import { draftFromView, draftsEqual, inputFromDraft } from "./retentionDraft";

const view: RetentionPolicyDefaultView = {
  admission_mode: "per_group",
  quality_profile: {
    version: "profile-v1",
    items: [
      { key: "subgroup.score", ranks: [] },
      { key: "anibt.language", ranks: [["CHS", "CHT"], ["CHS"]] },
      { key: "release.resolution", ranks: [["1080p"], ["720p"]] },
    ],
  },
  version: 5,
};

describe("retention default draft", () => {
  it("round-trips an untouched draft without changing what the server stored", () => {
    const draft = draftFromView(view);
    expect(draftsEqual(draft, draftFromView(view))).toBe(true);
    expect(inputFromDraft(view, draft)).toEqual({
      admission_mode: "per_group",
      quality_profile: view.quality_profile,
      expected_version: 5,
    });
  });

  it("saves an admission edit without a cutoff or cleanup policy", () => {
    const draft = { ...draftFromView(view), admission_mode: "single_copy" as const };
    const input = inputFromDraft(view, draft);
    expect(input.admission_mode).toBe("single_copy");
    expect(input.quality_profile).toEqual(view.quality_profile);
    expect(input).not.toHaveProperty("cutoff");
    expect(input).not.toHaveProperty("cleanup_policy");
    expect(input.expected_version).toBe(5);
  });

  it("is dirty when the comparison profile changes and never mutates the loaded view", () => {
    const draft = draftFromView(view);
    draft.quality_profile.items[1].ranks.reverse();
    expect(draftsEqual(draft, draftFromView(view))).toBe(false);
    expect(view.quality_profile.items[1].ranks[0]).toEqual(["CHS", "CHT"]);
    expect(inputFromDraft(view, draft).quality_profile.items[1].ranks).toEqual([["CHS"], ["CHS", "CHT"]]);
  });
});
