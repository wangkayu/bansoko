import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../lib/i18n";
import { ErrorBoundary } from "./ErrorBoundary";

// React synchronously retries the failed subtree once before handing the error
// to a boundary, so a one-shot throw gets swallowed by that retry. Bombs here
// throw persistently until the external switch is flipped.

function AlwaysBomb(): never {
  throw new Error("boom");
}

let shouldThrow = true;

function SwitchableBomb() {
  if (shouldThrow) throw new Error("boom");
  return <p>已恢复</p>;
}

describe("ErrorBoundary", () => {
  afterEach(() => {
    shouldThrow = true;
    vi.restoreAllMocks();
  });

  it("子树正常时直接渲染 children", () => {
    render(
      <ErrorBoundary>
        <p>一切正常</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText("一切正常")).toBeInTheDocument();
  });

  it("渲染抛错时展示崩溃卡片而不是白屏", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <ErrorBoundary>
        <AlwaysBomb />
      </ErrorBoundary>,
    );
    expect(screen.getByText(t("ui.errorBoundary.title"))).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(t("ui.errorBoundary.hint"));
    // The raw exception text only lives behind the "details" toggle.
    expect(screen.queryByText(/boom/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.toggle") }));
    expect(screen.getByText(/boom/)).toBeInTheDocument();
  });

  it("点击重试后重新渲染子树并可恢复", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <ErrorBoundary>
        <SwitchableBomb />
      </ErrorBoundary>,
    );
    expect(screen.getByText("页面出现异常")).toBeInTheDocument();
    shouldThrow = false;
    fireEvent.click(screen.getByRole("button", { name: t("ui.errorBoundary.retry") }));
    expect(screen.getByText("已恢复")).toBeInTheDocument();
    expect(screen.queryByText("页面出现异常")).not.toBeInTheDocument();
  });
});
