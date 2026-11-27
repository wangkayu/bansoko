import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { t } from "../lib/i18n";
import { UpstreamStatusBlock } from "./UpstreamStatusBlock";

afterEach(() => vi.useRealTimers());

it("enables retry when an already-mounted status receives an expired deadline", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  const { rerender } = render(<UpstreamStatusBlock onRetry={vi.fn()} />);
  vi.setSystemTime(new Date("2026-01-01T00:00:10Z"));
  const status = {
    state: "cooldown" as const,
    checked_at: "2026-01-01T00:00:01Z",
    cooldown_until: "2026-01-01T00:00:09Z",
  };
  rerender(<UpstreamStatusBlock status={status} onRetry={vi.fn()} />);
  const retry = screen.getByRole("button", { name: t("upstream.retryLater") });
  expect(retry).toBeEnabled();
  rerender(<UpstreamStatusBlock status={{ ...status, cooldown_until: "2026-01-01T00:00:12Z" }} onRetry={vi.fn()} />);
  expect(retry).toBeDisabled();
  act(() => vi.advanceTimersByTime(2000));
  expect(retry).toBeEnabled();
  rerender(<UpstreamStatusBlock status={status} onRetry={vi.fn()} retryDisabled />);
  expect(retry).toBeDisabled();
});
