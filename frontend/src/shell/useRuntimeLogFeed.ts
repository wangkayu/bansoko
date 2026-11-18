import { useCallback, useEffect, useRef, useState } from "react";
import { getRuntimeLog, type RuntimeLogQuery } from "../lib/api/client";
import {
  RUNTIME_LOG_PAGE_LIMIT,
  initialRuntimeLogFeed,
  mergeRuntimeLogPage,
  runtimeLogFeedLines,
  runtimeLogLineCount,
  runtimeLogNewerCursor,
  runtimeLogOlderCursor,
  type RuntimeLogFeedState,
} from "./runtimeLogFeed";

// Reading the retained runtime log history:
//   - the first read is the newest matching page (no cursor);
//   - "load older" pages backwards with next_before and prepends;
//   - while the tab is visible and active, every 5 seconds reads with
//     next_after; a full page is followed immediately so a burst of lines
//     catches up within one tick;
//   - an empty incremental page stores the boundary it scanned to, so the next
//     poll continues from there rather than re-reading the same range;
//   - a gap or a storage-source switch replaces the window instead of mixing
//     pages, and the reader may keep paging both ways.

export const RUNTIME_LOG_REFRESH_MS = 5000;
const MAX_CATCH_UP_PAGES = 20;

export type RuntimeLogLevel = NonNullable<RuntimeLogQuery["level"]>;
export type RuntimeLogFeedStatus = "loading" | "ready" | "error";
export type RuntimeLogOlderStatus = "idle" | "loading" | "done" | "error";

type FeedView = {
  status: RuntimeLogFeedStatus;
  error?: unknown;
  older: RuntimeLogOlderStatus;
  pollFailed: boolean;
};

type Cursor = { before?: string; after?: string };

function isAbortError(error: unknown): boolean {
  return typeof error === "object" && error !== null && (error as { name?: string }).name === "AbortError";
}

