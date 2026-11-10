import type { QueryClient, QueryKey } from "@tanstack/react-query";

const REFRESH_WINDOW_MS = 1000;

// These event scopes are scalar key prefixes, not full keys containing filter objects.
const contains = (parent: QueryKey, child: QueryKey) =>
  parent.length <= child.length && parent.every((value, index) => value === child[index]);

/** Start an idle event batch at the microtask checkpoint, not after a fixed delay.
 * Synchronous bursts still collapse into one pass. Events arriving during a read
 * queue one trailing pass after it settles. No cached pages are removed. */
export function createCoalescedInvalidation(client: QueryClient) {
  let pending: QueryKey[] = [];
  let timer: ReturnType<typeof setTimeout> | undefined;
  let leading = false;
  let reading = false;
  let disposed = false;

  function schedule() {
    if (disposed || reading || timer !== undefined || pending.length === 0) return;
    timer = setTimeout(flush, REFRESH_WINDOW_MS);
  }

  function scheduleLeading() {
    if (disposed || reading || timer !== undefined || leading || pending.length === 0) return;
    leading = true;
    queueMicrotask(() => {
      leading = false;
      flush();
    });
  }

  function settled() {
    reading = false;
    schedule();
  }

  function flush() {
    timer = undefined;
    if (disposed || reading || pending.length === 0) return;
    const keys = pending;
    pending = [];
    reading = true;
    void Promise.allSettled(keys.map(queryKey => client.invalidateQueries({ queryKey })))
      .then(settled);
  }

  return {
    enqueue(keys: readonly QueryKey[]) {
      if (disposed) return;
      for (const key of keys) {
        if (pending.some(parent => contains(parent, key))) continue;
        pending = pending.filter(child => !contains(key, child));
        pending.push(key);
      }
      if (reading) return;
      if (timer !== undefined) return;
      scheduleLeading();
    },
    dispose() {
      disposed = true;
      clearTimeout(timer);
      timer = undefined;
      leading = false;
      pending = [];
    },
  };
}
