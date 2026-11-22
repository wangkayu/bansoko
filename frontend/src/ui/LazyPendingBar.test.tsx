import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LazyPendingBar } from "./LazyPendingBar";
import { t } from "../lib/i18n";

describe("LazyPendingBar", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("立即提供无障碍状态文案，120ms 后才显示顶部进度条", () => {
    render(<LazyPendingBar label="正在打开设置" />);
    expect(screen.getByRole("status")).toHaveTextContent("正在打开设置");
    expect(screen.queryByTestId("lazy-pending-bar")).not.toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(119);
    });
    expect(screen.queryByTestId("lazy-pending-bar")).not.toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(screen.getByTestId("lazy-pending-bar")).toBeInTheDocument();
  });

  it("未传 label 时回退到默认加载文案", () => {
    render(<LazyPendingBar />);
    expect(screen.getByRole("status")).toHaveTextContent(t("nav.stageLoading"));
  });
});
