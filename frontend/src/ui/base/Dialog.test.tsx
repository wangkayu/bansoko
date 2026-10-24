import { useRef, useState } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Dialog, type DialogTier } from "./Dialog";
import { Select } from "./controls";

// Dialog tier contract: S/M/L/F geometry hooks, stacking tiers (1000 / 1100 /
// 1200, popups +10), close requests, focus return, content vs fixed M height.

function zIndexOf(element: Element | null): string {
  return element instanceof HTMLElement ? element.style.zIndex : "";
}

function Opener({ tier = "M", nested = false }: { tier?: DialogTier; nested?: boolean }) {
  const [open, setOpen] = useState(false);
  const [childOpen, setChildOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        打开
      </button>
      <Dialog open={open} onRequestClose={() => setOpen(false)} tier={tier} title="外层">
        <p>外层内容</p>
        {nested ? (
          <>
            <button type="button" onClick={() => setChildOpen(true)}>
              打开二级
            </button>
            <Dialog open={childOpen} onRequestClose={() => setChildOpen(false)} tier="S" title="二级">
              <p>二级内容</p>
            </Dialog>
          </>
        ) : null}
      </Dialog>
    </>
  );
}

describe("Dialog", () => {
  it("渲染标题、描述、footer，并标记档位", async () => {
    render(
      <Dialog open onRequestClose={() => {}} tier="L" title="标题" description="说明" footer={<button type="button">保存</button>}>
        <p>正文</p>
      </Dialog>,
    );
    const dialog = await screen.findByRole("dialog", { name: "标题" });
    expect(dialog).toHaveAttribute("data-tier", "L");
    expect(dialog).toHaveAccessibleDescription("说明");
    expect(screen.getByText("正文")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存" }).closest("footer")).not.toBeNull();
  });

  it("bodyLayout=fill：内容区不滚动、无内边距，由内容自己布局", async () => {
    render(
      <>
        <Dialog open onRequestClose={() => {}} title="默认">
          <p>默认正文</p>
        </Dialog>
      </>,
    );
    const scrollBody = screen.getByText("默认正文").parentElement;
    expect(scrollBody).toHaveClass("tw:overflow-y-auto", "tw:px-6");
    render(
      <Dialog open onRequestClose={() => {}} title="填充" bodyLayout="fill">
        <p>填充正文</p>
      </Dialog>,
    );
    const fillBody = screen.getByText("填充正文").parentElement;
    expect(fillBody).toHaveClass("tw:flex", "tw:flex-col", "tw:overflow-hidden");
    expect(fillBody).not.toHaveClass("tw:overflow-y-auto");
    expect(fillBody).not.toHaveClass("tw:px-6");
  });

  it("blocking 的 S 档不渲染拖动条", async () => {
    render(
      <Dialog open blocking tier="S" onRequestClose={() => {}} title="阻断">
        <p>正文</p>
      </Dialog>,
    );
    const dialog = await screen.findByRole("alertdialog", { name: "阻断" });
    expect(dialog.firstElementChild?.tagName).toBe("HEADER");
    expect(dialog.querySelector("[data-drag-handle]")).toBeNull();
  });

  it.each(["S", "M", "L", "F"] as const)("tier=%s 只有 S 档渲染手机底部面板拖动条", async (tier) => {
    render(
      <Dialog open onRequestClose={() => {}} tier={tier} title="档位">
        <p>正文</p>
      </Dialog>,
    );
    const dialog = await screen.findByRole("dialog", { name: "档位" });
    const handle = dialog.firstElementChild;
    if (tier === "S") {
      expect(handle?.tagName).toBe("DIV");
      expect(handle).toHaveAttribute("aria-hidden", "true");
    } else {
      expect(handle?.tagName).toBe("HEADER");
    }
  });

  it("关闭按钮与 Esc 都只发出关闭请求", async () => {
    const onRequestClose = vi.fn();
    render(
      <Dialog open onRequestClose={onRequestClose} title="请求">
        <p>正文</p>
      </Dialog>,
    );
    await userEvent.setup().click(await screen.findByRole("button", { name: "关闭" }));
    expect(onRequestClose).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(onRequestClose).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("dialog", { name: "请求" })).toBeInTheDocument();
  });

  it("阻断确认为 1200 层级的 alertdialog", async () => {
    render(
      <>
        <Dialog open blocking tier="S" onRequestClose={() => {}} title="确认删除">
          <p>不可撤销</p>
        </Dialog>
      </>,
    );
    expect(zIndexOf(await screen.findByRole("alertdialog", { name: "确认删除" }))).toBe("1200");
  });

  it("阻断确认不显示标题栏关闭 X，普通弹窗显示", async () => {
    const { unmount } = render(
      <Dialog open blocking tier="S" onRequestClose={() => {}} title="阻断">
        <p>x</p>
      </Dialog>,
    );
    await screen.findByRole("alertdialog", { name: "阻断" });
    expect(screen.queryByRole("button", { name: "关闭" })).not.toBeInTheDocument();
    unmount();
    render(
      <Dialog open onRequestClose={() => {}} title="普通">
        <p>x</p>
      </Dialog>,
    );
    expect(await screen.findByRole("button", { name: "关闭" })).toBeInTheDocument();
  });

  it("点遮罩：普通弹窗发出关闭请求，blocking 默认不关闭但 Esc 仍关闭，dismissOnOverlay 可覆盖", async () => {
    const user = userEvent.setup();
    const backdropOf = () => document.querySelector('[class*="bg-overlay"]') as HTMLElement;
    const normal = vi.fn();
    const { unmount } = render(
      <Dialog open onRequestClose={normal} title="普通">
        <p>x</p>
      </Dialog>,
    );
    await screen.findByRole("dialog", { name: "普通" });
    await user.click(backdropOf());
    expect(normal).toHaveBeenCalledTimes(1);
    unmount();

    const blocking = vi.fn();
    const second = render(
      <Dialog open blocking tier="S" onRequestClose={blocking} title="阻断">
        <button type="button">内部</button>
      </Dialog>,
    );
    await screen.findByRole("alertdialog", { name: "阻断" });
    await user.click(backdropOf());
    expect(blocking).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(blocking).toHaveBeenCalledTimes(1);
    second.unmount();

    const forced = vi.fn();
    render(
      <Dialog open blocking dismissOnOverlay tier="S" onRequestClose={forced} title="可点遮罩">
        <p>x</p>
      </Dialog>,
    );
    await screen.findByRole("alertdialog", { name: "可点遮罩" });
    await user.click(backdropOf());
    expect(forced).toHaveBeenCalledTimes(1);
  });

  it("initialFocus 指定打开后的初始焦点", async () => {
    function Focused() {
      const ref = useRef<HTMLButtonElement>(null);
      return (
        <Dialog open onRequestClose={() => {}} initialFocus={ref} title="焦点">
          <button type="button">第一个</button>
          <button type="button" ref={ref}>
            目标
          </button>
        </Dialog>
      );
    }
    render(<Focused />);
    await screen.findByRole("dialog", { name: "焦点" });
    await waitFor(() => expect(screen.getByRole("button", { name: "目标" })).toHaveFocus());
  });

  it("层级：顶层 1000，二级 1100", async () => {
    const user = userEvent.setup();
    render(<Opener nested />);
    await user.click(screen.getByRole("button", { name: "打开" }));
    expect(zIndexOf(await screen.findByRole("dialog", { name: "外层" }))).toBe("1000");
    await user.click(await screen.findByRole("button", { name: "打开二级" }));
    const child = await screen.findByRole("dialog", { name: "二级" });
    expect(zIndexOf(child)).toBe("1100");
    // The outer popup is aria-hidden behind the child now; read it by tier.
    expect(zIndexOf(document.querySelector('[data-tier="M"]'))).toBe("1000");
  });

  it("弹窗内的下拉浮层比所在弹窗高 10", async () => {
    const user = userEvent.setup();
    render(
      <Dialog open onRequestClose={() => {}} title="含下拉">
        <Select label="结果" value="all" options={[{ value: "all", label: "全部" }]} onChange={() => {}} />
      </Dialog>,
    );
    await user.click(await screen.findByRole("combobox", { name: "结果" }));
    const listbox = await screen.findByRole("listbox");
    // Positioner is the nearest ancestor carrying the inline z-index.
    let node: HTMLElement | null = listbox;
    while (node && node.style.zIndex === "") node = node.parentElement;
    expect(zIndexOf(node)).toBe("1010");
  });

  it("关闭后焦点回到打开它的按钮", async () => {
    const user = userEvent.setup();
    render(<Opener />);
    const opener = screen.getByRole("button", { name: "打开" });
    await user.click(opener);
    await screen.findByRole("dialog", { name: "外层" });
    await user.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "外层" })).not.toBeInTheDocument());
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it("二级弹窗关闭后焦点回到二级的触发按钮", async () => {
    const user = userEvent.setup();
    render(<Opener nested />);
    await user.click(screen.getByRole("button", { name: "打开" }));
    const childOpener = await screen.findByRole("button", { name: "打开二级" });
    await user.click(childOpener);
    await screen.findByRole("dialog", { name: "二级" });
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "二级" })).not.toBeInTheDocument());
    expect(screen.getByRole("dialog", { name: "外层" })).toBeInTheDocument();
    await waitFor(() => expect(childOpener).toHaveFocus());
  });

  // Touch taps (and Safari clicks) press a button without focusing it, so the
  // focused element at open time is the body or the parent dialog. Simulate
  // that with pointerdown + click and no focus change.
  function tap(element: HTMLElement) {
    fireEvent.pointerDown(element, { pointerType: "touch" });
    fireEvent.click(element);
  }

  it("触屏：点按打开（按钮未获得焦点）后关闭，焦点回到触发按钮", async () => {
    render(<Opener />);
    const opener = screen.getByRole("button", { name: "打开" });
    tap(opener);
    expect(opener).not.toHaveFocus();
    await screen.findByRole("dialog", { name: "外层" });
    tap(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "外层" })).not.toBeInTheDocument());
    await waitFor(() => expect(opener).toHaveFocus());
  });

  it("触屏：嵌套弹窗关闭后焦点回到二级的触发按钮，外层保持打开", async () => {
    render(<Opener nested />);
    tap(screen.getByRole("button", { name: "打开" }));
    const childOpener = await screen.findByRole("button", { name: "打开二级" });
    tap(childOpener);
    const child = await screen.findByRole("dialog", { name: "二级" });
    tap(within(child).getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "二级" })).not.toBeInTheDocument());
    expect(screen.getByRole("dialog", { name: "外层" })).toBeInTheDocument();
    await waitFor(() => expect(childOpener).toHaveFocus());
  });

  it("S 档与 M 档按内容高度（只设上限），L 档固定高度", async () => {
    const { rerender } = render(
      <Dialog open onRequestClose={() => {}} tier="M" title="高度">
        <p>短内容</p>
      </Dialog>,
    );
    const m = await screen.findByRole("dialog", { name: "高度" });
    expect(m.className).toContain("tw:max-h-[min(640px,calc(100dvh-2rem))]");
    expect(m.className).not.toMatch(/(^|\s)tw:h-\[/);
    expect(m).toHaveClass("tw:phone:h-dvh", "tw:phone:max-h-none");
    rerender(
      <Dialog open onRequestClose={() => {}} tier="S" title="高度">
        <p>短内容</p>
      </Dialog>,
    );
    const sDialog = await screen.findByRole("dialog", { name: "高度" });
    expect(sDialog.className).not.toMatch(/(^|\s)tw:h-\[/);
    rerender(
      <Dialog open onRequestClose={() => {}} tier="L" title="高度">
        <p>短内容</p>
      </Dialog>,
    );
    const l = await screen.findByRole("dialog", { name: "高度" });
    expect(l.className).toContain("tw:h-[min(760px,calc(100dvh-2rem))]");
  });

  it("R 档是右侧抽屉：桌面贴右缘全高、760px 上限，手机全屏", async () => {
    render(
      <Dialog open onRequestClose={() => {}} tier="R" title="抽屉">
        <p>内容</p>
      </Dialog>,
    );
    const sheet = await screen.findByRole("dialog", { name: "抽屉" });
    expect(sheet.className).toContain("tw:right-0");
    expect(sheet.className).toContain("tw:h-dvh");
    expect(sheet.className).toContain("tw:w-[min(760px,100vw)]");
    expect(sheet.className).toContain("tw:data-starting-style:translate-x-6");
    expect(sheet).toHaveClass("tw:phone:inset-0", "tw:phone:w-full", "tw:phone:rounded-none", "tw:phone:border-0");
  });

  it("触发元素已卸载时，returnFocus 指定关闭后的焦点", async () => {
    function MenuLikeOpener() {
      const fallback = useRef<HTMLButtonElement>(null);
      const [menuOpen, setMenuOpen] = useState(true);
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" ref={fallback}>
            菜单
          </button>
          {menuOpen ? (
            <button
              type="button"
              onClick={() => {
                setMenuOpen(false);
                setOpen(true);
              }}
            >
              菜单项
            </button>
          ) : null}
          <Dialog open={open} onRequestClose={() => setOpen(false)} returnFocus={fallback} title="来自菜单">
            <p>正文</p>
          </Dialog>
        </>
      );
    }
    const user = userEvent.setup();
    render(<MenuLikeOpener />);
    await user.click(screen.getByRole("button", { name: "菜单项" }));
    await screen.findByRole("dialog", { name: "来自菜单" });
    await user.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "菜单" })).toHaveFocus());
  });

  it("fixedHeight：M 档固定在上限高度而不是按内容伸缩，手机仍全屏", async () => {
    const { rerender } = render(
      <Dialog open onRequestClose={() => {}} tier="M" title="按内容">
        <p>正文</p>
      </Dialog>,
    );
    const content = await screen.findByRole("dialog", { name: "按内容" });
    expect(content.className).toContain("tw:max-h-[min(640px,calc(100dvh-2rem))]");
    expect(content.className).not.toContain("tw:h-[min(640px");
    rerender(
      <Dialog open fixedHeight onRequestClose={() => {}} tier="M" title="按内容">
        <p>正文</p>
      </Dialog>,
    );
    const fixed = await screen.findByRole("dialog", { name: "按内容" });
    expect(fixed.className).toContain("tw:h-[min(640px,calc(100dvh-2rem))]");
    expect(fixed.className).not.toContain("tw:max-h-[min(640px");
    expect(fixed.className).toContain("tw:phone:h-dvh");
  });
});

