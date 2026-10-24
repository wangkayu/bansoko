import { describe, expect, it } from "vitest";
import type { RetentionPolicyDefaultView } from "../../lib/api/client";
import {
  draftFromView,
  draftsEqual,
  graceDaysLabel,
  inputFromDraft,
  validateDraft,
} from "./retentionDraft";
import { t } from "../../lib/i18n";

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
  cleanup_policy: { version: "cleanup-v1", effect: "quarantine", grace_seconds: 604800 },
  version: 5,
};

describe("retention default draft", () => {
  it("round-trips an untouched draft without changing what the server stored", () => {
    const draft = draftFromView(view);
    expect(draftsEqual(draft, draftFromView(view))).toBe(true);
    expect(inputFromDraft(view, draft)).toEqual({
      admission_mode: "per_group",
      quality_profile: view.quality_profile,
      cleanup_policy: view.cleanup_policy,
      expected_version: 5,
    });
  });

  it("keeps fields the page does not own when saving an edit", () => {
    const draft = { ...draftFromView(view), admission_mode: "single_copy" as const, cleanup_effect: "unlink" as const, grace_seconds: "120" };
    const input = inputFromDraft(view, draft);
    expect(input.admission_mode).toBe("single_copy");
    expect(input.quality_profile).toEqual(view.quality_profile);
    expect(input).not.toHaveProperty("cutoff");
    expect(input.cleanup_policy).toEqual({ version: "cleanup-v1", effect: "unlink", grace_seconds: 120 });
    expect(input.expected_version).toBe(5);
  });

  it("is dirty when the comparison profile changes and never mutates the loaded view", () => {
    const draft = draftFromView(view);
    draft.quality_profile.items[1].ranks.reverse();
    expect(draftsEqual(draft, draftFromView(view))).toBe(false);
    expect(view.quality_profile.items[1].ranks[0]).toEqual(["CHS", "CHT"]);
    expect(inputFromDraft(view, draft).quality_profile.items[1].ranks).toEqual([["CHS"], ["CHS", "CHT"]]);
  });

  it("rejects a grace period that is not a whole non-negative number of seconds", () => {
    const draft = draftFromView(view);
    for (const bad of ["", "-1", "1.5", "abc", "1e3"]) {
      expect(validateDraft({ ...draft, grace_seconds: bad })).toBe(t("settings.retention.graceInvalid"));
    }
    expect(validateDraft({ ...draft, grace_seconds: "0" })).toBeUndefined();
  });

  it("describes the grace period in days", () => {
    expect(graceDaysLabel(604800)).toBe(t("settings.retention.graceDays", { n: 7 }));
    expect(graceDaysLabel(0)).toBe(t("settings.retention.graceNone"));
    expect(graceDaysLabel(undefined)).toBe("");
  });
});
