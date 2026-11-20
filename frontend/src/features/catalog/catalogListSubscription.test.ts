import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/api/client";
import { CandidateVersionConflict, CandidateWorkingDuplicate, type AnimeConfigCandidate, type CandidateStore } from "../../lib/candidate/port";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { withGlobalBusiness } from "../anime-config/businessDraft";
import { draftFromEntry } from "./entryDraft";
import { createCatalogListSubscriber } from "./catalogListSubscription";

vi.mock("../anime-config/businessDraft", async (importOriginal) => ({
  ...await importOriginal<typeof import("../anime-config/businessDraft")>(),
  withGlobalBusiness: vi.fn((draft) => Promise.resolve({ ...draft, ignore_multi_video_releases: true })),
}));

const entry: CatalogEntryView = {
  source: "mikan", providerEntryRef: "mikan:fixture", title: "Sky Notes",
  season: "2026-fall", metadataMatch: { matched: false },
};

function candidateFor(row = entry): AnimeConfigCandidate {
  const draft = draftFromEntry(row);
  return {
    id: row.candidateId ?? "candidate", status: "working", version: 5, current_revision: 3,
    origin: draft.origin, origin_entry_ref: draft.origin_entry_ref ?? null, title: draft.title, draft,
    title_override: null, instance_label: draft.instance_label ?? null, activated_managed_anime_id: null,
    current_revision_digest: "sha256:" + "1".repeat(64), retention_until: "2026-12-20T00:00:00Z",
    cleanup_eligible_at: null, cleanup_reason: null,
    draft_digest: "", created_at: "", updated_at: "", last_activity: "",
  };
}

function fixture() {
  vi.mocked(withGlobalBusiness).mockClear();
  const candidate = candidateFor();
  const store = {
    list: vi.fn<CandidateStore["list"]>().mockResolvedValue([candidate]),
    get: vi.fn<CandidateStore["get"]>(),
    save: vi.fn<CandidateStore["save"]>().mockResolvedValue(candidate),
    patch: vi.fn<CandidateStore["patch"]>(),
    subscribe: vi.fn<CandidateStore["subscribe"]>(),
    delete: vi.fn<CandidateStore["delete"]>(),
  };
  return { candidate, store, subscribe: createCatalogListSubscriber(store) };
}

describe("createCatalogListSubscriber", () => {
  it("uses a saved site draft without changing it or loading new defaults", async () => {
    const { store, subscribe } = fixture();
    await subscribe(entry, false);
    expect(store.list).toHaveBeenCalledWith({ origin: "mikan", origin_entry_ref: "mikan:fixture" });
    expect(withGlobalBusiness).not.toHaveBeenCalled();
    expect(store.save).not.toHaveBeenCalled();
    expect(store.subscribe).toHaveBeenCalledWith("candidate", 5, expect.any(String), { revision: 3, start: false });
  });

  it("loads current defaults and preserves source season for a new draft", async () => {
    const { store, subscribe } = fixture();
    store.list.mockResolvedValueOnce([]);
    await subscribe(entry, true);
    expect(store.save).toHaveBeenCalledWith(expect.objectContaining({ entry_season: "2026-fall", ignore_multi_video_releases: true }));
    expect(store.subscribe).toHaveBeenCalledWith("candidate", 5, expect.any(String), { revision: 3, start: true });
  });

  it("reads the exact custom preset by candidate ID", async () => {
    const { store, subscribe } = fixture();
    const custom = { ...entry, source: "custom" as const, candidateId: "preset", instanceLabel: "Local",
      rssUrl: "https://example.test/feed", groupName: "Local Group" };
    store.get.mockResolvedValue(candidateFor(custom));
    await subscribe(custom, false);
    expect(store.get).toHaveBeenCalledWith("preset");
    expect(store.list).not.toHaveBeenCalled();
    expect(store.save).not.toHaveBeenCalled();
    expect(store.subscribe).toHaveBeenCalledWith("preset", 5, expect.any(String), { revision: 3, start: false });
  });

  it("reuses the same request after an uncertain result but rereads after a version conflict", async () => {
    const { candidate, store, subscribe } = fixture();
    store.list.mockResolvedValueOnce([]).mockResolvedValueOnce([candidate]).mockResolvedValueOnce([{ ...candidate, version: 6, current_revision: 4 }]);
    store.save.mockRejectedValueOnce(new CandidateWorkingDuplicate());
    store.subscribe.mockRejectedValueOnce(new ApiError({ status: 0, message: "network" })).mockRejectedValueOnce(new CandidateVersionConflict(6));
    await expect(subscribe(entry, false)).rejects.toBeInstanceOf(ApiError);
    await expect(subscribe(entry, false)).rejects.toBeInstanceOf(CandidateVersionConflict);
    await subscribe(entry, false);
    const [first, replay, updated] = store.subscribe.mock.calls;
    expect(replay).toEqual(first);
    expect(updated).toEqual(["candidate", 6, expect.any(String), { revision: 4, start: false }]);
    expect(updated[2]).not.toBe(first[2]);
  });
});
