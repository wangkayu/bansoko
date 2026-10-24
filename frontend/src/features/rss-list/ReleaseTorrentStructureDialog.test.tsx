import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import { renderWithQuery } from "../../testing/renderWithQuery";
import { ReleaseTorrentStructureDialog } from "./ReleaseTorrentStructureDialog";
import { formatStructureBytes } from "./torrentStructure";

function stubResponse(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ReleaseTorrentStructureDialog", () => {
  it("404 表示载荷不在保留期内：只显示该文案，没有重试入口", async () => {
    stubResponse(404, { code: "TORRENT_STRUCTURE_UNAVAILABLE", message: "This release no longer has a retained torrent payload." });
    renderWithQuery(<ReleaseTorrentStructureDialog releaseID="release-1" onClose={() => {}} />);

    expect(await screen.findByText(t("rss.structure.unavailable"))).toBeInTheDocument();
    // 这是正常状态，不是加载失败：不显示错误文案，也没有重试。
    expect(screen.queryByText(t("rss.structure.loadFailed"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("log.retry") })).not.toBeInTheDocument();
  });

  it("渲染 torrent 名称、总计行、一级条目与截断提示", async () => {
    stubResponse(200, {
      name: "Torrent name",
      torrent_version: "v1",
      total_size: 3072,
      file_count: 3,
      files: [
        { path: "Show/A.mkv", size_bytes: 2048 },
        { path: "Show/B.mkv", size_bytes: 1024 },
        { path: "readme.txt", size_bytes: 0 },
      ],
      truncated: true,
    });
    renderWithQuery(<ReleaseTorrentStructureDialog releaseID="release-1" onClose={() => {}} />);

    expect(await screen.findByText("Torrent name")).toBeInTheDocument();
    expect(screen.getByText(t("rss.structure.total", { size: "3.0 KiB", n: 3 }))).toBeInTheDocument();
    const listing = document.querySelector("pre");
    expect(listing?.textContent).toContain("Show/");
    expect(listing?.textContent).toContain(`${formatStructureBytes(3072)} · ${t("rss.structure.files", { n: 2 })}`);
    expect(listing?.textContent).toContain("readme.txt");
    expect(screen.getByText(t("rss.structure.truncated", { n: 3 }))).toBeInTheDocument();
  });
});
