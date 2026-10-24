import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GlobalSectionView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { DownloadTimeoutsSettings } from "./DownloadTimeoutsSettings";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;

function section(value: Record<string, unknown>, saved = false, version = 1): GlobalSectionView {
  return { section_name: "download_timeouts", schema_version: "download-timeouts-v1", value, version, saved };
}

function stubFetch(view: GlobalSectionView) {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (init?.method === "PUT" && url.includes("/config/global-sections/download_timeouts")) {
      const body = JSON.parse(String(init.body)) as { value: Record<string, unknown> };
      return new Response(JSON.stringify(section(body.value, true, 2)), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    return new Response(JSON.stringify(url.includes("/config/global-sections") ? [view] : []), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <DownloadTimeoutsSettings />
    </QueryClientProvider>,
  );
}

function putCalls() {
  return fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/config/global-sections/") && init?.method === "PUT");
}

const stallAmount = () => t("downloadTimeouts.amountAria", { field: t("downloadTimeouts.stallLabel") });
const waitAmount = () => t("downloadTimeouts.amountAria", { field: t("downloadTimeouts.waitLabel") });
const waitUnit = () => t("downloadTimeouts.unitAria", { field: t("downloadTimeouts.waitLabel") });

describe("DownloadTimeoutsSettings", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("shows the default values in hours and days with the only-new-downloads hint", async () => {
    stubFetch(section({ stall_seconds: 21600, download_wait_seconds: 604800 }));
    renderCard();
    expect(await screen.findByText(t("downloadTimeouts.amount.hours", { n: "6" }))).toBeInTheDocument();
    expect(screen.getByText(t("downloadTimeouts.amount.days", { n: "7" }))).toBeInTheDocument();
    expect(screen.getByText(t("downloadTimeouts.hint"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.badge.builtin"))).toBeInTheDocument();
  });

  it("saves the edited values in seconds with the section version", async () => {
    stubFetch(section({ stall_seconds: 21600, download_wait_seconds: 604800 }));
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: t("downloadTimeouts.editTitle") }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText(stallAmount())).toHaveValue("6");
    fireEvent.change(within(dialog).getByLabelText(stallAmount()), { target: { value: "2" } });
    fireEvent.change(within(dialog).getByLabelText(waitAmount()), { target: { value: "36" } });
    await userEvent.click(within(dialog).getByLabelText(waitUnit()));
    await userEvent.click(await screen.findByRole("option", { name: t("downloadTimeouts.unit.hours") }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("downloadTimeouts.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("downloadTimeouts.saved")));
    expect(putCalls()).toHaveLength(1);
    const [input, init] = putCalls()[0]!;
    expect(String(input)).toContain("/config/global-sections/download_timeouts");
    expect(JSON.parse(String(init?.body))).toEqual({
      schema_version: "download-timeouts-v1",
      value: { stall_seconds: 7200, download_wait_seconds: 129600 },
    });
    expect(new Headers(init?.headers).get("If-Match")).toBe("1");
  });

  it.each([
    ["0", "7", () => t("downloadTimeouts.invalidNumber")],
    ["1.5", "7", () => t("downloadTimeouts.invalidNumber")],
    ["8", "7", () => t("downloadTimeouts.stallRange")],
    ["6", "31", () => t("downloadTimeouts.waitRange")],
  ])("refuses stall %s h / wait %s d before saving", async (stall, wait, message) => {
    stubFetch(section({ stall_seconds: 21600, download_wait_seconds: 604800 }));
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: t("downloadTimeouts.editTitle") }));
    const dialog = await screen.findByRole("dialog");
    const stallUnit = t("downloadTimeouts.unitAria", { field: t("downloadTimeouts.stallLabel") });
    if (stall === "8") {
      await userEvent.click(within(dialog).getByLabelText(stallUnit));
      await userEvent.click(await screen.findByRole("option", { name: t("downloadTimeouts.unit.days") }));
    }
    fireEvent.change(within(dialog).getByLabelText(stallAmount()), { target: { value: stall } });
    fireEvent.change(within(dialog).getByLabelText(waitAmount()), { target: { value: wait } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("downloadTimeouts.save") }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(message());
    expect(putCalls()).toHaveLength(0);
  });

  it("refuses a no-progress timeout longer than the maximum download time", async () => {
    stubFetch(section({ stall_seconds: 21600, download_wait_seconds: 86400 }, true));
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: t("downloadTimeouts.editTitle") }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText(waitAmount())).toHaveValue("1");
    fireEvent.change(within(dialog).getByLabelText(stallAmount()), { target: { value: "25" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("downloadTimeouts.save") }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(t("downloadTimeouts.stallOverWait"));
    expect(putCalls()).toHaveLength(0);
  });
});
