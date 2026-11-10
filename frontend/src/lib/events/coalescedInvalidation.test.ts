import { InfiniteQueryObserver, QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createCoalescedInvalidation } from "./coalescedInvalidation";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}

describe("coalesced event invalidation", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("coalesces bursts and collapses overlapping key prefixes in either order", async () => {
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries").mockResolvedValue();
    const queue = createCoalescedInvalidation(client);
    queue.enqueue([["logs", "records"], ["acquisitions"]]);
    queue.enqueue([["logs"], ["acquisitions"], ["logs", "details"]]);
    expect(invalidate).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(0);
    expect(invalidate.mock.calls).toEqual([[{ queryKey: ["acquisitions"] }], [{ queryKey: ["logs"] }]]);
    queue.dispose();
    client.clear();
  });

  it("does not cancel its active pass and reads fresh after events received in flight", async () => {
    const client = new QueryClient();
    const gate = deferred();
    const invalidate = vi.spyOn(client, "invalidateQueries").mockReturnValueOnce(gate.promise).mockResolvedValue();
    const queue = createCoalescedInvalidation(client);
    queue.enqueue([["records"]]);
    await vi.advanceTimersByTimeAsync(0);
    queue.enqueue([["records"]]);
    queue.enqueue([["records"]]);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(invalidate).toHaveBeenCalledTimes(1);
    gate.resolve();
    await vi.advanceTimersByTimeAsync(1000);
    expect(invalidate).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(invalidate).toHaveBeenCalledTimes(2);
    queue.dispose();
    client.clear();
  });

  it("clears queued work on disposal, including work behind an active request", async () => {
    const client = new QueryClient();
    const gate = deferred();
    const invalidate = vi.spyOn(client, "invalidateQueries").mockReturnValue(gate.promise);
    const queue = createCoalescedInvalidation(client);
    queue.enqueue([["records"]]);
    await vi.advanceTimersByTimeAsync(0);
    queue.enqueue([["records"]]);
    queue.dispose();
    gate.resolve();
    queue.enqueue([["records"]]);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(invalidate).toHaveBeenCalledTimes(1);
    client.clear();
  });

  it("does not stay blocked after a failed pass", async () => {
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries").mockRejectedValueOnce(new Error("offline")).mockResolvedValue();
    const queue = createCoalescedInvalidation(client);
    queue.enqueue([["records"]]);
    await vi.advanceTimersByTimeAsync(0);
    queue.enqueue([["records"]]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(invalidate).toHaveBeenCalledTimes(2);
    queue.dispose();
    client.clear();
  });

  it("waits for the remaining reads when another scope rejects", async () => {
    const client = new QueryClient();
    const gate = deferred();
    const invalidate = vi.spyOn(client, "invalidateQueries")
      .mockRejectedValueOnce(new Error("offline")).mockReturnValueOnce(gate.promise).mockResolvedValue();
    const queue = createCoalescedInvalidation(client);
    queue.enqueue([["records"], ["files"]]);
    await vi.advanceTimersByTimeAsync(1000);
    queue.enqueue([["records"], ["files"]]);
    await vi.advanceTimersByTimeAsync(5000);
    expect(invalidate).toHaveBeenCalledTimes(2);
    gate.resolve();
    await vi.advanceTimersByTimeAsync(1000);
    expect(invalidate).toHaveBeenCalledTimes(4);
    queue.dispose();
    client.clear();
  });

  it("retains all loaded pages and applies the final event after a slow page read", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    const key = ["records"];
    client.setQueryData(key, { pages: [{ page: 0, version: 0 }, { page: 1, version: 0 }], pageParams: [0, 1] });
    let version = 1;
    const gate = deferred();
    const read = vi.fn(async ({ pageParam }: { pageParam: number }) => {
      const observed = version;
      if (observed === 1 && pageParam === 0) await gate.promise;
      return { page: pageParam, version: observed };
    });
    const observer = new InfiniteQueryObserver(client, {
      queryKey: key, queryFn: read, initialPageParam: 0,
      getNextPageParam: page => page.page < 1 ? page.page + 1 : undefined,
    });
    const unsubscribe = observer.subscribe(() => undefined);
    const queue = createCoalescedInvalidation(client);
    queue.enqueue([key]);
    await vi.advanceTimersByTimeAsync(0);
    version = 2;
    queue.enqueue([key]);
    queue.enqueue([key]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(read).toHaveBeenCalledTimes(1);
    gate.resolve();
    await vi.advanceTimersByTimeAsync(1000);
    expect(read).toHaveBeenCalledTimes(4);
    expect(client.getQueryData(key)).toEqual({ pages: [{ page: 0, version: 2 }, { page: 1, version: 2 }], pageParams: [0, 1] });
    queue.dispose();
    unsubscribe();
    client.clear();
  });
});
