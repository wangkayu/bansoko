import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GlobalSectionView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { DownloadCompletionSettings } from "./DownloadCompletionSettings";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

function stubFetch(section: GlobalSectionView) {
  const fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (init?.method === "PUT" && url.includes("/config/global-sections/download_completion")) {
      const body = JSON.parse(String(init.body)) as { schema_version: string; value: Record<string, unknown> };
      return Response.json({ ...section, value: body.value, version: section.version + 1, saved: true });
    }
    if (url.includes("/config/global-sections")) return Response.json([section]);
    return Response.json([]);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderCard(importMethod = "hardlink") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <DownloadCompletionSettings importMethod={importMethod} />
    </QueryClientProvider>,
  );
}

function putBody(fetchMock: ReturnType<typeof stubFetch>) {
  const call = fetchMock.mock.calls.find(([input, init]) => String(input).includes("/config/global-sections/download_completion") && init?.method === "PUT");
  expect(call).toBeDefined();
  return { body: JSON.parse(String(call![1]!.body)) as unknown, ifMatch: (call![1]!.headers as Headers).get("If-Match") };
}

const seedSection: GlobalSectionView = {
  section_name: "download_completion",
  schema_version: "download-completion-v2",
  value: { action: "seed" },
  version: 2,
  saved: true,
};

describe("DownloadCompletionSettings", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("摘要显示处理方式与保种上限；入库方式为移动时说明无法做种", async () => {
    stubFetch({ ...seedSection, value: { action: "seed", seed_limits: { seeding_time_minutes: 2880, ratio: 2, at_limit: "remove" } } });
    renderCard("move");

    expect(await screen.findByText(t("downloadCompletion.title"))).toBeInTheDocument();
    expect(screen.getByText(t("downloadCompletion.action.seed"))).toBeInTheDocument();
    expect(screen.getByText(`${t("downloadCompletion.timeHours", { n: 48 })} · ${t("downloadCompletion.ratioValue", { n: 2 })}`)).toBeInTheDocument();
    expect(screen.getByText(t("downloadCompletion.atLimit.remove"))).toBeInTheDocument();
    expect(screen.getByText(t("downloadCompletion.moveSeedNote"))).toBeInTheDocument();
  });

  it("继续做种设置保种上限：保存整段，带上限和到达后的处理", async () => {
    const fetchMock = stubFetch(seedSection);
    renderCard();

    fireEvent.click(await screen.findByRole("button", { name: t("downloadCompletion.editTitle") }));
    fireEvent.click(screen.getByRole("checkbox", { name: t("downloadCompletion.limited") }));
    fireEvent.change(screen.getByRole("textbox", { name: t("downloadCompletion.seedingTime") }), { target: { value: "24" } });
    fireEvent.change(screen.getByRole("textbox", { name: t("downloadCompletion.ratio") }), { target: { value: "1.5" } });
    await userEvent.click(screen.getByRole("combobox", { name: t("downloadCompletion.atLimitLabel") }));
    await userEvent.click(await screen.findByRole("option", { name: t("downloadCompletion.atLimit.remove_with_data") }));
    expect(screen.getByRole("alert")).toHaveTextContent(t("downloadCompletion.removeDataWarning"));
    fireEvent.click(screen.getByRole("button", { name: t("downloadCompletion.save") }));

    await waitFor(() => expect(putBody(fetchMock).body).toEqual({
      schema_version: "download-completion-v2",
      value: { action: "seed", seed_limits: { seeding_time_minutes: 1440, ratio: 1.5, at_limit: "remove_with_data" } },
    }));
    expect(putBody(fetchMock).ifMatch).toBe("2");
  });

  it("勾选上限但未填写时不保存，并提示原因", async () => {
    const fetchMock = stubFetch(seedSection);
    renderCard();

    fireEvent.click(await screen.findByRole("button", { name: t("downloadCompletion.editTitle") }));
    fireEvent.click(screen.getByRole("checkbox", { name: t("downloadCompletion.limited") }));
    fireEvent.click(screen.getByRole("button", { name: t("downloadCompletion.save") }));

    expect(await screen.findByText(t("downloadCompletion.limitRequired"))).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("改为其他处理方式时不显示保种上限，保存只带处理方式", async () => {
    const fetchMock = stubFetch({ ...seedSection, value: { action: "seed", seed_limits: { ratio: 2, at_limit: "stop" } } });
    renderCard();

    fireEvent.click(await screen.findByRole("button", { name: t("downloadCompletion.editTitle") }));
    await userEvent.click(screen.getByRole("combobox", { name: t("downloadCompletion.field") }));
    await userEvent.click(await screen.findByRole("option", { name: t("downloadCompletion.action.pause") }));
    expect(screen.queryByRole("checkbox", { name: t("downloadCompletion.limited") })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t("downloadCompletion.save") }));

    await waitFor(() => expect(putBody(fetchMock).body).toEqual({ schema_version: "download-completion-v2", value: { action: "pause" } }));
  });
});
