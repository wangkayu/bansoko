import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ConfirmDialog } from "./ConfirmDialog";

describe("ConfirmDialog", () => {
  it("danger 时初始焦点在取消，非 danger 时在主按钮；无标题栏关闭 X", async () => {
    const { unmount } = render(
      <ConfirmDialog open onOpenChange={() => {}} title="删除" danger confirmLabel="删除" onConfirm={() => {}} />,
    );
    await screen.findByRole("alertdialog", { name: "删除" });
    await waitFor(() => expect(screen.getByRole("button", { name: "取消" })).toHaveFocus());
    expect(screen.queryByRole("button", { name: "关闭" })).not.toBeInTheDocument();
    unmount();
    render(<ConfirmDialog open onOpenChange={() => {}} title="继续" confirmLabel="继续" onConfirm={() => {}} />);
    await screen.findByRole("alertdialog", { name: "继续" });
    await waitFor(() => expect(screen.getByRole("button", { name: "继续" })).toHaveFocus());
  });

  it("点遮罩不关闭，Esc 与取消关闭", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    render(<ConfirmDialog open onOpenChange={onOpenChange} title="确认" onConfirm={() => {}} />);
    const dialog = await screen.findByRole("alertdialog", { name: "确认" });
    await user.click(document.querySelector('[class*="bg-overlay"]') as HTMLElement);
    expect(onOpenChange).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(onOpenChange).toHaveBeenLastCalledWith(false);
    onOpenChange.mockClear();
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
