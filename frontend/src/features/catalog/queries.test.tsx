import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { listAnime } from "../../lib/api/client";
import { animeKeys } from "../../lib/queryKeys";
import { useCatalog } from "./queries";

afterEach(() => vi.unstubAllGlobals());

describe("shared subscription list", () => {
  it("shares one raw list across wall/log observers and seasons", async () => {
    const fetch = vi.fn(async () => new Response(JSON.stringify([]), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetch);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 15_000 } } });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    const hook = renderHook(({ season }) => {
      const wall = useCatalog("all", season);
      const log = useQuery({ queryKey: animeKeys.list, queryFn: listAnime });
      return { wall, log };
    }, { wrapper, initialProps: { season: "2026-autumn" } });
    await waitFor(() => expect(hook.result.current.wall.isSuccess).toBe(true));
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(hook.result.current.wall.data).toEqual({ rows: [] });
    expect(hook.result.current.log.data).toEqual([]);
    hook.rerender({ season: "2026-summer" });
    expect(fetch).toHaveBeenCalledTimes(1);
    await act(async () => { await client.invalidateQueries({ queryKey: animeKeys.list }); });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(client.getQueryData(animeKeys.list)).toEqual([]);
    hook.unmount();
    client.clear();
  });
});
