import type { ReactNode } from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, type CatalogSuppression } from "./api/client";
import { QueryWrapper } from "../testing/renderWithQuery";
import { blacklistKey, useBlacklistActions, useBlacklistState } from "./blacklist";

const listCatalogSuppressions = vi.hoisted(() => vi.fn());
const suppressCatalogEntry = vi.hoisted(() => vi.fn());
const unsuppressCatalogEntry = vi.hoisted(() => vi.fn());

vi.mock("./api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api/client")>();
  return { ...actual, listCatalogSuppressions, suppressCatalogEntry, unsuppressCatalogEntry };
});

function suppression(overrides: Partial<CatalogSuppression> = {}): CatalogSuppression {
  return {
    id: "sup-1",
    provider: "mikan",
    provider_entry_ref: "12345",
    title_snapshot: "测试番剧",
    suppressed_at: "2026-07-26T00:00:00Z",
    version: 1,
    ...overrides,
  };
}

function wrapper({ children }: { children: ReactNode }) {
  return <QueryWrapper>{children}</QueryWrapper>;
}

function renderBlacklist() {
  return renderHook(() => ({ ...useBlacklistState(), ...useBlacklistActions() }), { wrapper });
}

describe("blacklist store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listCatalogSuppressions.mockResolvedValue([]);
    suppressCatalogEntry.mockImplementation((provider, ref, title) =>
      Promise.resolve(suppression({ provider, provider_entry_ref: ref, title_snapshot: title })),
    );
    unsuppressCatalogEntry.mockResolvedValue(undefined);
  });

  it("从服务端读取隐藏列表条目", async () => {
    listCatalogSuppressions.mockResolvedValue([suppression()]);
    const { result } = renderBlacklist();

    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    expect(result.current.entries[0]).toMatchObject({
      key: "mikan:12345",
      source: "mikan",
      providerEntryRef: "12345",
      title: "测试番剧",
    });
  });

  it("同一裸 ref 在两个 provider 下互不影响", async () => {
    listCatalogSuppressions.mockResolvedValue([
      suppression({ id: "sup-mikan", provider: "mikan", provider_entry_ref: "12345" }),
      suppression({ id: "sup-anibt", provider: "anibt", provider_entry_ref: "12345" }),
    ]);
    const { result } = renderBlacklist();

    await waitFor(() => expect(result.current.entries).toHaveLength(2));
    const keys = result.current.entries.map((entry) => entry.key);
    expect(new Set(keys).size).toBe(2);
    expect(keys).toContain(blacklistKey("mikan", "12345"));
    expect(keys).toContain(blacklistKey("anibt", "12345"));
  });

  it("add 调用 suppress", async () => {
    const { result } = renderBlacklist();
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    await result.current.add({ source: "mikan", providerEntryRef: "12345", title: "测试番剧" });
    expect(suppressCatalogEntry).toHaveBeenCalledWith("mikan", "12345", "测试番剧");
    expect(unsuppressCatalogEntry).not.toHaveBeenCalled();
  });

  it("remove 调用 unsuppress", async () => {
    listCatalogSuppressions.mockResolvedValue([suppression()]);
    const { result } = renderBlacklist();
    await waitFor(() => expect(result.current.entries).toHaveLength(1));

    await result.current.remove({ source: "mikan", providerEntryRef: "12345" });
    expect(unsuppressCatalogEntry).toHaveBeenCalledWith("mikan", "12345");
    expect(suppressCatalogEntry).not.toHaveBeenCalled();
  });

  it("自定义预设不写服务端，明确报错而非静默失败", async () => {
    const { result } = renderBlacklist();
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    await expect(
      result.current.add({ source: "custom", providerEntryRef: "custom:c1", title: "候选" }),
    ).rejects.toThrow(/自定义预设/);
    expect(suppressCatalogEntry).not.toHaveBeenCalled();
  });

  it("读取失败时暴露错误而非静默空列表", async () => {
    listCatalogSuppressions.mockRejectedValue(
      new ApiError({ status: 503, message: "目录服务暂不可用", code: "UNAVAILABLE" }),
    );
    const { result } = renderBlacklist();

    await waitFor(() => expect(result.current.error).toBeInstanceOf(ApiError));
    expect(result.current.entries).toEqual([]);
  });

  it("写入失败时 reject，交由调用方提示", async () => {
    suppressCatalogEntry.mockRejectedValue(
      new ApiError({ status: 422, message: "条目引用不合法", code: "CATALOG_SUPPRESSION_INVALID" }),
    );
    const { result } = renderBlacklist();
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    await expect(
      result.current.add({ source: "mikan", providerEntryRef: "12345", title: "测试番剧" }),
    ).rejects.toBeInstanceOf(ApiError);
  });
});
