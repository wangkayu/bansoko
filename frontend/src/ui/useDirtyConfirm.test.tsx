import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useDirtyConfirm } from "./useDirtyConfirm";

function Harness({
  dirty,
  onClose,
  title,
  description,
  confirmLabel,
}: {
  dirty: boolean | (() => boolean);
  onClose: () => void;
  title?: string;
  description?: string;
  confirmLabel?: string;
}) {
  const { requestClose, confirmDialog } = useDirtyConfirm({
    dirty,
    onClose,
    title,
    description,
    confirmLabel,
  });
  return (
    <>
      <button onClick={requestClose}>请求关闭</button>
      {confirmDialog}
    </>
  );
}

describe("useDirtyConfirm", () => {
  it("非 dirty 时直接关闭，不弹确认", () => {
    const onClose = vi.fn();
    render(<Harness dirty={false} onClose={onClose} />);
    fireEvent.click(screen.getByText("请求关闭"));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("dirty 时先弹「放弃未保存的修改？」，确认后才关闭", () => {
    const onClose = vi.fn();
    render(<Harness dirty onClose={onClose} description="当前草稿只存在于对话框内存中。" />);
    fireEvent.click(screen.getByText("请求关闭"));
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog")).toHaveTextContent("放弃未保存的修改？");
    expect(screen.getByRole("alertdialog")).toHaveTextContent("当前草稿只存在于对话框内存中。");
    fireEvent.click(screen.getByRole("button", { name: "放弃修改" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("取消确认框则不关闭", () => {
    const onClose = vi.fn();
    render(<Harness dirty onClose={onClose} />);
    fireEvent.click(screen.getByText("请求关闭"));
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("dirty getter 在请求关闭时读取最新值", () => {
    const onClose = vi.fn();
    let current = true;
    render(<Harness dirty={() => current} onClose={onClose} />);
    fireEvent.click(screen.getByText("请求关闭"));
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    current = false;
    fireEvent.click(screen.getByText("请求关闭"));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("标题与确认文案可配", () => {
    const onClose = vi.fn();
    render(
      <Harness dirty onClose={onClose} title="放弃未保存的选择？" confirmLabel="放弃并关闭" />,
    );
    fireEvent.click(screen.getByText("请求关闭"));
    expect(screen.getByRole("alertdialog")).toHaveTextContent("放弃未保存的选择？");
    fireEvent.click(screen.getByRole("button", { name: "放弃并关闭" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
