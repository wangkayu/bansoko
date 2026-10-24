import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FileEffectPolicySection } from "./FileEffectPolicySection";
import { t } from "../../lib/i18n";
import { SavePartsProvider, useSaveParts } from "../../lib/saveParts";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const video = "/library/Show/Season 01/Show - E01.mkv";
const subtitle = "/library/Show/Season 01/Show - 01.sc.ass";
const expectedSubtitle = "/library/Show/Season 01/Show - E01.sc.ass";

// Write requests are recorded in arrival order (path + body), to assert the footer save's submission order.
const puts: { url: string; body: unknown }[] = [];
let routeConflict = false;
// The server's stored value: updated after a successful PUT, and a later GET reads the new value back.
let storedRoute = "skip";
let storedAlignment = false;
let storedVersion = 1;

beforeEach(() => {
  puts.length = 0;
  routeConflict = false;
  storedRoute = "skip";
  storedAlignment = false;
  storedVersion = 1;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      if (url.includes("/file-maintenance/file-effect/anime/")) {
        if (routeConflict) return json({ code: "OWNER_VERSION_CONFLICT", current_version: 4, message: "conflict" }, 409);
        puts.push({ url, body });
        storedRoute = String(body.unclassified_route);
        return json({ unclassified_route: storedRoute, version: 2 });
      }
      if (url.includes("/file-maintenance/policy/anime/")) {
        puts.push({ url, body });
        storedAlignment = Boolean(body.alignment_enabled);
        storedVersion = 2;
        return json({ scope: "anime", alignment_enabled: storedAlignment, enabled: true, version: storedVersion });
      }
    }
    if (url.includes("/file-maintenance/policy/anime/")) {
      return json({ scope: "anime", alignment_enabled: storedAlignment, enabled: true, version: storedVersion });
    }
    if (url.includes("/path-diffs")) {
      return json([{ file_path: video, disposition: "present", expected_path: video, differs: false, companions: [
        { file_path: subtitle, expected_path: expectedSubtitle, media_kind: "subtitle", state: "present", differs: true },
      ] }]);
    }
    if (url.includes("/file-maintenance/file-effect/anime/")) {
      return json({ unclassified_route: storedRoute, version: 1 });
    }
    return json({});
  }));
});

afterEach(() => vi.unstubAllGlobals());

// A mock rules-tab footer: fileEffect → alignment submitted in order, single save button + count.
const harnessOrder = ["fileEffect", "alignment"] as const;
function SaveHarness({ children }: { children: ReactNode }) {
  const parts = useSaveParts(harnessOrder);
  const count = parts.count();
  return (
    <SavePartsProvider value={parts.registrar}>
      {children}
      <output data-testid="unsaved-count">{count}</output>
      {parts.failed && <span data-testid="failed-part">{parts.failed}</span>}
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
        <FileEffectPolicySection animeID="anime-1" readOnly={props.readOnly} />
      </SaveHarness>
    </QueryClientProvider>,
  );
}

const unsavedCount = () => screen.getByTestId("unsaved-count").textContent;
const alignmentSwitch = () => screen.findByRole("switch", { name: t("policy.fileEffect.alignmentTitle") });

describe("FileEffectPolicySection layout", () => {
  it("names the setting once and shows only the option label", async () => {
    renderSection();
    await alignmentSwitch();
    expect(screen.getAllByText(t("file.unclassifiedTitle"))).toHaveLength(1);
    expect(screen.getByRole("heading", { name: t("file.unclassifiedTitle") })).toBeInTheDocument();
    // No on/off text beside the switch anymore.
    expect(screen.queryByText(t("act.on"))).not.toBeInTheDocument();
  });

  it("shows companion moves in the alignment confirmation", async () => {
    renderSection();
    const toggle = await alignmentSwitch();
    await userEvent.click(toggle);
    expect(await screen.findByText(`${t("policy.fileEffect.willChange")}${subtitle} → ${expectedSubtitle}`)).toBeInTheDocument();
    expect(screen.getByText(`${t("policy.fileEffect.keep")}${video} → ${video}`)).toBeInTheDocument();
  });
});

describe("FileEffectPolicySection unsaved edits", () => {
  it("registers route and alignment drafts; the footer save submits route then alignment", async () => {
    renderSection();
    const combo = await screen.findByRole("combobox", { name: t("file.unclassifiedTitle") });
    expect(unsavedCount()).toBe("0");
    // The card has no save button of its own and no "save separately" note.
    expect(screen.getAllByRole("button", { name: t("act.save") })).toHaveLength(1);

    await userEvent.click(combo);
    await userEvent.click(await screen.findByRole("option", { name: t("file.unclassified.route") }));
    expect(unsavedCount()).toBe("1");

    // Turning on auto-alignment: the path differences are confirmed first; confirming only writes the draft, firing no request.
    await userEvent.click(await alignmentSwitch());
    await userEvent.click(await screen.findByRole("button", { name: t("policy.fileEffect.confirm") }));
    await waitFor(() => expect(screen.queryByText(t("policy.fileEffect.confirmBody"))).not.toBeInTheDocument());
    expect(await alignmentSwitch()).toBeChecked();
    expect(unsavedCount()).toBe("2");
    expect(puts).toHaveLength(0);

    await userEvent.click(screen.getByRole("button", { name: t("act.save") }));
    await waitFor(() => expect(unsavedCount()).toBe("0"));
    expect(puts.map((put) => put.url.includes("/file-effect/") ? "fileEffect" : "alignment")).toEqual(["fileEffect", "alignment"]);
    expect(puts[0]!.body).toEqual({ expected_version: 1, unclassified_route: "route" });
    expect(puts[1]!.body).toEqual({ enabled: true, alignment_enabled: true, expected_version: 1 });
  });

  it("cancelling the alignment confirmation leaves the switch off and nothing unsaved", async () => {
    renderSection();
    await userEvent.click(await alignmentSwitch());
    await userEvent.click(await screen.findByRole("button", { name: t("policy.fileEffect.later") }));
    expect(await alignmentSwitch()).not.toBeChecked();
    expect(unsavedCount()).toBe("0");
    expect(puts).toHaveLength(0);
  });

  it("a route conflict stops the save before alignment and names the failed part", async () => {
    routeConflict = true;
    renderSection();
    await userEvent.click(await screen.findByRole("combobox", { name: t("file.unclassifiedTitle") }));
    await userEvent.click(await screen.findByRole("option", { name: t("file.unclassified.route") }));
    await userEvent.click(await alignmentSwitch());
    await userEvent.click(await screen.findByRole("button", { name: t("policy.fileEffect.confirm") }));
    await waitFor(() => expect(unsavedCount()).toBe("2"));

    await userEvent.click(screen.getByRole("button", { name: t("act.save") }));
    expect((await screen.findAllByText(t("common.versionConflict"))).length).toBeGreaterThan(0);
    expect(screen.getByTestId("failed-part")).toHaveTextContent(t("file.unclassifiedTitle"));
    expect(puts).toHaveLength(0);
    expect(unsavedCount()).toBe("2");
  });

  it("read-only disables the route select and the alignment switch", async () => {
    renderSection({ readOnly: true });
    expect(await screen.findByRole("combobox", { name: t("file.unclassifiedTitle") })).toBeDisabled();
    expect(await alignmentSwitch()).toHaveAttribute("aria-disabled", "true");
    expect(unsavedCount()).toBe("0");
  });
});