export function useRuntimeLogFeed({
  active,
  level,
  q,
  workItemId,
}: {
  active: boolean;
  level: RuntimeLogLevel;
  q: string;
  workItemId: string;
}) {
  // The state ref is the synchronous source of truth for paging cursors; React
  // state only drives rendering. A merge must never read a stale cursor.
  const [state, setState] = useState<RuntimeLogFeedState>(initialRuntimeLogFeed);
  const stateRef = useRef(state);
  const commit = useCallback((next: RuntimeLogFeedState) => {
    stateRef.current = next;
    setState(next);
  }, []);

  const [view, setView] = useState<FeedView>({ status: "loading", older: "idle", pollFailed: false });
  const viewRef = useRef(view);
  const setViewState = useCallback((patch: Partial<FeedView>) => {
    const next = { ...viewRef.current, ...patch };
    viewRef.current = next;
    setView(next);
  }, []);

  const generationRef = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const pollingRef = useRef(false);
  const previousScopeRef = useRef<string | null>(null);
  const forceReloadRef = useRef(false);
  const [attempt, setAttempt] = useState(0);
  // One request at a time: a poll queued behind "load older" must see the
  // cursor that page produced, not the one from before the click.
  const chainRef = useRef<Promise<unknown>>(Promise.resolve());

  const runSerial = useCallback(<T,>(task: () => Promise<T>): Promise<T> => {
    const next = chainRef.current.then(task, task);
    chainRef.current = next.then(
      () => undefined,
      () => undefined,
    );
    return next;
  }, []);

  const getPage = useCallback(
    (cursor: Cursor, signal?: AbortSignal) => {
      const query: RuntimeLogQuery = {
        level,
        limit: RUNTIME_LOG_PAGE_LIMIT,
        ...(q === "" ? {} : { q }),
        ...(workItemId === "" ? {} : { work_item_id: workItemId }),
        ...cursor,
      };
      return getRuntimeLog(query, signal);
    },
    [level, q, workItemId],
  );

  const pollNewer = useCallback(
    async (generation: number, signal: AbortSignal) => {
      if (pollingRef.current) return;
      pollingRef.current = true;
      try {
        for (let pageIndex = 0; pageIndex < MAX_CATCH_UP_PAGES; pageIndex += 1) {
          const cursor = runtimeLogNewerCursor(stateRef.current);
          const page = await getPage(cursor === undefined ? {} : { after: cursor }, signal);
          if (generation !== generationRef.current) return;
          const merged = mergeRuntimeLogPage(stateRef.current, page, "append");
          if (merged.reset) {
            commit(merged.state);
            setViewState({
              status: "ready",
              error: undefined,
              pollFailed: false,
              older: runtimeLogOlderCursor(merged.state) === undefined ? "done" : "idle",
            });
            return;
          }
          if (page.lines.length === 0) {
            // Empty incremental page: keep the loaded window, but commit the
            // advanced `next_after` so the next tick starts where this scan
            // ended instead of repeating it.
            commit(merged.state);
            setViewState({ pollFailed: false });
            return;
          }
          commit(merged.state);
          if (viewRef.current.older === "done") setViewState({ older: "idle" });
          setViewState({ status: "ready", error: undefined, pollFailed: false });
          // A full page is followed immediately, unless the server handed back
          // the same watermark (then reading it again would duplicate lines).
          if (runtimeLogNewerCursor(merged.state) === cursor) return;
          if (page.lines.length < RUNTIME_LOG_PAGE_LIMIT) return;
        }
      } catch (error) {
        if (generation !== generationRef.current || isAbortError(error)) return;
        setViewState({ pollFailed: true });
      } finally {
        pollingRef.current = false;
      }
    },
    [commit, getPage, setViewState],
  );

  const scopeKey = `${level}\u0000${q}\u0000${workItemId}`;

  useEffect(() => {
    if (!active) return;
    const reload = forceReloadRef.current || previousScopeRef.current !== scopeKey;
    forceReloadRef.current = false;
    previousScopeRef.current = scopeKey;

    const generation = generationRef.current + 1;
    generationRef.current = generation;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    if (reload) {
      const reset = initialRuntimeLogFeed();
      commit(reset);
      setViewState({ status: "loading", error: undefined, older: "idle", pollFailed: false });
      void runSerial(async () => {
        try {
          const page = await getPage({}, controller.signal);
          if (generation !== generationRef.current) return;
          const merged = mergeRuntimeLogPage(initialRuntimeLogFeed(), page, "initial");
          commit(merged.state);
          setViewState({
            status: "ready",
            error: undefined,
            older: runtimeLogOlderCursor(merged.state) === undefined ? "done" : "idle",
          });
        } catch (error) {
          if (generation !== generationRef.current || isAbortError(error)) return;
          setViewState({ status: "error", error });
        }
      });
    } else {
      // The tab became active again on the same filters: catch up from the
      // watermark instead of reloading the newest page (no duplicates). It is
      // queued behind any in-flight initial/older read, so it sees that page's
      // cursor.
      void runSerial(() => pollNewer(generation, controller.signal));
    }

    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = () => {
      if (document.visibilityState === "hidden") return;
      void runSerial(() => pollNewer(generation, controller.signal));
    };
    const schedule = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        tick();
        if (!stopped) schedule();
      }, RUNTIME_LOG_REFRESH_MS);
    };
    schedule();
    const onVisibility = () => {
      if (document.visibilityState === "hidden") return;
      // Poll immediately on becoming visible, then restart the 5s cadence.
      tick();
      schedule();
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
      controller.abort();
    };
  }, [active, scopeKey, attempt, commit, getPage, pollNewer, runSerial, setViewState]);

  const loadOlder = useCallback(() => {
    if (viewRef.current.status !== "ready" || viewRef.current.older === "loading" || viewRef.current.older === "done") {
      return Promise.resolve();
    }
    const cursor = runtimeLogOlderCursor(stateRef.current);
    if (cursor === undefined) {
      setViewState({ older: "done" });
      return Promise.resolve();
    }
    const generation = generationRef.current;
    setViewState({ older: "loading" });
    return runSerial(async () => {
      try {
        const page = await getPage({ before: cursor }, abortRef.current?.signal);
        if (generation !== generationRef.current) return;
        const merged = mergeRuntimeLogPage(stateRef.current, page, "prepend");
        if (merged.reset) {
          // A gap or a source switch replaces the window even on an empty page.
          commit(merged.state);
          setViewState({ older: runtimeLogOlderCursor(merged.state) === undefined ? "done" : "idle" });
          return;
        }
        if (page.lines.length === 0) {
          setViewState({ older: "done" });
          return;
        }
        commit(merged.state);
        setViewState({ older: runtimeLogOlderCursor(merged.state) === undefined ? "done" : "idle" });
      } catch (error) {
        if (generation !== generationRef.current || isAbortError(error)) return;
        setViewState({ older: "error" });
      }
    });
  }, [commit, getPage, runSerial, setViewState]);

  const retry = useCallback(() => {
    forceReloadRef.current = true;
    setAttempt((value) => value + 1);
  }, []);

  return {
    state,
    lines: runtimeLogFeedLines(state),
    lineCount: runtimeLogLineCount(state),
    status: view.status,
    error: view.error,
    older: view.older,
    pollFailed: view.pollFailed,
    loadOlder,
    retry,
  };
}
