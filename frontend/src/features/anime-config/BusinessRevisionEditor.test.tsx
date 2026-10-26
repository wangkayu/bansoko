import { fireEvent, screen, waitFor } from "@testing-library/react";
import { renderWithQuery as render } from "../../testing/renderWithQuery";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { BusinessRevisionEditor } from "./BusinessRevisionEditor";
import type { AnimeDraft } from "../../lib/api/client";
import type { BusinessPresetView } from "../../lib/api/client";
import { webcrypto, createHash } from "node:crypto";
import { canonicalJSON } from "../../lib/draft";
import { preset } from "../../testing/businessPreset";
import { t } from "../../lib/i18n";
const mocks = vi.hoisted(() => ({ read: vi.fn(), preset: vi.fn(), apply: vi.fn(), save: vi.fn(), patch: vi.fn(), confirm: vi.fn() }));
vi.mock("../../lib/api/client", () => ({ getAnimeBusinessEdit: mocks.read, getBusinessPreset: mocks.preset, applyAnimeConfigCandidate: mocks.apply, newUUID: () => "exact-key", toUserMessage: () => "请求失败", getRetentionCompareVocabulary: async () => ({ items: [] }) }));
vi.mock("../../lib/candidate/serverCandidateStore", () => ({ getCandidateStore: () => ({ save: mocks.save, patch: mocks.patch }) }));
vi.mock("./confirmBusinessReplace", () => ({ confirmBusinessReplace: mocks.confirm }));
vi.mock("../anime-forms/RulesSectionsFields", () => ({ RulesSectionsFields: () => null }));
const draft: AnimeDraft = { origin: "custom", title: "instance", instance_label: "label", metadata: {}, subgroups: { max_rss: 3, entries: [] }, rules: { sections: {} }, rss_sources: [] };
// Base UI Select: open the combobox, then pick the option by role.
async function pick(selectName: string, optionLabel: string) {
 await userEvent.setup().click(screen.getByRole("combobox",{name:selectName}));
 await userEvent.setup().click(await screen.findByRole("option",{name:optionLabel}));
}
const versions = { rules: 2, subgroups: 3, parser: 4, retention: 5, file_effect: 6, business: 7 };
beforeEach(() => { Object.defineProperty(globalThis,"crypto",{value:webcrypto,configurable:true}); vi.clearAllMocks(); mocks.read.mockResolvedValue({ target_managed_anime_id: "target", draft, expected_owner_versions: versions }); mocks.preset.mockResolvedValue(preset); mocks.confirm.mockResolvedValue(true); mocks.apply.mockResolvedValue({}); mocks.save.mockImplementation(async (value: AnimeDraft) => ({ id: "working", version: 9, current_revision: 11, draft: value })); });
it("load does not save; save does not apply; explicit apply carries exact target and versions", async () => {
 render(<BusinessRevisionEditor animeID="target" onApplied={vi.fn()} />);
 fireEvent.click(screen.getByText(t("config.revision.edit")));
 fireEvent.click(await screen.findByText(t("config.useDefaults")));
 await screen.findByText(t("config.revision.dirty"));
 expect(mocks.save).not.toHaveBeenCalled(); expect(mocks.apply).not.toHaveBeenCalled();
 fireEvent.click(screen.getByText(t("config.revision.save")));
 await screen.findByText(t("config.revision.saved"));
 expect(mocks.apply).not.toHaveBeenCalled();
 fireEvent.click(screen.getByText(t("config.revision.apply")));
 await waitFor(() => expect(mocks.apply).toHaveBeenCalledWith("working", { target_managed_anime_id: "target", revision: 11, expected_candidate_version: 9, expected_owner_versions: versions }, "exact-key"));
});
it("cancelled dirty reload and failed preset leave draft unsaved", async () => {
 render(<BusinessRevisionEditor animeID="target" onApplied={vi.fn()} />);
 fireEvent.click(screen.getByText(t("config.revision.edit"))); fireEvent.click(await screen.findByText(t("config.useDefaults")));
 await screen.findByText(t("config.revision.dirty")); mocks.confirm.mockResolvedValue(false);
 fireEvent.click(screen.getByText(t("config.useDefaults"))); await waitFor(() => expect(mocks.confirm).toHaveBeenCalled());
 expect(mocks.preset).toHaveBeenCalledTimes(1); expect(mocks.save).not.toHaveBeenCalled();
 mocks.confirm.mockResolvedValue(true); mocks.preset.mockRejectedValue(new Error("failed"));
 fireEvent.click(screen.getByText(t("config.useDefaults"))); await screen.findByRole("alert");
 expect(screen.getByText(t("config.revision.dirty"))).toBeTruthy(); expect(mocks.apply).not.toHaveBeenCalled();
});
it("save failure preserves dirty draft and reports parent lifecycle; successful retry is not apply", async () => {
 const changed=vi.fn(); render(<BusinessRevisionEditor animeID="target" onApplied={vi.fn()} onDirtyChange={changed} />);
 fireEvent.click(screen.getByText(t("config.revision.edit"))); fireEvent.click(await screen.findByText(t("config.useDefaults")));
 await screen.findByText(t("config.revision.dirty")); expect(changed).toHaveBeenLastCalledWith(true);
 mocks.save.mockRejectedValueOnce(new Error("save failed")); fireEvent.click(screen.getByText(t("config.revision.save")));
 await screen.findByRole("alert"); expect(screen.getByText(t("config.revision.dirty"))).toBeTruthy(); expect(mocks.apply).not.toHaveBeenCalled();
 fireEvent.click(screen.getByText(t("config.revision.save"))); await screen.findByText(t("config.revision.saved"));
 expect(changed).toHaveBeenLastCalledWith(false); expect(mocks.apply).not.toHaveBeenCalled();
});
it("apply CAS conflict preserves exact saved revision and does not replace editing baseline", async () => {
 render(<BusinessRevisionEditor animeID="target" onApplied={vi.fn()} />);
 fireEvent.click(screen.getByText(t("config.revision.edit"))); fireEvent.click(await screen.findByText(t("config.useDefaults")));
 await screen.findByText(t("config.revision.dirty")); fireEvent.click(screen.getByText(t("config.revision.save"))); await screen.findByText(t("config.revision.saved"));
 mocks.apply.mockRejectedValueOnce(new Error("BUSINESS_OWNER_VERSION_CONFLICT")); fireEvent.click(screen.getByText(t("config.revision.apply")));
 await screen.findByRole("alert"); expect(screen.getByText(t("config.revision.saved"))).toBeTruthy(); expect(mocks.read).toHaveBeenCalledTimes(1); expect(mocks.save).toHaveBeenCalledTimes(1);
 expect(mocks.apply).toHaveBeenCalledWith("working",{target_managed_anime_id:"target",revision:11,expected_candidate_version:9,expected_owner_versions:versions},"exact-key");
});
it("real complete business controls freeze SHA then save and explicitly apply exact revision",async()=>{
 render(<BusinessRevisionEditor animeID="target" onApplied={vi.fn()} />);
 fireEvent.click(screen.getByText(t("config.revision.edit")));fireEvent.click(await screen.findByText(t("config.useDefaults")));await screen.findByText(t("config.revision.dirty"));
 await pick(t("retention.admissionTitle"),t("retention.admission.all"));await pick(t("file.unclassifiedTitle"),t("file.unclassified.skip"));
 fireEvent.click(screen.getByText(t("config.revision.save")));await screen.findByText(t("config.revision.saved"));
 const submitted=mocks.save.mock.calls[0][0] as AnimeDraft;const expected=structuredClone(preset.snapshot);expected.retention.admission_mode="all";expected.unclassified_route="skip";
 expect(submitted.business).toEqual(expected);expect(submitted.business_digest).toBe(`sha256:${createHash("sha256").update(canonicalJSON(expected)).digest("hex")}`);
 expect(submitted.title).toBe("instance");expect(submitted.instance_label).toBe("label");expect(submitted.metadata).toEqual(draft.metadata);expect(submitted.rss_sources).toEqual(draft.rss_sources);expect(mocks.apply).not.toHaveBeenCalled();
 fireEvent.click(screen.getByText(t("config.revision.apply")));await waitFor(()=>expect(mocks.apply).toHaveBeenCalledWith("working",{target_managed_anime_id:"target",revision:11,expected_candidate_version:9,expected_owner_versions:versions},"exact-key"));
});
it("program digests are read-only; a failed save preserves the edited draft",async()=>{
 render(<BusinessRevisionEditor animeID="target" onApplied={vi.fn()} />);fireEvent.click(screen.getByText(t("config.revision.edit")));fireEvent.click(await screen.findByText(t("config.useDefaults")));await screen.findByText(t("config.revision.dirty"));
 expect(screen.queryByLabelText("vars digest")).toBeNull();expect(screen.queryByLabelText("attrrules digest")).toBeNull();
 await pick(t("retention.admissionTitle"),t("retention.admission.all"));mocks.save.mockRejectedValueOnce(new Error("exact Vars program is not persisted"));fireEvent.click(screen.getByText(t("config.revision.save")));
 await waitFor(()=>expect(mocks.save).toHaveBeenCalledTimes(1));await screen.findByRole("alert");
 expect(screen.getByRole("combobox",{name:t("retention.admissionTitle")})).toHaveTextContent(t("retention.admission.all"));expect(screen.getByText(t("config.revision.dirty"))).toBeTruthy();expect(mocks.apply).not.toHaveBeenCalled();
});
// Hidden per the subscription's current retention and cleanup version: the anime config dialog
// passes hideRetention (the restore checklist lives with the dialog). The cases above omit it
// and keep covering the full control set.
it("hideRetention：不显示保留与清理的两项控件，隐藏的取值原样随修订保存",async()=>{
 render(<BusinessRevisionEditor animeID="target" onApplied={vi.fn()} hideRetention />);
 fireEvent.click(screen.getByText(t("config.revision.edit")));fireEvent.click(await screen.findByText(t("config.useDefaults")));await screen.findByText(t("config.revision.dirty"));
 expect(screen.queryByText(t("config.snapshot.legend"))).toBeNull();
 expect(screen.queryByRole("combobox",{name:t("retention.admissionTitle")})).toBeNull();
 expect(screen.queryByRole("group",{name:t("qualityCompare.title")})).toBeNull();

 // The parser is not chosen here: it lives in the rules tab's "parsing" section together with the episode recognition rules.
 expect(screen.queryByRole("combobox",{name:t("policy.parser.kindLabel")})).toBeNull();
 await pick(t("file.unclassifiedTitle"),t("file.unclassified.skip"));
 fireEvent.click(screen.getByText(t("config.revision.save")));await screen.findByText(t("config.revision.saved"));
 const submitted=mocks.save.mock.calls[0][0] as AnimeDraft;
 expect(submitted.business?.retention).toEqual(preset.snapshot.retention);expect(submitted.business?.retention_version).toBe(preset.snapshot.retention_version);
 expect(submitted.business?.unclassified_route).toBe("skip");
});
