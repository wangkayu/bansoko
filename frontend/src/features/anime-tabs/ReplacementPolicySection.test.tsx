import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReplacementPolicySection } from "./ReplacementPolicySection";
import { compareItemName } from "../replacement/compareCopy";
import { t } from "../../lib/i18n";
import { SavePartsProvider, useSaveParts } from "../../lib/saveParts";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const profile = { version: "profile-v1", items: [{ key: "subgroup.score", ranks: [] }, { key: "release.resolution", ranks: [["1080p"], ["720p"]] }] };
let stored: { admission_mode: string; quality_profile: unknown; version: number };
let puts: Record<string, unknown>[];
let putUrls: string[];
let conflictNext: boolean;

beforeEach(() => {
  stored = { admission_mode: "per_group", quality_profile: profile, version: 4 };
  puts = [];
  putUrls = [];
  conflictNext = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/file-maintenance/replacement/anime/")) {
      if (init?.method === "PUT") {
        if (conflictNext) return json({ code: "OWNER_VERSION_CONFLICT", current_version: 9, message: "conflict" }, 409);
        const body = JSON.parse(String(init.body)) as Record<string, unknown>;
        puts.push(body);
        putUrls.push(url);
        stored = { admission_mode: String(body.admission_mode), quality_profile: body.quality_profile, version: stored.version + 1 };
      }
      return json({ managed_anime_id: "anime-1", source_default_version: 1, ...stored });
    }
    return json({ items: [] });
  }));
});

afterEach(() => vi.unstubAllGlobals());

function SaveHarness({ children }: { children: ReactNode }) {
  const parts = useSaveParts(["replacement"] as const);
  const count = parts.count();
  return (
    <SavePartsProvider value={parts.registrar}>
      {children}
      <output data-testid="unsaved-count">{count}</output>
      <button type="button" disabled={count === 0} onClick={() => void parts.save()}>
        {t("act.save")}
      </button>
    </SavePartsProvider>
  );
}

function renderSection(props: { readOnly?: boolean } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SaveHarness>
        <ReplacementPolicySection animeID="anime-1" readOnly={props.readOnly} />
      </SaveHarness>
    </QueryClientProvider>,
  );
}

const unsavedCount = () => screen.getByTestId("unsaved-count").textContent;
const moveUp = () => screen.findByRole("button", { name: t("qualityCompare.moveUp", { item: compareItemName("release.resolution") }) });
const modeSelect = () => screen.findByRole("combobox", { name: t("replacement.admissionTitle") });

describe("ReplacementPolicySection", () => {
  it("reads the subscription's own policy and shows the timing note", async () => {
    renderSection();
    expect(await modeSelect()).toHaveTextContent(t("replacement.admission.per_group"));
    expect(screen.getByRole("heading", { name: t("config.snapshot.legend") })).toBeInTheDocument();
    expect(screen.getByText(t("policy.versions.timingNote"))).toBeInTheDocument();
    expect(unsavedCount()).toBe("0");
  });

  it("registers an edit and saves it with a PUT carrying the loaded version", async () => {
    renderSection();
    await userEvent.click(await moveUp());
    await waitFor(() => expect(unsavedCount()).toBe("1"));
    await userEvent.click(screen.getByRole("button", { name: t("act.save") }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(putUrls[0]).toContain("/file-maintenance/replacement/anime/anime-1");
    expect(puts[0]).toMatchObject({
      expected_version: 4,
      admission_mode: "per_group",
      quality_profile: { items: [{ key: "release.resolution" }, { key: "subgroup.score" }] },
    });
    await waitFor(() => expect(unsavedCount()).toBe("0"));
  });

  it("collapses the version priority under keep-all and restores the edited terms when switching back", async () => {
    renderSection();
    await userEvent.click(await moveUp());
    await userEvent.click(await modeSelect());
    await userEvent.click(await screen.findByRole("option", { name: t("replacement.admission.all") }));
    expect(screen.getByText(t("qualityCompare.allModeNote"))).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: t("qualityCompare.listAria") })).not.toBeInTheDocument();
    expect(screen.queryByText(t("policy.versions.timingNote"))).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: t("replacement.admissionTitle") }));
    await userEvent.click(await screen.findByRole("option", { name: t("replacement.admission.single_copy") }));
    const items = within(screen.getByRole("list", { name: t("qualityCompare.listAria") })).getAllByRole("listitem", { hidden: false });
    expect(items[0]).toHaveTextContent(t("qualityCompare.item.release_resolution"));
  });

  it("shows a conflict note and keeps the local edit", async () => {
    renderSection();
    await userEvent.click(await moveUp());
    conflictNext = true;
    await userEvent.click(screen.getByRole("button", { name: t("act.save") }));
    expect(await screen.findByText(t("policy.versionConflictRefreshed"))).toBeInTheDocument();
    expect(unsavedCount()).toBe("1");
  });

  it("is read-only for a retired subscription", async () => {
    renderSection({ readOnly: true });
    expect(await modeSelect()).toBeDisabled();
    expect(screen.queryByText(t("policy.versions.timingNote"))).not.toBeInTheDocument();
    expect(unsavedCount()).toBe("0");
  });
});
