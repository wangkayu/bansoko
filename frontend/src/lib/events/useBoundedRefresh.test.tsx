import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useBoundedRefresh } from "./useBoundedRefresh";

const state = vi.hoisted(() => ({ connected: false, refetch: vi.fn() }));
vi.mock("@/lib/events/useRealtimeInvalidation", () => ({ useRealtimeConnected: () => state.connected }));
const client = { refetchQueries: state.refetch };
vi.mock("@tanstack/react-query", () => ({ useQueryClient: () => client }));

function visible(value: boolean) {
  Object.defineProperty(document, "hidden", { configurable: true, value: !value });
  act(() => document.dispatchEvent(new Event("visibilitychange")));
}

describe("bounded refresh visibility", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    state.connected = false;
    state.refetch.mockReset();
    visible(true);
  });
  afterEach(() => {
    vi.useRealTimers();
    Reflect.deleteProperty(document, "hidden");
  });

  it("stops hidden-tab polling and resumes the fallback if reconnect fails", () => {
    const hook = renderHook(() => useBoundedRefresh([["anime", "list"]]));
    act(() => vi.advanceTimersByTime(1000));
    expect(state.refetch).toHaveBeenCalledTimes(1);
    visible(false);
    act(() => vi.advanceTimersByTime(120_000));
    expect(state.refetch).toHaveBeenCalledTimes(1);
    visible(true);
    act(() => vi.advanceTimersByTime(999));
    expect(state.refetch).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(1));
    expect(state.refetch).toHaveBeenCalledTimes(2);
    hook.unmount();
    act(() => vi.advanceTimersByTime(120_000));
    expect(state.refetch).toHaveBeenCalledTimes(2);
  });

  it("does no initial hidden read and lets reconnect close the gap once", () => {
    visible(false);
    const hook = renderHook(() => useBoundedRefresh([["anime", "list"]]));
    act(() => vi.advanceTimersByTime(120_000));
    expect(state.refetch).not.toHaveBeenCalled();
    visible(true);
    state.connected = true;
    hook.rerender();
    expect(state.refetch).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(120_000));
    expect(state.refetch).toHaveBeenCalledTimes(1);
  });
});
