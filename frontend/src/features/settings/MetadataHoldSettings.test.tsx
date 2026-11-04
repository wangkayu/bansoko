import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GlobalSectionView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { MetadataHoldSettings } from "./MetadataHoldSettings";
import { SaveHarness } from "./saveHarness.test-utils";

// The release-without-metadata toggle, edited in place: the switch writes the draft, and the
//   settings dialog footer's single save submits the section.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type Call = { method: string; url: string; body?: unknown; ifMatch: string | null };

function stubFetch(section: GlobalSectionView) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const headers = new Headers(init?.headers);
      calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined, ifMatch: headers.get("If-Match") });
      if (url.includes("/config/global-sections/metadata_hold") && method === "PUT") {
        const body = calls[calls.length - 1].body as { value: { allow_missing_linkage: boolean } };
        return Response.json({ ...section, value: body.value, version: section.version + 1, saved: true });
      }
      if (url.includes("/config/global-sections")) return Response.json([section]);
      return Response.json([]);
    }),
  );
  return calls;
}

function renderCard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SaveHarness>
        <MetadataHoldSettings />
      </SaveHarness>
    </QueryClientProvider>,
  );
}

const offSection: GlobalSectionView = {
  section_name: "metadata_hold",
  schema_version: "metadata-hold-v1",
  value: { allow_missing_linkage: false },
  version: 1,
  saved: true,
};

describe("MetadataHoldSettings", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => vi.unstubAllGlobals());

  it("默认关闭，打开后由统一保存提交 allow_missing_linkage", async () => {
    const calls = stubFetch(offSection);
    renderCard();

    const toggle = await screen.findByRole("switch", { name: t("metadataHold.title") });
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(screen.getByText(t("metadataHold.description"))).toBeInTheDocument();

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-checked", "true");
    fireEvent.click(screen.getByRole("button", { name: t("act.save") }));

    await waitFor(() => {
      const put = calls.find((call) => call.method === "PUT");
      expect(put?.body).toEqual({ schema_version: "metadata-hold-v1", value: { allow_missing_linkage: true } });
      expect(put?.ifMatch).toBe("1");
    });
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  });

  it("已开启时关闭也只提交 allow_missing_linkage", async () => {
    const calls = stubFetch({ ...offSection, value: { allow_missing_linkage: true }, version: 4 });
    renderCard();

    const toggle = await screen.findByRole("switch", { name: t("metadataHold.title") });
    expect(toggle).toHaveAttribute("aria-checked", "true");
    fireEvent.click(toggle);
    fireEvent.click(screen.getByRole("button", { name: t("act.save") }));

    await waitFor(() => {
      const put = calls.find((call) => call.method === "PUT");
      expect(put?.body).toEqual({ schema_version: "metadata-hold-v1", value: { allow_missing_linkage: false } });
      expect(put?.ifMatch).toBe("4");
    });
  });

  it("没有改动时不提交", async () => {
    const calls = stubFetch(offSection);
    renderCard();

    await screen.findByRole("switch", { name: t("metadataHold.title") });
    expect(screen.getByRole("button", { name: t("act.save") })).toBeDisabled();
    expect(calls.some((call) => call.method === "PUT")).toBe(false);
  });
});
