import { act, renderHook } from "@testing-library/react";
import { QueryClient } from "@tanstack/react-query";
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

  it("catalog/subscribed/rss/records 映射到正确 query key 前缀", () => {
    initRealtimeInvalidation(client);
    useSessionStore.getState().setAuthenticated("owner");
    const invalidate = vi.spyOn(client, "invalidateQueries");

    emit({ target: "catalog" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: catalogKeys.all });

    invalidate.mockClear();
    emit({ target: "subscribed" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.list });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: catalogKeys.all });

    invalidate.mockClear();
    emit({ target: "rss" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.workspacePrefix });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: fileManagerKeys.all });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: acquisitionRecordKeys.all });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: observabilityKeys.records });
    expect(invalidate).toHaveBeenCalledTimes(4);

    invalidate.mockClear();
    emit({ target: "rss", managedAnimeId: "anime-1" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: animeKeys.workspace("anime-1") });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: rssListKeys.owner("anime-1") });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: fileManagerKeys.all });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: acquisitionRecordKeys.all });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: observabilityKeys.records });
    expect(invalidate).toHaveBeenCalledTimes(5);

    invalidate.mockClear();
    emit({ target: "records" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: observabilityKeys.all });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: fileManagerKeys.all });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: acquisitionRecordKeys.all });
    expect(invalidate).toHaveBeenCalledTimes(3);
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
