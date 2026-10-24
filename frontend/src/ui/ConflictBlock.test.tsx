import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ConflictBlock } from "./ConflictBlock";

describe("ConflictBlock", () => {
  it("展示服务端当前版本、保留说明与错误详情", () => {
    render(<ConflictBlock currentVersion={7} message="保存规则失败：版本冲突" />);
    expect(screen.getByRole("alert")).toHaveTextContent("内容已被修改，请刷新后重新编辑");
    expect(screen.getByText("保存规则失败：版本冲突")).toBeInTheDocument();
    expect(screen.getByText(/刷新后将载入最新内容/)).toBeInTheDocument();
  });

  it("不传 onRefresh 时不显示刷新按钮", () => {
    render(<ConflictBlock currentVersion={3} />);
    expect(screen.queryByRole("button", { name: "刷新载入最新" })).not.toBeInTheDocument();
  });

  it("点击「刷新载入最新」触发 onRefresh", () => {
    const onRefresh = vi.fn();
    render(<ConflictBlock currentVersion={3} onRefresh={onRefresh} />);
    fireEvent.click(screen.getByRole("button", { name: "刷新载入最新" }));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });
});
