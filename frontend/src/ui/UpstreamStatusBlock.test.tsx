import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ApiError } from "../lib/api/client";
import { t } from "../lib/i18n";
import { nonErrorUpstream, UpstreamStatusBlock } from "./UpstreamStatusBlock";

afterEach(() => vi.useRealTimers());

it("counts down from local receipt despite server clock skew and allows early retry", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  const onRetry = vi.fn();
  const quota = new ApiError({
    status: 429,
    message: "quota",
    upstream: {
      state: "cooldown",
      checked_at: "2025-12-31T23:00:00Z",
      cooldown_until: "2025-12-31T23:00:05Z",
      retry_after_seconds: 5,
    },
  });
  const status = nonErrorUpstream(quota);
  act(() => vi.advanceTimersByTime(1000));
  const wait = (n: number) => t("upstream.waitSeconds", { n });
  const { rerender } = render(<UpstreamStatusBlock status={status} onRetry={onRetry} />);
  const retry = screen.getByRole("button", { name: t("upstream.retryLater") });
  expect(screen.getByText(wait(4))).toBeInTheDocument();
  expect(retry).toBeEnabled();
  fireEvent.click(retry);
  expect(onRetry).toHaveBeenCalledOnce();

  rerender(<UpstreamStatusBlock status={status} onRetry={onRetry} retryDisabled />);
  expect(retry).toBeDisabled();
  act(() => vi.advanceTimersByTime(3000));
  expect(screen.getByText(wait(1))).toBeInTheDocument();
  rerender(<UpstreamStatusBlock status={status} onRetry={onRetry} />);
  expect(retry).toBeEnabled();
  act(() => vi.advanceTimersByTime(1000));
  expect(screen.queryByText(wait(1))).not.toBeInTheDocument();
  expect(retry).toBeEnabled();
});

it("immediately omits an expired absolute deadline on an already-mounted block", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  const onRetry = vi.fn();
  const { rerender } = render(<UpstreamStatusBlock onRetry={onRetry} />);
  vi.setSystemTime(new Date("2026-01-01T00:00:10Z"));
  rerender(<UpstreamStatusBlock onRetry={onRetry} status={{
    state: "cooldown",
    checked_at: "2026-01-01T00:00:01Z",
    cooldown_until: "2026-01-01T00:00:09Z",
  }} />);
  expect(screen.getByText(t("upstream.cooldown"))).toBeInTheDocument();
  expect(screen.queryByText(t("upstream.waitSeconds", { n: 9 }))).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: t("upstream.retryLater") })).toBeEnabled();
});
