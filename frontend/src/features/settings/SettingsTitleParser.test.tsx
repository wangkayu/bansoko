import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { SettingsTitleParser } from "./SettingsTitleParser";
import type { TitleParserStatus } from "../../lib/api/client";
import { t } from "../../lib/i18n";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

let fetchMock: ReturnType<typeof vi.fn>;

type FetchHandler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: FetchHandler) {
  fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => handler(String(input), init));
  vi.stubGlobal("fetch", fetchMock);
}

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SettingsTitleParser />
    </QueryClientProvider>,
  );
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const builtinStatus: TitleParserStatus = {
  source: "builtin",
  version: "reltitle@v0.0.1",
  line: "0.0",
  config_digest: "lexicon:sha256:5a5a",
  binary_path: "/config/rt/reltitle",
  note: "",
  external_failure: "",
  lane: "stable",
  message: "",
};

const newerChannelStatus: TitleParserStatus = {
  ...builtinStatus,
  channel: {
    checked_at: "2026-11-02T08:00:00Z",
    version: "reltitle@v0.0.4",
    notes: {
      zh_cn: "## 0.0.4\n\n修复无修版识别",
      en: "## 0.0.4\n\nFixes unrated recognition",
    },
    release_url: "https://github.com/wangkayu/reltitle/releases/tag/v0.0.4",
    newer: true,
    error: "",
  },
};

const installedStatus: TitleParserStatus = {
  ...builtinStatus,
  source: "channel",
  version: "reltitle@v0.0.4",
  config_digest: "lexicon:sha256:0404",
  message: "installed reltitle reltitle@v0.0.4",
  channel: { ...newerChannelStatus.channel!, newer: false },
};

describe("SettingsTitleParser", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the running parser and an unchecked channel state without an install button", async () => {
    stubFetch((url) => {
      if (url.endsWith("/title-parser")) return jsonResponse(builtinStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();

    expect(await screen.findByText(t("settings.titleParser.currentTitle"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.source.builtin"))).toBeInTheDocument();
    expect(screen.getByText("reltitle@v0.0.1")).toBeInTheDocument();
    expect(screen.getByText("lexicon:sha256:5a5a")).toBeInTheDocument();
    expect(screen.getByText("/config/rt/reltitle")).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.channelEmptyTitle"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.titleParser.check") })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.titleParser.install") })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/title-parser/check"))).toBe(false);
  });

  it("runs a manual check and surfaces a newer channel with bilingual notes", async () => {
    stubFetch((url, init) => {
      if (url.endsWith("/title-parser") && init?.method !== "POST") return jsonResponse(builtinStatus);
      if (url.endsWith("/title-parser/check")) return jsonResponse(newerChannelStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();
    await screen.findByText(t("settings.titleParser.currentTitle"));

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.check") }));

    expect(await screen.findByText(t("settings.titleParser.hasUpdate"))).toBeInTheDocument();
    expect(screen.getByText("reltitle@v0.0.4")).toBeInTheDocument();
    // The zh-CN locale reads the zh_cn notes (restricted markdown, so a heading).
    expect(await screen.findByRole("heading", { name: "0.0.4" })).toBeInTheDocument();
    expect(screen.getByText("修复无修版识别")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "https://github.com/wangkayu/reltitle/releases/tag/v0.0.4" }),
    ).toBeInTheDocument();
  });

  it("keeps the last cached channel answer and toasts when a check cannot read the channel", async () => {
    const cachedFailure: TitleParserStatus = {
      ...builtinStatus,
      channel: {
        checked_at: "2026-11-02T07:00:00Z",
        version: "reltitle@v0.0.4",
        notes: { zh_cn: "", en: "" },
        release_url: "",
        newer: false,
        error: "raw.githubusercontent.com: connection refused",
      },
    };
    stubFetch((url, init) => {
      if (url.endsWith("/title-parser") && init?.method !== "POST") return jsonResponse(cachedFailure);
      if (url.endsWith("/title-parser/check"))
        return jsonResponse({ code: "TITLE_PARSER_CHANNEL_UNAVAILABLE", message: "The reltitle channel could not be read." }, 502);
      throw new Error("unexpected " + url);
    });
    renderPanel();
    // Both the state pill and the channel error alert carry the title.
    expect(await screen.findAllByText(t("settings.titleParser.channelErrorTitle"))).toHaveLength(2);

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.check") }));

    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    // The refetch presents the cached answer: the error stays visible, no install.
    expect(await screen.findAllByText(t("settings.titleParser.channelErrorTitle"))).toHaveLength(2);
    expect(screen.getByText("raw.githubusercontent.com: connection refused")).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.notesEmpty"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.titleParser.install") })).not.toBeInTheDocument();
  });

  it("installs a newer channel build behind a confirm dialog and reflects the new running parser", async () => {
    stubFetch((url, init) => {
      if (url.endsWith("/title-parser/update")) return jsonResponse(installedStatus);
      if (url.endsWith("/title-parser") && init?.method !== "POST") return jsonResponse(newerChannelStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();
    await screen.findByText(t("settings.titleParser.hasUpdate"));

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.install") }));
    expect(await screen.findByText(t("settings.titleParser.installConfirmTitle"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.installConfirmHint", { version: "reltitle@v0.0.4" }))).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.installConfirmAction") }));

    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(t("settings.titleParser.installedToast", { version: "reltitle@v0.0.4" })),
    );
    expect(await screen.findByText(t("settings.titleParser.source.channel"))).toBeInTheDocument();
    expect(screen.getByText("lexicon:sha256:0404")).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.messageLabel"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.upToDate"))).toBeInTheDocument();
  });

  it("reports a no-newer install answer instead of claiming an update", async () => {
    stubFetch((url, init) => {
      if (url.endsWith("/title-parser/update")) return jsonResponse(newerChannelStatus);
      if (url.endsWith("/title-parser") && init?.method !== "POST") return jsonResponse(newerChannelStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();
    await screen.findByText(t("settings.titleParser.hasUpdate"));

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.install") }));
    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.installConfirmAction") }));

    await waitFor(() => expect(toast.info).toHaveBeenCalledWith(t("settings.titleParser.installNoNewer")));
  });

  it("keeps the running parser visible and warns when the external parser failed", async () => {
    const failed: TitleParserStatus = {
      ...builtinStatus,
      external_failure: "reltitle@v0.0.3: smoke parse failed",
    };
    stubFetch((url) => jsonResponse(failed));
    renderPanel();

    expect(await screen.findByText(t("settings.titleParser.externalFailureTitle"))).toBeInTheDocument();
    expect(screen.getByText("reltitle@v0.0.3: smoke parse failed")).toBeInTheDocument();
    expect(screen.getByText("reltitle@v0.0.1")).toBeInTheDocument();
  });

  it("shows the read error with a reread action when the status cannot load", async () => {
    stubFetch(() => jsonResponse({ code: "UNAVAILABLE", message: "starting" }, 503));
    renderPanel();

    expect(await screen.findByText(t("settings.titleParser.statusFailed"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.titleParser.reread") })).toBeInTheDocument();
  });
});
