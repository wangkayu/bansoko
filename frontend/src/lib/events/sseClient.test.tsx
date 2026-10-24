import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRealtimeEventStream, type RealtimeEvent } from "./sseClient";

// sseClient unit tests: mocked fetch + ReadableStream, verifying SSE frame parsing,
// exponential-backoff reconnection, document.hidden pause/resume and stop cleanup. All timers
// are fake (the 1s→15s backoff is advanced with advanceTimersByTimeAsync).

type FetchStub = typeof fetch;

function sseResponse(frames: string[], { keepOpen = false }: { keepOpen?: boolean } = {}) {
  const encoder = new TextEncoder();
  let sent = false;
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      if (frames.length > 0 && !sent) {
        sent = true;
        controller.enqueue(encoder.encode(frames.join("")));
        if (!keepOpen) controller.close();
      }
    },
  });
  return new Response(body, {
    status: 200,
    headers: { "Content-Type": "text/event-stream" },
  });
}

function neverEndingResponse() {
  return new Response(new ReadableStream<Uint8Array>({ start() {} }), {
    status: 200,
    headers: { "Content-Type": "text/event-stream" },
  });
}

function stubFetch(impl: (...args: Parameters<FetchStub>) => Promise<Response>) {
  const mock = vi.fn(impl);
  vi.stubGlobal("fetch", mock);
  return mock;
}

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", { value: hidden, configurable: true });
}

function setVisibility(hidden: boolean) {
  setHidden(hidden);
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("createRealtimeEventStream", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    // Set the flag without dispatching: a visibility event here would leak a
    // reconnect into the previous test's still-running stream.
    setHidden(false);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("解析 data: 帧并触发 onEvent，忽略心跳注释行", async () => {
    stubFetch(async () =>
      sseResponse([
        ": connected\n\n",
        'data: {"target":"rss"}\n\n',
        ": heartbeat\n\n",
        'data: {"target":"catalog"}\n\n',
        'data: {"managed_anime_id":"anime-1","target":"rss"}\n\n',
      ]),
    );
    const events: RealtimeEvent[] = [];
    const states: boolean[] = [];
    const stream = createRealtimeEventStream({
      isAuthenticated: () => true,
      onEvent: (event) => events.push(event),
      onStateChange: (connected) => states.push(connected),
    });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(events).toEqual([{ target: "rss" }, { target: "catalog" }, { target: "rss", managedAnimeId: "anime-1" }]);
    // Connected on first chunk, disconnected after clean EOF (then reconnects).
    expect(states[0]).toBe(true);

    stream.stop();
  });

  it("不发送 Authorization header，改为同源携带会话 cookie", async () => {
    const fetchMock = stubFetch(async () => sseResponse([": connected\n\n"]));
    const stream = createRealtimeEventStream({ isAuthenticated: () => true, onEvent: () => {} });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);

    const [, init] = fetchMock.mock.calls[0]!;
    expect(new Headers(init?.headers).get("Authorization")).toBeNull();
    expect(init?.credentials).toBe("same-origin");

    stream.stop();
  });

  it("会话未建立时不连接，等到下一次重试", async () => {
    let authenticated = false;
    const fetchMock = stubFetch(async () => neverEndingResponse());
    const stream = createRealtimeEventStream({ isAuthenticated: () => authenticated, onEvent: () => {} });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).not.toHaveBeenCalled();

    authenticated = true;
    await vi.advanceTimersByTimeAsync(1_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    stream.stop();
  });

  it("忽略未知 target 与畸形 JSON，不中断流", async () => {
    stubFetch(async () =>
      sseResponse(['data: {"target":"unknown"}\n\n', "data: {broken\n\n", 'data: {"target":"records"}\n\n']),
    );
    const events: RealtimeEvent[] = [];
    const stream = createRealtimeEventStream({ isAuthenticated: () => true, onEvent: (event) => events.push(event) });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(events).toEqual([{ target: "records" }]);

    stream.stop();
  });

  it("EOF 后重连；每次成功接收后退避重置为 1s", async () => {
    const fetchMock = stubFetch(async () => sseResponse([": connected\n\n"]));
    const stream = createRealtimeEventStream({ isAuthenticated: () => true, onEvent: () => {} });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    // Each reconnect receives a chunk, so the backoff resets to 1s every
    // time: reconnects fire at +1s, +1s, +1s… (the growing backoff only
    // applies to consecutive failures — covered by the cap test below).
    await vi.advanceTimersByTimeAsync(999);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(999);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchMock).toHaveBeenCalledTimes(3);

    stream.stop();
  });

  it("失败重连的退避封顶 15s", async () => {
    let call = 0;
    const fetchMock = stubFetch(async () => {
      call += 1;
      // First two attempts fail immediately (backoff grows), then hang open so
      // no further reconnects interfere with the assertion window.
      if (call <= 2) throw new Error("network down");
      return neverEndingResponse();
    });
    const stream = createRealtimeEventStream({ isAuthenticated: () => true, onEvent: () => {} });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(fetchMock).toHaveBeenCalledTimes(2);

    // Third attempt stays open (connected); no more timers fire.
    await vi.advanceTimersByTimeAsync(2_000);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetchMock).toHaveBeenCalledTimes(3);

    stream.stop();
  });

  it("document.hidden 断开并暂停重连，可见时立即恢复", async () => {
    const fetchMock = stubFetch(async () => neverEndingResponse());
    const states: boolean[] = [];
    const stream = createRealtimeEventStream({
      isAuthenticated: () => true,
      onEvent: () => {},
      onStateChange: (connected) => states.push(connected),
    });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(states).toEqual([true]);

    setVisibility(true);
    expect(states).toEqual([true, false]);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    setVisibility(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(states).toEqual([true, false, true]);
    await vi.advanceTimersByTimeAsync(999);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);

    stream.stop();
  });

  it("stop() 中止连接并清理重连定时器", async () => {
    const fetchMock = stubFetch(async () => sseResponse([": connected\n\n"]));
    const stream = createRealtimeEventStream({ isAuthenticated: () => true, onEvent: () => {} });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    stream.stop();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("401 是终态：不重连并回调 onUnauthorized", async () => {
    const fetchMock = stubFetch(async () => new Response("unauthorized", { status: 401 }));
    const onUnauthorized = vi.fn();
    const stream = createRealtimeEventStream({
      isAuthenticated: () => true,
      onEvent: () => {},
      onUnauthorized,
    });

    stream.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    stream.stop();
  });
});
