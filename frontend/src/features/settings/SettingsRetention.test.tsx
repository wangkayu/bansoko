import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RetentionPolicyDefaultView } from "../../lib/api/client";
import { SettingsRetention } from "./SettingsRetention";
import { SaveHarness } from "./saveHarness.test-utils";
import { t } from "../../lib/i18n";
import { compareItemName } from "../retention/compareCopy";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));

let retention: RetentionPolicyDefaultView;
let route: { unclassified_route: "route" | "skip"; version: number };
let retentionPuts: unknown[];
let routePuts: unknown[];
let conflictNext: boolean;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  retention = {
    admission_mode: "all",
    quality_profile: { version: "profile-v1", items: [{ key: "subgroup.score", ranks: [] }, { key: "release.resolution", ranks: [["1080p"], ["720p"]] }] },
    cleanup_policy: { version: "cleanup-v1", effect: "quarantine", grace_seconds: 604800 },
    version: 3,
  };
  route = { unclassified_route: "skip", version: 1 };
  retentionPuts = [];
  routePuts = [];
  conflictNext = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (url.includes("/file-maintenance/retention/default")) {
      if (method === "PUT") {
        const body = JSON.parse(String(init?.body));
        retentionPuts.push(body);
        if (conflictNext) {
          conflictNext = false;
          retention = { ...retention, admission_mode: "per_group", version: 4 };
          return json({ code: "VERSION_CONFLICT", message: "stale", current_version: 4 }, 409);
        }
        const { expected_version: _expected, ...rest } = body;
        retention = { ...retention, ...rest, version: retention.version + 1 };
      }
      return json(retention);
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

function renderPage(onDirtyChange?: (dirty: boolean) => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SaveHarness>
        <SettingsRetention onDirtyChange={onDirtyChange} />
      </SaveHarness>
    </QueryClientProvider>,
  );
}

describe("SettingsRetention", () => {
  it("loads the global defaults and keeps save disabled until something changes", async () => {
    renderPage();
    expect(await screen.findByLabelText(t("settings.retention.graceAria"))).toHaveValue("604800");
    expect(screen.getByRole("button", { name: t("settings.retention.saveAria") })).toBeDisabled();
    const resolution = compareItemName("release.resolution");
    const ranks = screen.getByRole("list", { name: t("qualityCompare.ranksAria", { item: resolution }) });
    expect(within(ranks).getAllByRole("listitem").map((item) => item.textContent)).toEqual(["11080p", "2720p", t("qualityCompare.others")]);
  });

  it("saves a reordered comparison profile without a cutoff", async () => {
    renderPage();
    await screen.findByLabelText(t("settings.retention.graceAria"));
    fireEvent.click(screen.getByRole("button", { name: t("qualityCompare.moveUp", { item: compareItemName("release.resolution") }) }));
    fireEvent.click(screen.getByRole("button", { name: t("settings.retention.saveAria") }));
    await waitFor(() => expect(retentionPuts).toHaveLength(1));
    expect(retentionPuts[0]).toMatchObject({
      quality_profile: { version: "profile-v1", items: [{ key: "release.resolution", ranks: [["1080p"], ["720p"]] }, { key: "subgroup.score", ranks: [] }] },
    });
    expect(retentionPuts[0]).not.toHaveProperty("cutoff");
  });

  it("saves an edit with the loaded version as the CAS expectation and reports dirty state", async () => {
    const dirty = vi.fn();
    renderPage(dirty);
    const grace = await screen.findByLabelText(t("settings.retention.graceAria"));
    fireEvent.change(grace, { target: { value: "120" } });
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(true));
    fireEvent.click(screen.getByRole("button", { name: t("settings.retention.saveAria") }));
    await waitFor(() => expect(retentionPuts).toHaveLength(1));
    expect(retentionPuts[0]).toMatchObject({
      expected_version: 3,
      admission_mode: "all",
      cleanup_policy: { version: "cleanup-v1", effect: "quarantine", grace_seconds: 120 },
    });
    await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(false));
    expect(screen.getByRole("button", { name: t("settings.retention.saveAria") })).toBeDisabled();
  });

  it("blocks saving an invalid grace period without calling the server", async () => {
    renderPage();
    fireEvent.change(await screen.findByLabelText(t("settings.retention.graceAria")), { target: { value: "-5" } });
    expect(await screen.findByRole("alert")).toHaveTextContent(t("settings.retention.graceInvalid"));
    expect(screen.getByRole("button", { name: t("settings.retention.saveAria") })).toBeDisabled();
    expect(retentionPuts).toHaveLength(0);
  });

  it("shows a version conflict and reloads the server value on refresh, dropping the local edit", async () => {
    renderPage();
    fireEvent.change(await screen.findByLabelText(t("settings.retention.graceAria")), { target: { value: "120" } });
    conflictNext = true;
    fireEvent.click(screen.getByRole("button", { name: t("settings.retention.saveAria") }));
    expect(await screen.findByText(t("common.versionConflict"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("common.refreshLatest") }));
    await waitFor(() => expect(screen.getByLabelText(t("settings.retention.graceAria"))).toHaveValue("604800"));
    expect(screen.queryByText(t("common.versionConflict"))).not.toBeInTheDocument();
  });

  it("saves the unclassified-file default through the footer save, independently of the retention default", async () => {
    renderPage();
    // The buttons render before the data does; wait for the loaded control itself.
    const select = await screen.findByLabelText(t("settings.retention.unclassified.selectLabel"));
    const footerSave = () => screen.getByRole("button", { name: t("act.save") });
    expect(footerSave()).toBeDisabled();
    // Open the select and choose the other option: only the draft changes.
    await userEvent.click(select);
    await userEvent.click(await screen.findByRole("option", { name: t("file.unclassified.route") }));
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(routePuts).toHaveLength(0);
    fireEvent.click(footerSave());
    await waitFor(() => expect(routePuts).toEqual([{ expected_version: 1, unclassified_route: "route" }]));
    expect(retentionPuts).toHaveLength(0);
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.retention.unclassified.saved")));
    await waitFor(() => expect(footerSave()).toBeDisabled());
  });
});
