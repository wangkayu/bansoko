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
  version: "reltitle@v0.0.3",
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

  it("shows the running parser with a digest footnote and an unchecked channel (state A)", async () => {
    stubFetch((url) => {
      if (url.endsWith("/title-parser")) return jsonResponse(builtinStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();

    expect(await screen.findByText(t("settings.titleParser.componentLabel"))).toBeInTheDocument();
    // Versions display short (reltitle@v0.0.3 → 0.0.3), full value in the title.
    expect(screen.getByText("0.0.3").getAttribute("title")).toBe("reltitle@v0.0.3");
    expect(screen.getByText(t("settings.titleParser.source.builtin"))).toBeInTheDocument();
    // The digest is a footnote, not a table row.
    expect(screen.getByText(t("settings.titleParser.digestFootnote", { digest: "lexicon:sha256:5a5a" }))).toBeInTheDocument();
    // The check button lives on the running-parser card.
    expect(screen.getByRole("button", { name: t("settings.titleParser.check") })).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.channelEmptyTitle"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /安装/ })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/title-parser/check"))).toBe(false);
  });

  it("shows a manually placed parser with the manual hint (state F)", async () => {
    const manual: TitleParserStatus = { ...builtinStatus, source: "manual", version: "0.0.3-custom", note: "" };
    stubFetch((url) => jsonResponse(manual));
    renderPanel();

    expect(await screen.findByText("0.0.3-custom")).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.source.manual"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.manualHint"))).toBeInTheDocument();
  });

  it("runs a manual check and surfaces a newer channel with bilingual notes (state B)", async () => {
    stubFetch((url, init) => {
      if (url.endsWith("/title-parser") && init?.method !== "POST") return jsonResponse(builtinStatus);
      if (url.endsWith("/title-parser/check")) return jsonResponse(newerChannelStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();
    await screen.findByText(t("settings.titleParser.componentLabel"));

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.check") }));

    expect(await screen.findByText(t("settings.titleParser.canUpdateTo", { version: "0.0.4" }))).toBeInTheDocument();
    // The zh-CN locale reads the zh_cn notes (restricted markdown, so a heading).
    expect(await screen.findByRole("heading", { name: "0.0.4" })).toBeInTheDocument();
    expect(screen.getByText("修复无修版识别")).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.channelFrom"))).toBeInTheDocument();
    expect(screen.getByRole("link", { name: t("settings.titleParser.releaseUrlLabel") })).toHaveProperty(
      "href",
      "https://github.com/wangkayu/reltitle/releases/tag/v0.0.4",
    );
    expect(
      screen.getByRole("button", { name: t("settings.titleParser.installVersion", { version: "0.0.4" }) }),
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
    await screen.findByText(t("settings.titleParser.channelErrorTitle"));

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.check") }));

    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    // The refetch presents the cached answer: the error stays visible, no install.
    expect(await screen.findByText(t("settings.titleParser.channelErrorTitle"))).toBeInTheDocument();
    expect(screen.getByText("raw.githubusercontent.com: connection refused")).toBeInTheDocument();
    // An errored check shows the reason; the notes block stays hidden.
    expect(screen.queryByText(t("settings.titleParser.notesEmpty"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /安装/ })).not.toBeInTheDocument();
  });

  it("installs behind a danger confirm and shows the transition receipt (states C and D)", async () => {
    stubFetch((url, init) => {
      if (url.endsWith("/title-parser/update")) return jsonResponse(installedStatus);
      if (url.endsWith("/title-parser") && init?.method !== "POST") return jsonResponse(newerChannelStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();
    await screen.findByText(t("settings.titleParser.canUpdateTo", { version: "0.0.4" }));

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.installVersion", { version: "0.0.4" }) }));
    expect(await screen.findByText(t("settings.titleParser.installConfirmTitle"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.installConfirmHint", { version: "0.0.4" }))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.installConfirmNote"))).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.installConfirmAction") }));

    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(t("settings.titleParser.installedToast", { version: "0.0.4" })),
    );
    expect(await screen.findByText(t("settings.titleParser.updatedReceipt", { from: "0.0.3", to: "0.0.4" }))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.source.channel"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.digestFootnote", { digest: "lexicon:sha256:0404" }))).toBeInTheDocument();
  });

  it("reports a no-newer install answer instead of claiming an update", async () => {
    stubFetch((url, init) => {
      if (url.endsWith("/title-parser/update")) return jsonResponse(newerChannelStatus);
      if (url.endsWith("/title-parser") && init?.method !== "POST") return jsonResponse(newerChannelStatus);
      throw new Error("unexpected " + url);
    });
    renderPanel();
    await screen.findByText(t("settings.titleParser.canUpdateTo", { version: "0.0.4" }));

    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.installVersion", { version: "0.0.4" }) }));
    await userEvent.click(screen.getByRole("button", { name: t("settings.titleParser.installConfirmAction") }));

    await waitFor(() => expect(toast.info).toHaveBeenCalledWith(t("settings.titleParser.installNoNewer")));
  });

  it("states a not-adopted binary neutrally, not as an alarm (state E)", async () => {
    const refused: TitleParserStatus = {
      ...builtinStatus,
      note: "protocol mismatch (needs rt-serve-1, got rt-serve-2)",
    };
    stubFetch((url) => jsonResponse(refused));
    renderPanel();

    expect(await screen.findByText(t("settings.titleParser.usingBuiltin", { version: "0.0.3" }))).toBeInTheDocument();
    expect(screen.getByText(t("settings.titleParser.notAdopted", { path: "/config/rt/reltitle" }))).toBeInTheDocument();
    expect(screen.getByText("protocol mismatch (needs rt-serve-1, got rt-serve-2)")).toBeInTheDocument();
    // A refused binary is a neutral fact, never a warning alert.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("also states a mid-flight external failure as a neutral fallback", async () => {
    const dropped: TitleParserStatus = {
      ...builtinStatus,
      external_failure: "reltitle@v0.0.4: process exited during a parse",
    };
    stubFetch((url) => jsonResponse(dropped));
    renderPanel();

    expect(await screen.findByText(t("settings.titleParser.usingBuiltin", { version: "0.0.3" }))).toBeInTheDocument();
    expect(screen.getByText("reltitle@v0.0.4: process exited during a parse")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows the read error with a reread action when the status cannot load", async () => {
    stubFetch(() => jsonResponse({ code: "UNAVAILABLE", message: "starting" }, 503));
    renderPanel();

    expect(await screen.findByText(t("settings.titleParser.statusFailed"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.titleParser.reread") })).toBeInTheDocument();
  });
});
