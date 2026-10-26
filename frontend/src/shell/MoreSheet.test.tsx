import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Connection } from "../lib/api/client";
import { t } from "../lib/i18n";
import { MoreSheet } from "./MoreSheet";

vi.mock("next-themes", () => ({ useTheme: () => ({ theme: "light", setTheme: () => {} }) }));

// The phone "More" sheet: hidden anime live in Settings and are not in the tools section;
// the system section lists the downloader, and with no usable downloader it says "no
// downloader configured" with a go-to-settings action; ↗ appears only on menu rows that open
// a dialog.

const qbConnection = { id: "c-qb", kind: "qbittorrent", name: "qB", enabled: true, version: 1 } as unknown as Connection;
const mikanConnection = { id: "c-mikan", kind: "mikan", name: "Mikan", enabled: true, version: 1 } as unknown as Connection;

function renderSheet(connections: Connection[], handlers: Partial<Record<"onOpenDownloaderSettings", () => void>> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MoreSheet
        open
        onClose={() => {}}
        onLogout={() => {}}
        connections={connections}
        failedCount={0}
        onOpenSearch={() => {}}
        onOpenRecords={() => {}}
        onOpenLog={() => {}}
        onOpenSettings={() => {}}
        onOpenDownloaderSettings={handlers.onOpenDownloaderSettings ?? (() => {})}
      />
    </QueryClientProvider>,
  );
}

describe("MoreSheet", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("[]", { status: 200, headers: { "Content-Type": "application/json" } })),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("工具区没有隐藏的番剧入口（它在设置里）", () => {
    renderSheet([qbConnection]);
    const sheet = screen.getByRole("dialog", { name: t("nav.more") });
    expect(within(sheet).queryByRole("button", { name: t("settings.nav.hidden") })).not.toBeInTheDocument();
  });

  it("没有可用下载器时系统区写「未配置下载器」，点「去设置」打开下载设置", () => {
    const onOpenDownloaderSettings = vi.fn();
    renderSheet([mikanConnection], { onOpenDownloaderSettings });
    const row = screen.getByRole("button", { name: new RegExp(t("nav.statusNoDownloader")) });
    expect(row).toHaveTextContent(t("nav.goSettings"));
    fireEvent.click(row);
    expect(onOpenDownloaderSettings).toHaveBeenCalledTimes(1);
  });

  it("有下载器时它排在系统区第一位，不显示缺失提示", () => {
    renderSheet([mikanConnection, qbConnection]);
    expect(screen.queryByText(t("nav.statusNoDownloader"))).not.toBeInTheDocument();
    const items = document.querySelectorAll(".more-sheet-status-item");
    expect(items[0]).toHaveTextContent(t("nav.connection.qbittorrent"));
  });

  it("系统区状态列表包含 Nyaa，排在 AniBT 与 Mikan 之后", () => {
    const nyaa = { id: "c-nyaa", kind: "nyaa", name: "Nyaa", enabled: true, version: 1 } as unknown as Connection;
    const anibt = { id: "c-anibt", kind: "anibt", name: "AniBT", enabled: true, version: 1 } as unknown as Connection;
    renderSheet([nyaa, mikanConnection, anibt, qbConnection]);
    const items = Array.from(document.querySelectorAll(".more-sheet-status-item")).map((el) => el.textContent ?? "");
    const at = (label: string) => items.findIndex((text) => text.includes(label));
    expect(at("Nyaa")).toBeGreaterThan(at("Mikan"));
    expect(at("Mikan")).toBeGreaterThan(at("AniBT"));
  });
});
