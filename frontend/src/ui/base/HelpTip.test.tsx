import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { LAYER } from "./layers";
import { HelpTip } from "./HelpTip";

describe("HelpTip", () => {
  it("默认只显示“?”按钮，点击后显示说明", async () => {
    render(<HelpTip label="说明：示例">详细说明</HelpTip>);
    expect(screen.queryByText("详细说明")).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "说明：示例" }));
    expect(await screen.findByText("详细说明")).toBeInTheDocument();
  });

  it("键盘可以打开，浮层高于页面层", async () => {
    render(<HelpTip label="说明：示例">键盘说明</HelpTip>);
    const user = userEvent.setup();
    await user.tab();
    expect(screen.getByRole("button", { name: "说明：示例" })).toHaveFocus();
    await user.keyboard("{Enter}");
    const content = await screen.findByText("键盘说明");
    let node: HTMLElement | null = content;
    while (node && node.style.zIndex === "") node = node.parentElement;
    expect(node?.style.zIndex).toBe(String(LAYER.page + 10));
  });
});
