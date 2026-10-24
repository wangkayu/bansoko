import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiError, toUserMessage } from "../lib/api/client";
import { t } from "../lib/i18n";
import { StateBlock } from "./StateBlock";

describe("StateBlock", () => {
  it("错误态带有已登记错误的“详情”入口，原文默认折叠", () => {
    const title = toUserMessage(
      new ApiError({ status: 500, code: "INTERNAL", message: "raw backend message", requestId: "req-1" }),
      "fallback",
    );
    render(<StateBlock kind="error" title={title} />);
    expect(screen.getByRole("alert")).toHaveTextContent(t("error.INTERNAL"));
    expect(screen.queryByText(/raw backend message/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.toggle") }));
    expect(screen.getByText(/raw backend message/)).toBeInTheDocument();
    expect(screen.getByText(/req-1/)).toBeInTheDocument();
  });

  it("没有登记详情的错误文案不显示“详情”入口", () => {
    render(<StateBlock kind="error" title="自定义错误文案" />);
    expect(screen.queryByRole("button", { name: t("errorDetails.toggle") })).toBeNull();
  });

  it("四类各有自己的语义：busy 是 status，error 是 alert，empty/restricted 带 CTA 时渲染动作", () => {
    const { rerender } = render(<StateBlock kind="busy" title="正在读取" />);
    expect(screen.getByRole("status")).toHaveTextContent("正在读取");
    expect(screen.queryByRole("alert")).toBeNull();

    rerender(<StateBlock kind="empty" title="还没有连接" description="添加后可用" action={<button type="button">添加</button>} />);
    expect(screen.getByText("还没有连接")).toBeInTheDocument();
    expect(screen.getByText("添加后可用")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "添加" })).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();

    rerender(<StateBlock kind="restricted" title="本页无法直接升级" />);
    const restricted = screen.getByText("本页无法直接升级").closest("[data-state-block]");
    expect(restricted).toHaveAttribute("data-state-block", "restricted");
    expect(screen.queryByRole("alert")).toBeNull();

    rerender(<StateBlock kind="error" title="读取失败" action={<button type="button">重试</button>} />);
    expect(screen.getByRole("alert")).toHaveTextContent("读取失败");
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });
});
