import { refreshOwnerRss } from "./refreshOwnerRss";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionStore } from "../../shell/sessionStore";
import {
  initRealtimeInvalidation,
  resetRealtimeInvalidationForTests,
  useRealtimeConnected,
} from "./useRealtimeInvalidation";
import type { RealtimeEvent } from "./sseClient";
import { acquisitionRecordKeys, animeKeys, catalogKeys, fileManagerKeys, observabilityKeys, rssListKeys } from "../queryKeys";

// Hook unit tests: the target → query key prefix invalidation mapping, connected state
// propagation and the session lifecycle (started on login, stopped on logout). sseClient is
// mocked and events are injected directly.

interface StreamDouble {
  onEvent: (event: RealtimeEvent) => void;
  onStateChange: ((connected: boolean) => void) | undefined;
  onUnauthorized: (() => void) | undefined;
  start: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
}

let lastStream: StreamDouble | undefined;

vi.mock("./sseClient", () => ({
  createRealtimeEventStream: (options: {
    onEvent: (event: RealtimeEvent) => void;
    onStateChange?: (connected: boolean) => void;
    onUnauthorized?: () => void;
  }) => {
    lastStream = {
      onEvent: options.onEvent,
      onStateChange: options.onStateChange,
      onUnauthorized: options.onUnauthorized,
      start: vi.fn(),
      stop: vi.fn(),
    };
    return { start: lastStream.start, stop: lastStream.stop };
  },
}));

function emit(event: RealtimeEvent) {
  act(() => lastStream!.onEvent(event));
}

