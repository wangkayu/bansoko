import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReplacementPolicyDefaultView } from "../../lib/api/client";
import { SettingsReplacement, SettingsUnclassified } from "./SettingsReplacement";
import { SaveHarness } from "./saveHarness.test-utils";
import { t } from "../../lib/i18n";
import { compareItemName } from "../replacement/compareCopy";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));

let replacementPolicy: ReplacementPolicyDefaultView;
let route: { unclassified_route: "route" | "skip"; version: number };
let replacementPuts: unknown[];
let routePuts: unknown[];
let conflictNext: boolean;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  replacementPolicy = {
    admission_mode: "per_group",
    quality_profile: { version: "profile-v1", items: [{ key: "subgroup.score", ranks: [] }, { key: "release.resolution", ranks: [["1080p"], ["720p"]] }] },
    version: 3,
  };
  route = { unclassified_route: "skip", version: 1 };
  replacementPuts = [];
  routePuts = [];
  conflictNext = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.includes("/file-maintenance/replacement/default")) {
      if (method === "PUT") {
        const body = JSON.parse(String(init?.body));
        replacementPuts.push(body);
        if (conflictNext) {
          conflictNext = false;
          replacementPolicy = { ...replacementPolicy, admission_mode: "per_group", version: 4 };
          return json({ code: "VERSION_CONFLICT", message: "stale", current_version: 4 }, 409);
        }
        const { expected_version: _expected, ...rest } = body;
        replacementPolicy = { ...replacementPolicy, ...rest, version: replacementPolicy.version + 1 };
      }
      return json(replacementPolicy);
    }
    if (url.includes("/file-maintenance/file-effect/default")) {
      if (method === "PUT") {
        const body = JSON.parse(String(init?.body));
        routePuts.push(body);
        route = { unclassified_route: body.unclassified_route, version: route.version + 1 };
      }
      return json(route);
    }
    return json({});
  }));
});

afterEach(() => vi.unstubAllGlobals());

function renderPage(onDirtyChange?: (dirty: boolean) => void, Page: typeof SettingsReplacement = SettingsReplacement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SaveHarness>
        <Page onDirtyChange={onDirtyChange} />
      </SaveHarness>
    </QueryClientProvider>,
  );
}

const moveResolutionUp = () => t("qualityCompare.moveUp", { item: compareItemName("release.resolution") });

describe("SettingsReplacement", () => {
  it("loads the global defaults and keeps save disabled until something changes", async () => {
    renderPage();
    await screen.findByRole("button", { name: moveResolutionUp() });
    expect(screen.getByRole("button", { name: t("settings.replacement.saveAria") })).toBeDisabled();
    const resolution = compareItemName("release.resolution");
    const ranks = screen.getByRole("list", { name: t("qualityCompare.ranksAria", { item: resolution }) });
    expect(within(ranks).getAllByRole("listitem").map((item) => item.textContent)).toEqual(["11080p", "2720p", t("qualityCompare.others")]);
    expect(screen.getByText(t("settings.replacement.replacedNote"))).toBeInTheDocument();
  });

  it("collapses the version priority to one line when keeping every version", async () => {
    replacementPolicy = { ...replacementPolicy, admission_mode: "all" };
    renderPage();
    expect(await screen.findByText(t("qualityCompare.allModeNote"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: moveResolutionUp() })).not.toBeInTheDocument();
    // Keeping every version replaces nothing, so no deletion note is shown.
    expect(screen.queryByText(t("settings.replacement.replacedNote"))).not.toBeInTheDocument();
  });

  it("saves a reordered comparison profile without a cutoff or cleanup policy", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: moveResolutionUp() }));
    fireEvent.click(screen.getByRole("button", { name: t("settings.replacement.saveAria") }));
    await waitFor(() => expect(replacementPuts).toHaveLength(1));
    expect(replacementPuts[0]).toMatchObject({
      quality_profile: { version: "profile-v1", items: [{ key: "release.resolution", ranks: [["1080p"], ["720p"]] }, { key: "subgroup.score", ranks: [] }] },
    });
    expect(replacementPuts[0]).not.toHaveProperty("cutoff");
    expect(replacementPuts[0]).not.toHaveProperty("cleanup_policy");
  });

  it("saves an edit with the loaded version as the CAS expectation and reports dirty state", async () => {
    const dirty = vi.fn();
    renderPage(dirty);
    fireEvent.click(await screen.findByRole("button", { name: moveResolutionUp() }));
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(true));
    fireEvent.click(screen.getByRole("button", { name: t("settings.replacement.saveAria") }));
    await waitFor(() => expect(replacementPuts).toHaveLength(1));
    expect(replacementPuts[0]).toMatchObject({ expected_version: 3, admission_mode: "per_group" });
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(false));
    expect(screen.getByRole("button", { name: t("settings.replacement.saveAria") })).toBeDisabled();
  });

  it("shows a version conflict and reloads the server value on refresh, dropping the local edit", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: moveResolutionUp() }));
    conflictNext = true;
    fireEvent.click(screen.getByRole("button", { name: t("settings.replacement.saveAria") }));
    expect(await screen.findByText(t("common.versionConflict"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("common.refreshLatest") }));
    // The reloaded server value keeps one version per group, so replaced old
    // files are deleted and the page says so.
    expect(await screen.findByText(t("settings.replacement.replacedNote"))).toBeInTheDocument();
    // The timing note belongs to the subscription's own editor, not the global default.
    expect(screen.queryByText(t("policy.versions.timingNote"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("common.versionConflict"))).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.replacement.saveAria") })).toBeDisabled();
  });

  it("saves the unclassified-file default through the footer save, independently of the replacement default", async () => {
    renderPage(undefined, SettingsUnclassified);
    // The buttons render before the data does; wait for the loaded control itself.
    const select = await screen.findByLabelText(t("settings.replacement.unclassified.selectLabel"));
    const footerSave = () => screen.getByRole("button", { name: t("act.save") });
    expect(footerSave()).toBeDisabled();
    // Open the select and choose the other option: only the draft changes.
    await userEvent.click(select);
    await userEvent.click(await screen.findByRole("option", { name: t("file.unclassified.route") }));
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(routePuts).toHaveLength(0);
    fireEvent.click(footerSave());
    await waitFor(() => expect(routePuts).toEqual([{ expected_version: 1, unclassified_route: "route" }]));
    expect(replacementPuts).toHaveLength(0);
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.replacement.unclassified.saved")));
    await waitFor(() => expect(footerSave()).toBeDisabled());
  });
});
