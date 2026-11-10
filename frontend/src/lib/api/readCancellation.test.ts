import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getAcquisitionRecord, getRuntimeLog, listAcquisitionRecordFiles, listAcquisitionRecords, listObservabilityDiagnostics, listObservabilityRecords, listReleaseBatchEntries } from "./client";

const reads: Array<[string, (signal: AbortSignal) => Promise<unknown>]> = [
  ["execution list", signal => listAcquisitionRecords({}, signal)],
  ["execution detail", signal => getAcquisitionRecord("record-1", signal)],
  ["execution files", signal => listAcquisitionRecordFiles("record-1", {}, signal)],
  ["business logs", signal => listObservabilityRecords({}, signal)],
  ["batch logs", signal => listReleaseBatchEntries("batch-1", {}, signal)],
  ["diagnostics", signal => listObservabilityDiagnostics({}, signal)],
  ["runtime logs", signal => getRuntimeLog({}, signal)],
];

afterEach(() => vi.unstubAllGlobals());

function pendingFetch() {
  const aborted = vi.fn();
  const fetch = vi.fn((_url: string, options: RequestInit) => new Promise<Response>((_resolve, reject) => {
    options.signal?.addEventListener("abort", () => {
      aborted();
      reject(options.signal?.reason);
    }, { once: true });
  }));
  vi.stubGlobal("fetch", fetch);
  return { fetch, aborted };
}

describe("read request cancellation", () => {
  it.each(reads)("forwards abort for %s without wrapping it as a network error", async (_name, read) => {
    const { fetch, aborted } = pendingFetch();
    const controller = new AbortController();
    const promise = read(controller.signal);
    controller.abort();
    await expect(promise).rejects.toBe(controller.signal.reason);
    expect(fetch.mock.calls[0][1].signal).toBe(controller.signal);
    expect(aborted).toHaveBeenCalledTimes(1);
  });

  it("preserves cancellation while the response body is still downloading", async () => {
    const controller = new AbortController();
    let bodyStarted!: () => void;
    const started = new Promise<void>(resolve => { bodyStarted = resolve; });
    vi.stubGlobal("fetch", vi.fn(async () => ({
      ok: true, status: 200,
      json: () => new Promise((_resolve, reject) => {
        bodyStarted();
        controller.signal.addEventListener("abort", () => reject(controller.signal.reason), { once: true });
      }),
    })));
    const result = listAcquisitionRecords({}, controller.signal);
    await started;
    controller.abort();
    await expect(result).rejects.toBe(controller.signal.reason);
  });

  it("keeps a shared request until its last observer leaves", async () => {
    const { fetch, aborted } = pendingFetch();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const options = { queryKey: ["records"], queryFn: ({ signal }: { signal: AbortSignal }) => listAcquisitionRecords({}, signal) };
    const first = new QueryObserver(client, options);
    const second = new QueryObserver(client, options);
    const leaveFirst = first.subscribe(() => undefined);
    const leaveSecond = second.subscribe(() => undefined);
    expect(fetch).toHaveBeenCalledTimes(1);
    leaveFirst();
    expect(aborted).not.toHaveBeenCalled();
    leaveSecond();
    await Promise.resolve();
    expect(aborted).toHaveBeenCalledTimes(1);
    client.clear();
  });

  it("aborts obsolete filter reads without cancelling the replacement", async () => {
    const { fetch } = pendingFetch();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const options = (state: "running" | "failed") => ({
      queryKey: ["records", state],
      queryFn: ({ signal }: { signal: AbortSignal }) => listAcquisitionRecords({ state }, signal),
    });
    const observer = new QueryObserver(client, options("running"));
    const leave = observer.subscribe(() => undefined);
    observer.setOptions(options("failed"));
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls[0][1].signal?.aborted).toBe(true);
    expect(fetch.mock.calls[1][1].signal?.aborted).toBe(false);
    leave();
    await Promise.resolve();
    client.clear();
  });
});
