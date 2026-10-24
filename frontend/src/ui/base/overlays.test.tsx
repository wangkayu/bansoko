import { useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { LAYER, LayerContext } from "./layers";
import { DropdownMenu, Popconfirm, Popover, Tooltip } from "./overlays";

// Floating overlays: open from their trigger element, stack POPUP_OFFSET above
// the containing layer (page 50 → 60, top dialog 1000 → 1010), and report
// selections back to the caller.

function stackedZIndex(element: HTMLElement): string {
  let node: HTMLElement | null = element;
  while (node && node.style.zIndex === "") node = node.parentElement;
  return node?.style.zIndex ?? "";
}

describe("Popover", () => {
  it("点击触发元素打开，显示标题与内容；页面层浮层为 60", async () => {
    render(
      <Popover trigger={<button type="button">测试</button>} title="选择测试事件">
        <p>下载已提交</p>
      </Popover>,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "测试" }));
    const content = await screen.findByText("下载已提交");
    expect(screen.getByText("选择测试事件")).toBeInTheDocument();
    expect(stackedZIndex(content)).toBe(String(LAYER.page + 10));
  });

  it("受控 open：内容里的操作可以关闭浮层", async () => {
    function Harness() {
      const [open, setOpen] = useState(false);
      return (
        <Popover open={open} onOpenChange={setOpen} trigger={<button type="button">打开</button>}>
          <button type="button" onClick={() => setOpen(false)}>
            选项
          </button>
        </Popover>
      );
    }
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("button", { name: "打开" }));
    await user.click(await screen.findByRole("button", { name: "选项" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "选项" })).not.toBeInTheDocument());
  });
});

describe("DropdownMenu", () => {
  it("菜单项回调 onSelect，单选组回调新值，浮层叠在所在弹窗之上", async () => {
    const onLogout = vi.fn();
    const onTheme = vi.fn();
    const user = userEvent.setup();
    render(
      <LayerContext.Provider value={LAYER.top}>
        <DropdownMenu
          trigger={<button type="button" aria-label="账户" />}
          items={[
            {
              type: "radio",
              key: "theme",
              value: "light",
              onValueChange: onTheme,
              options: [
                { value: "light", label: "浅色" },
                { value: "dark", label: "深色" },
              ],
            },
            { type: "divider", key: "divider" },
            { key: "logout", label: "退出登录", danger: true, onSelect: onLogout },
          ]}
        />
      </LayerContext.Provider>,
    );
    await user.click(screen.getByRole("button", { name: "账户" }));
    const menu = await screen.findByRole("menu");
    expect(stackedZIndex(menu)).toBe(String(LAYER.top + 10));
    expect(screen.getByRole("menuitemradio", { name: "浅色" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("separator")).toBeInTheDocument();

    await user.click(screen.getByRole("menuitemradio", { name: "深色" }));
    expect(onTheme).toHaveBeenCalledWith("dark");

    await user.click(screen.getByRole("button", { name: "账户" }));
    await user.click(await screen.findByRole("menuitem", { name: "退出登录" }));
    expect(onLogout).toHaveBeenCalledTimes(1);
  });
});

describe("DropdownMenu submenu", () => {
  it("子菜单随触发项展开，选中子项回调并更高层级不被遮挡", async () => {
    const onTheme = vi.fn();
    const user = userEvent.setup();
    render(
      <LayerContext.Provider value={LAYER.top}>
        <DropdownMenu
          trigger={<button type="button" aria-label="账户" />}
          items={[
            {
              type: "submenu",
              key: "theme",
              label: "外观",
              items: [{ type: "radio", key: "r", value: "light", onValueChange: onTheme, options: [{ value: "light", label: "浅色" }, { value: "dark", label: "深色" }] }],
            },
          ]}
        />
      </LayerContext.Provider>,
    );
    await user.click(screen.getByRole("button", { name: "账户" }));
    await user.click(await screen.findByRole("menuitem", { name: "外观" }));
    const dark = await screen.findByRole("menuitemradio", { name: "深色" });
    expect(stackedZIndex(dark)).toBe(String(LAYER.top + 10));
    fireEvent.click(dark);
    expect(onTheme).toHaveBeenCalledWith("dark");
  });
});

describe("Tooltip", () => {
  it("悬停触发元素后显示提示", async () => {
    render(
      <Tooltip content="复制路径" delay={0}>
        <button type="button">复制</button>
      </Tooltip>,
    );
    await userEvent.setup().hover(screen.getByRole("button", { name: "复制" }));
    expect(await screen.findByText("复制路径")).toBeInTheDocument();
  });
});

describe("Popconfirm", () => {
  function Harness({ onConfirm }: { onConfirm: () => void }) {
    const [open, setOpen] = useState(false);
    return (
      <Popconfirm
        open={open}
        onOpenChange={setOpen}
        title="删除目标？"
        description="历史记录会保留"
        confirmLabel="删除"
        danger
        onConfirm={() => {
          onConfirm();
          setOpen(false);
        }}
        trigger={<button type="button">删除目标</button>}
      />
    );
  }

  it("点击触发元素展开，确认回调后关闭；取消只关闭", async () => {
    const onConfirm = vi.fn();
    render(<Harness onConfirm={onConfirm} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "删除目标" }));
    expect(await screen.findByText("删除目标？")).toBeInTheDocument();
    expect(screen.getByText("历史记录会保留")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByText("删除目标？")).not.toBeInTheDocument());
    expect(onConfirm).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "删除目标" }));
    await user.click(await screen.findByRole("button", { name: "删除" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByText("删除目标？")).not.toBeInTheDocument());
  });

  it("Esc 关闭", async () => {
    render(<Harness onConfirm={() => {}} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "删除目标" }));
    await screen.findByText("删除目标？");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByText("删除目标？")).not.toBeInTheDocument());
  });
});