describe("useRealtimeInvalidation", () => {
  let client: QueryClient;

  beforeEach(() => {
    lastStream = undefined;
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    window.localStorage.clear();
    useSessionStore.setState({ authenticated: false, username: null });
  });

  afterEach(() => {
    resetRealtimeInvalidationForTests();
    vi.clearAllMocks();
  });

  it("登录后启动单例流，重复 init 不重建", () => {
    initRealtimeInvalidation(client);
    expect(lastStream).toBeDefined();
    expect(lastStream!.start).not.toHaveBeenCalled();

    useSessionStore.getState().setAuthenticated("owner");
    expect(lastStream!.start).toHaveBeenCalledTimes(1);

    const first = lastStream!;
    initRealtimeInvalidation(client);
    expect(lastStream).toBe(first);
    expect(first.start).toHaveBeenCalledTimes(1);
  });

  it("无会话不启动；登出后停止", () => {
    initRealtimeInvalidation(client);
    expect(lastStream!.start).not.toHaveBeenCalled();

    useSessionStore.getState().setAuthenticated("owner");
    expect(lastStream!.start).toHaveBeenCalledTimes(1);
    useSessionStore.getState().clearSession();
    expect(lastStream!.stop).toHaveBeenCalledTimes(1);
  });

  it("catalog/subscribed/rss/records 映射到正确 query key 前缀", async () => {
    initRealtimeInvalidation(client);
    useSessionStore.getState().setAuthenticated("owner");
    const invalidate = vi.spyOn(client, "invalidateQueries");

    emit({ target: "catalog" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: catalogKeys.all });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.list });
    expect(invalidate).toHaveBeenCalledTimes(2);

    invalidate.mockClear();
    emit({ target: "subscribed" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.list });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: catalogKeys.all });

    vi.useFakeTimers();
    try {
      invalidate.mockClear();
      emit({ target: "rss" });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.workspacePrefix });
      expect(invalidate).toHaveBeenCalledTimes(1);
      await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: fileManagerKeys.all });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: acquisitionRecordKeys.all });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: observabilityKeys.records });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.list });
      expect(invalidate).toHaveBeenCalledTimes(5);

      invalidate.mockClear();
      await act(async () => lastStream!.onEvent({ target: "rss", managedAnimeId: "anime-1" }));
      expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.workspace("anime-1") });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: rssListKeys.owner("anime-1") });
      expect(invalidate).toHaveBeenCalledTimes(2);
      emit({ target: "records" });
      emit({ target: "records" });
      await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: observabilityKeys.all });
      expect(invalidate).not.toHaveBeenCalledWith({ queryKey: observabilityKeys.records });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: fileManagerKeys.all });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: acquisitionRecordKeys.all });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.list });
      expect(invalidate).toHaveBeenCalledTimes(6);
    } finally {
      vi.useRealTimers();
    }
  });

  it("rss/records 合并为一次订阅列表刷新", () => {
    vi.useFakeTimers();
    try {
      initRealtimeInvalidation(client);
      useSessionStore.getState().setAuthenticated("owner");
      const invalidate = vi.spyOn(client, "invalidateQueries");

      emit({ target: "rss", managedAnimeId: "anime-1" });
      emit({ target: "rss", managedAnimeId: "anime-2" });
      emit({ target: "records" });
      const listCalls = () =>
        invalidate.mock.calls.filter(([filters]) => {
          const key = filters?.queryKey;
          return key === animeKeys.list || (Array.isArray(key) && key.join("/") === catalogKeys.listTab("all").join("/"));
        }).length;
      expect(listCalls()).toBe(0);

      act(() => vi.advanceTimersByTime(1000));
      expect(invalidate).not.toHaveBeenCalledWith({ queryKey: catalogKeys.listTab("all") });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.list });
      expect(listCalls()).toBe(1);

      emit({ target: "catalog" });
      expect(listCalls()).toBe(2);
      act(() => vi.advanceTimersByTime(1000));
      expect(listCalls()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("coalesces a scoped event with the workspace observer and preserves unchanged events", async () => {
    const key = animeKeys.workspace("anime-1");
    const rssKey = rssListKeys.owner("anime-1");
    client.setQueryDefaults(key, { staleTime: Infinity });
    client.setQueryDefaults(rssKey, { staleTime: Infinity });
    client.setQueryData(key, { version: 0 });
    client.setQueryData(rssKey, { version: 0 });
    const workspace = new QueryObserver(client, { queryKey: key, queryFn: async () => ({ version: 1 }) });
    const readRSS = vi.fn(async () => ({ version: 1 }));
    const rss = new QueryObserver(client, { queryKey: rssKey, queryFn: readRSS });
    let last = client.getQueryData(key);
    const unsubscribeWorkspace = workspace.subscribe(result => {
      if (result.data === last) return;
      last = result.data;
      void refreshOwnerRss(client, "anime-1", result.data);
    });
    const unsubscribeRss = rss.subscribe(() => undefined);
    initRealtimeInvalidation(client);
    await act(async () => {
      lastStream!.onEvent({ target: "rss", managedAnimeId: "anime-1" });
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    expect(readRSS).toHaveBeenCalledTimes(1);
    await act(async () => {
      lastStream!.onEvent({ target: "rss", managedAnimeId: "anime-1" });
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    expect(readRSS).toHaveBeenCalledTimes(2);
    unsubscribeWorkspace();
    unsubscribeRss();
    client.clear();
  });

  it("scoped events refresh the active configuration read without fetching inactive history", async () => {
    const fullKey = animeKeys.workspace("anime-1");
    const configKey = animeKeys.workspaceConfiguration("anime-1");
    const readFull = vi.fn(async () => ({ version: 2 }));
    const readConfig = vi.fn(async () => ({ version: 2 }));
    client.setQueryDefaults(fullKey, { staleTime: Infinity, queryFn: readFull });
    client.setQueryData(fullKey, { version: 1 });
    client.setQueryData(configKey, { version: 1 });
    const configuration = new QueryObserver(client, { queryKey: configKey, queryFn: readConfig });
    const unsubscribe = configuration.subscribe(() => undefined);
    initRealtimeInvalidation(client);
    await act(async () => {
      lastStream!.onEvent({ target: "rss", managedAnimeId: "anime-1" });
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    expect(readConfig).toHaveBeenCalledTimes(1);
    expect(readFull).not.toHaveBeenCalled();
    expect(client.getQueryState(fullKey)?.isInvalidated).toBe(true);
    unsubscribe();
  });

  it("drops queued refreshes on logout or client replacement", async () => {
    vi.useFakeTimers();
    const replacement = new QueryClient();
    try {
      initRealtimeInvalidation(client);
      useSessionStore.getState().setAuthenticated("owner");
      const invalidate = vi.spyOn(client, "invalidateQueries");
      emit({ target: "records" });
      useSessionStore.getState().clearSession();
      await vi.advanceTimersByTimeAsync(1000);
      expect(invalidate).not.toHaveBeenCalled();
      useSessionStore.getState().setAuthenticated("owner");
      emit({ target: "records" });
      initRealtimeInvalidation(replacement);
      const nextInvalidate = vi.spyOn(replacement, "invalidateQueries");
      await vi.advanceTimersByTimeAsync(1000);
      expect(invalidate).not.toHaveBeenCalled();
      expect(nextInvalidate).not.toHaveBeenCalled();
      emit({ target: "records" });
      await vi.advanceTimersByTimeAsync(1000);
      expect(nextInvalidate).toHaveBeenCalledWith({ queryKey: observabilityKeys.all });
    } finally {
      replacement.clear();
      vi.useRealTimers();
    }
  });

  it("connected 状态经 useRealtimeConnected 传播", () => {
    initRealtimeInvalidation(client);
    useSessionStore.getState().setAuthenticated("owner");
    const { result } = renderHook(() => useRealtimeConnected());
    expect(result.current).toBe(false);

    act(() => lastStream!.onStateChange?.(true));
    expect(result.current).toBe(true);
    act(() => lastStream!.onStateChange?.(false));
    expect(result.current).toBe(false);
  });

  it("onUnauthorized 清空本地会话", () => {
    initRealtimeInvalidation(client);
    useSessionStore.getState().setAuthenticated("owner");
    expect(useSessionStore.getState().authenticated).toBe(true);

    act(() => lastStream!.onUnauthorized?.());
    expect(useSessionStore.getState().authenticated).toBe(false);
    expect(lastStream!.stop).toHaveBeenCalled();
  });
});
