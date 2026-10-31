import { render, screen } from "@testing-library/react";
import { Component, Suspense, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { lazyEntry } from "./lazyEntry";

// lazyEntry: a lazily loaded entry renders once its chunk resolves; a stale chunk (the reload
// handler prevented the preload error, so Vite resolves undefined) keeps the fallback instead of
// rejecting into the error boundary; a real failure still reaches the boundary.

function Entry() {
  return <div>entry</div>;
}

class Boundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? <div>failed</div> : this.props.children;
  }
}

function renderEntry(load: () => Promise<{ Entry: typeof Entry }>) {
  const Lazy = lazyEntry(load, (module) => module.Entry);
  render(
    <Boundary>
      <Suspense fallback={<div>loading</div>}>
        <Lazy />
      </Suspense>
    </Boundary>,
  );
}

describe("lazyEntry", () => {
  it("模块就绪后渲染条目", async () => {
    renderEntry(async () => ({ Entry }));
    expect(await screen.findByText("entry")).toBeInTheDocument();
  });

  it("陈旧 chunk 解析为 undefined 时停留在加载态，不落到错误边界", async () => {
    renderEntry((async () => undefined) as unknown as () => Promise<{ Entry: typeof Entry }>);
    expect(await screen.findByText("loading")).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByText("loading")).toBeInTheDocument();
    expect(screen.queryByText("failed")).not.toBeInTheDocument();
  });

  it("真正的加载失败仍交给错误边界", async () => {
    // React logs the boundary-caught error; the assertion below is what matters here.
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      renderEntry(async () => {
        throw new Error("chunk failed");
      });
      expect(await screen.findByText("failed")).toBeInTheDocument();
    } finally {
      consoleError.mockRestore();
    }
  });
});
