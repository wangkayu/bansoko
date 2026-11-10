import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { rssListKeys } from "../queryKeys";
import { refreshOwnerRss } from "./refreshOwnerRss";

describe("owner RSS refresh coordination", () => {
  it("shares the read for the same workspace snapshot but not later events", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    const queryKey = rssListKeys.owner("anime-1");
    client.setQueryData(queryKey, { version: 0 });
    const queryFn = vi.fn(async () => ({ version: 1 }));
    const observer = new QueryObserver(client, { queryKey, queryFn });
    const unsubscribe = observer.subscribe(() => undefined);
    const workspace = { version: 1 };
    await refreshOwnerRss(client, "anime-1", workspace);
    await refreshOwnerRss(client, "anime-1", workspace);
    expect(queryFn).toHaveBeenCalledTimes(1);
    await refreshOwnerRss(client, "anime-1", workspace, true);
    expect(queryFn).toHaveBeenCalledTimes(2);
    await refreshOwnerRss(client, "anime-1", { version: 2 });
    expect(queryFn).toHaveBeenCalledTimes(3);
    unsubscribe();
    client.clear();
  });

  it("keeps caches and owner scopes independent", async () => {
    const clients = [new QueryClient(), new QueryClient()];
    const spies = clients.map(client => vi.spyOn(client, "invalidateQueries"));
    const snapshot = {};
    await refreshOwnerRss(clients[0], "a", snapshot);
    await refreshOwnerRss(clients[0], "b", snapshot);
    await refreshOwnerRss(clients[1], "a", snapshot);
    expect(spies[0]).toHaveBeenCalledTimes(2);
    expect(spies[1]).toHaveBeenCalledTimes(1);
    clients.forEach(client => client.clear());
  });
});