// Drag-to-dismiss on the phone bottom sheet. jsdom has no layout or media
// queries: matchMedia and performance.now are stubbed per test.
describe("Dialog 手机底部面板拖动关闭", () => {
  let now = 0;
  function stubMedia({ phone = true, reducedMotion = false } = {}) {
    vi.stubGlobal(
      "matchMedia",
      vi.fn((query: string) => ({
        matches: (query === "(width <= 600px)" && phone) || (query === "(prefers-reduced-motion: reduce)" && reducedMotion),
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      })),
    );
    vi.spyOn(performance, "now").mockImplementation(() => now);
  }

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  function drag(target: Element, moves: { y: number; at: number }[]) {
    now = 0;
    fireEvent.pointerDown(target, { pointerId: 1, clientY: 100, button: 0, pointerType: "touch" });
    for (const move of moves.slice(0, -1)) {
      now = move.at;
      fireEvent.pointerMove(target, { pointerId: 1, clientY: move.y, pointerType: "touch" });
    }
    const last = moves[moves.length - 1]!;
    now = last.at;
    fireEvent.pointerMove(target, { pointerId: 1, clientY: last.y, pointerType: "touch" });
    fireEvent.pointerUp(target, { pointerId: 1, clientY: last.y, pointerType: "touch" });
  }

  async function renderSheet(onRequestClose = vi.fn(), blocking = false) {
    render(
      <Dialog open tier="S" blocking={blocking} onRequestClose={onRequestClose} title="面板">
        <p>正文</p>
      </Dialog>,
    );
    const dialog = await screen.findByRole(blocking ? "alertdialog" : "dialog", { name: "面板" });
    return { dialog, onRequestClose };
  }

  it("拖过阈值松手即请求关闭，拖动中跟随手指", async () => {
    stubMedia();
    const { dialog, onRequestClose } = await renderSheet();
    const handle = dialog.querySelector("[data-drag-handle]")!;
    now = 0;
    fireEvent.pointerDown(handle, { pointerId: 1, clientY: 100, button: 0, pointerType: "touch" });
    now = 300;
    fireEvent.pointerMove(handle, { pointerId: 1, clientY: 180, pointerType: "touch" });
    expect(dialog.style.transform).toBe("translateY(80px)");
    now = 600;
    fireEvent.pointerMove(handle, { pointerId: 1, clientY: 260, pointerType: "touch" });
    fireEvent.pointerUp(handle, { pointerId: 1, clientY: 260, pointerType: "touch" });
    expect(onRequestClose).toHaveBeenCalledTimes(1);
  });

  it("慢速小距离拖动回弹，不关闭", async () => {
    stubMedia();
    const { dialog, onRequestClose } = await renderSheet();
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    drag(dialog.querySelector("[data-drag-handle]")!, [
      { y: 120, at: 400 },
      { y: 140, at: 800 },
    ]);
    expect(onRequestClose).not.toHaveBeenCalled();
    expect(dialog.style.transform).toBe("");
    expect(dialog.style.transition).toContain("transform");
    vi.advanceTimersByTime(300);
    expect(dialog.style.transition).toBe("");
  });

  it("快速下滑（速度超过阈值）即请求关闭；从标题栏开始拖同样生效", async () => {
    stubMedia();
    const { dialog, onRequestClose } = await renderSheet();
    drag(dialog.querySelector("header")!, [
      { y: 110, at: 10 },
      { y: 140, at: 30 },
    ]);
    expect(onRequestClose).toHaveBeenCalledTimes(1);
  });

  it("从标题栏的按钮上开始不拖动", async () => {
    stubMedia();
    const { dialog, onRequestClose } = await renderSheet();
    drag(within(dialog).getByRole("button", { name: "关闭" }), [{ y: 400, at: 300 }]);
    expect(onRequestClose).not.toHaveBeenCalled();
    expect(dialog.style.transform).toBe("");
  });

  it("调用方拒绝关闭（如脏数据确认）时面板回弹", async () => {
    stubMedia();
    const { dialog, onRequestClose } = await renderSheet();
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    drag(dialog.querySelector("[data-drag-handle]")!, [{ y: 400, at: 300 }]);
    expect(onRequestClose).toHaveBeenCalledTimes(1);
    expect(dialog.style.transform).toBe("translateY(300px)");
    vi.advanceTimersByTime(1);
    expect(dialog.style.transform).toBe("");
  });

  it("prefers-reduced-motion：回弹不做过渡动画", async () => {
    stubMedia({ reducedMotion: true });
    const { dialog } = await renderSheet();
    drag(dialog.querySelector("[data-drag-handle]")!, [{ y: 130, at: 500 }]);
    expect(dialog.style.transform).toBe("");
    expect(dialog.style.transition).toBe("");
  });

  it("桌面断点不拖动", async () => {
    stubMedia({ phone: false });
    const { dialog, onRequestClose } = await renderSheet();
    drag(dialog.querySelector("header")!, [{ y: 400, at: 300 }]);
    expect(onRequestClose).not.toHaveBeenCalled();
    expect(dialog.style.transform).toBe("");
  });

  it("blocking 面板不可拖动", async () => {
    stubMedia();
    const { dialog, onRequestClose } = await renderSheet(vi.fn(), true);
    drag(dialog.querySelector("header")!, [{ y: 400, at: 300 }]);
    expect(onRequestClose).not.toHaveBeenCalled();
    expect(dialog.style.transform).toBe("");
  });
});
