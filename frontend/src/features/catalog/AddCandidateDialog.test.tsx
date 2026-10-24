import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AddCandidateDialog } from "./AddCandidateDialog";
import { t } from "../../lib/i18n";
import { renderWithQuery } from "../../testing/renderWithQuery";
import { presetRuleSections } from "../anime-config/businessDraft";

const mocks = vi.hoisted(() => ({ save: vi.fn(), error: vi.fn() }));
vi.mock("../../lib/candidate/serverCandidateStore", () => ({ getCandidateStore: () => ({ save: mocks.save }) }));
vi.mock("sonner", () => ({ toast: { error: mocks.error } }));
vi.mock("../../lib/api/client", async (importOriginal) => ({ ...(await importOriginal<typeof import("../../lib/api/client")>()), getBusinessPreset: vi.fn(async () => (await import("../../testing/businessPreset")).preset),
  // The declared site list is a static read, not a feed fetch; the feed itself is never fetched here.
  listFeedSites: vi.fn(async () => ({ sites: [] })) }));

beforeEach(() => { mocks.save.mockReset().mockResolvedValue({ id: "exact-custom-candidate", version: 1 }); mocks.error.mockReset(); vi.stubGlobal("fetch", vi.fn(() => { throw new Error("implicit network request"); })); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function fill() {
  const inputs = screen.getAllByRole("textbox");
  [" Custom Anime ", " Living Room ", " https://feed.example.test/rss ", " Exact Group "].forEach((value, i) => fireEvent.change(inputs[i], { target: { value } }));
}

describe("Custom candidate explicit save boundary", () => {
 it("opens without fetching and saves one source through the ordinary candidate chain", async () => {
  const added = vi.fn(); renderWithQuery(<AddCandidateDialog open onOpenChange={vi.fn()} onAdded={added} />);
  expect(fetch).not.toHaveBeenCalled(); expect(mocks.save).not.toHaveBeenCalled(); fill();
  expect(await screen.findByText(t("feedSite.unsupported"))).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: t("addPreset.save") }));
  await waitFor(() => expect(added).toHaveBeenCalledTimes(1));
  expect(mocks.save).toHaveBeenCalledTimes(1);
  expect(mocks.save.mock.calls[0][0]).toMatchObject({ origin: "custom", origin_entry_ref: null, title: "Custom Anime", instance_label: "Living Room", subgroups: { entries: [] }, rss_sources: [{ source_kind: "custom_rss", locator: "https://feed.example.test/rss", group_name: "Exact Group" }] });
  expect(mocks.save.mock.calls[0][0].rss_sources).toHaveLength(1);
  const saved = mocks.save.mock.calls[0][0];
  expect(saved.business?.programs).toEqual((await import("../../testing/businessPreset")).preset.snapshot.programs);
  expect(saved.business_digest).toMatch(/^sha256:[a-f0-9]{64}$/);
  // Every rules section is one-time input (owner 2026-10-20): the new draft
  // takes a complete copy of the Global value, tag_filters included.
  const { preset } = await import("../../testing/businessPreset");
  expect(saved.rules.sections).toEqual(presetRuleSections(preset));
  expect(saved.business?.sections.release_rules).toEqual((await import("../../testing/businessPreset")).preset.snapshot.sections.release_rules);
  expect(fetch).not.toHaveBeenCalled();
 });
 it("retains the draft and exposes save failure without activation or implicit fetching", async () => {
  mocks.save.mockRejectedValueOnce(new Error("save rejected")); const added = vi.fn(); renderWithQuery(<AddCandidateDialog open onOpenChange={vi.fn()} onAdded={added} />); fill();
  fireEvent.click(screen.getByRole("button", { name: t("addPreset.save") }));
  await waitFor(() => expect(mocks.error).toHaveBeenCalledTimes(1));
  expect(added).not.toHaveBeenCalled(); expect(screen.getAllByRole("textbox")[0]).toHaveValue(" Custom Anime "); expect(fetch).not.toHaveBeenCalled();
 });
});
