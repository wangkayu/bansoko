import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Alert, Progress } from "./feedback";

// Alert announces role="alert" for every tone (existing tests query it) and
// carries the tone via stroke + icon; Progress exposes a labelled progressbar.

describe("Alert", () => {
  it.each([
    ["error", "tw:border-offline/40"],
    ["warning", "tw:border-warning/40"],
    ["info", "tw:border-primary/40"],
    ["success", "tw:border-online/40"],
  ] as const)("tone=%s 渲染 role=alert、细描边与图标", (tone, borderClass) => {
    render(<Alert tone={tone} title="连接失败" />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("连接失败");
    expect(alert).toHaveAttribute("data-tone", tone);
    expect(alert).toHaveClass(borderClass);
    expect(alert.querySelector("svg[aria-hidden='true']")).not.toBeNull();
  });

  it("description 与 action 插槽", () => {
    render(<Alert tone="warning" title="需要确认" description="保存后立即生效" action={<button type="button">重试</button>} />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("需要确认");
    expect(alert).toHaveTextContent("保存后立即生效");
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });
});

describe("Progress", () => {
  it("带名称的 progressbar，显示百分比", () => {
    render(<Progress value={42} label="下载进度" />);
    const bar = screen.getByRole("progressbar", { name: "下载进度" });
    expect(bar).toHaveAttribute("aria-valuenow", "42");
    expect(bar).toHaveTextContent("42%");
  });

  it("超出范围的值被截断到 0–100；showValue=false 不显示文字", () => {
    render(<Progress value={180} label="完成" showValue={false} />);
    const bar = screen.getByRole("progressbar", { name: "完成" });
    expect(bar).toHaveAttribute("aria-valuenow", "100");
    expect(bar).not.toHaveTextContent("%");
  });
});
