import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { setSessionTokenProvider } from "../../lib/api/client";
import { resetAuthedImageCache } from "../../lib/useAuthedImage";
import { Dialog } from "../../ui/base";
import { t } from "../../lib/i18n";
import { HeaderPoster } from "./HeaderPoster";

const POSTER = "/api/v1/images/poster/583729";

function entry(overrides: Partial<CatalogEntryView> = {}): CatalogEntryView {
  return { providerEntryRef: "mikan:4012", source: "mikan", title: "测试番剧", metadataMatch: { matched: false }, ...overrides };
}

// Mounted the same way as the anime config dialog: the poster sits in the parent dialog's title area and the full image is a nested dialog.
function renderInParent(value: CatalogEntryView, onParentClose = vi.fn()) {
  render(
    <Dialog open onRequestClose={onParentClose} tier="L" title={value.title} headerStart={<HeaderPoster entry={value} />}>
      <p>body</p>
    </Dialog>,
  );
  return onParentClose;
}

describe("HeaderPoster", () => {
  beforeEach(() => {
    URL.createObjectURL = vi.fn(() => "blob:poster");
    URL.revokeObjectURL = vi.fn();
    setSessionTokenProvider(() => "t-1");
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url === POSTER
          ? ({ ok: true, status: 200, blob: async () => new Blob(["img"], { type: "image/jpeg" }) } as unknown as Response)
          : new Response(null, { status: 404 }),
      ),
    );
  });

  afterEach(() => {
    resetAuthedImageCache();
    vi.unstubAllGlobals();
    setSessionTokenProvider(() => "");
  });

  it("有海报时渲染缩略图按钮，点击打开大图，Esc 只关闭大图并把焦点还给缩略图", async () => {
    const user = userEvent.setup();
    const onParentClose = renderInParent(entry({ posterUrl: POSTER }));
    const thumb = await screen.findByRole("button", { name: t("config.viewPoster") });
    expect(thumb.querySelector("img")?.getAttribute("src")).toBe("blob:poster");

    await user.click(thumb);
    const large = await screen.findByRole("img", { name: "测试番剧" });
    expect(large.getAttribute("src")).toBe("blob:poster");
    // The nested layer (1100), stacked above the anime config dialog (1000).
    expect((large.closest("[role='dialog']") as HTMLElement).style.zIndex).toBe("1100");

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("img", { name: "测试番剧" })).toBeNull());
    expect(onParentClose).not.toHaveBeenCalled();
    await waitFor(() => expect(thumb).toHaveFocus());
  });

  it("关闭按钮只关闭大图", async () => {
    const onParentClose = renderInParent(entry({ posterUrl: POSTER }));
    fireEvent.click(await screen.findByRole("button", { name: t("config.viewPoster") }));
    const large = await screen.findByRole("img", { name: "测试番剧" });
    const nested = large.closest("[role='dialog']") as HTMLElement;
    fireEvent.click(within(nested).getByRole("button", { name: t("ui.close") }));
    await waitFor(() => expect(screen.queryByRole("img", { name: "测试番剧" })).toBeNull());
    expect(onParentClose).not.toHaveBeenCalled();
  });

  it("没有图片源时不渲染缩略图和按钮", () => {
    renderInParent(entry());
    expect(screen.queryByRole("button", { name: t("config.viewPoster") })).toBeNull();
    expect(document.querySelector(".anime-config-head-poster")).toBeNull();
  });

  it("第三方海报 URL 不加载，也不渲染按钮", () => {
    renderInParent(entry({ posterUrl: "https://image.tmdb.org/t/p/w342/p.jpg" }));
    expect(screen.queryByRole("button", { name: t("config.viewPoster") })).toBeNull();
  });
});
