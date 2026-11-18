import { describe, expect, it } from "vitest";
import type { RuntimeLogLine } from "../lib/api/client";
import {
  RUNTIME_LOG_MAX_LINES,
  RUNTIME_LOG_PAGE_LIMIT,
  initialRuntimeLogFeed,
  mergeRuntimeLogPage,
  runtimeLogFeedLines,
  runtimeLogLineCount,
  runtimeLogNewerCursor,
  runtimeLogOlderCursor,
} from "./runtimeLogFeed";

function line(index: number): RuntimeLogLine {
  return {
    at: new Date(Date.UTC(2026, 10, 4, 6, 0, 0) + index * 1000).toISOString(),
    level: "info",
    message: `line ${index}`,
    component: "worker",
    operation: "tick",
  };
}

function page(from: number, count: number, extra: { gap?: boolean; source?: "file" | "memory" } = {}) {
  const lines = Array.from({ length: count }, (_, offset) => line(from + offset));
  return {
    lines,
    source: extra.source ?? "file",
    next_before: `b:${from}`,
    // The server names the next unread line, not the last line returned.
    next_after: `a:${from + count}`,
    ...(extra.gap ? { gap: true } : {}),
  };
}

describe("runtime log feed", () => {
  it("keeps pages oldest-first when appending newer and prepending older", () => {
    let state = initialRuntimeLogFeed();
    state = mergeRuntimeLogPage(state, page(200, 2), "initial").state;
    expect(runtimeLogFeedLines(state).map((entry) => entry.line.message)).toEqual(["line 200", "line 201"]);
    expect(runtimeLogNewerCursor(state)).toBe("a:202");
    expect(runtimeLogOlderCursor(state)).toBe("b:200");

    state = mergeRuntimeLogPage(state, page(202, 2), "append").state;
    expect(runtimeLogFeedLines(state).map((entry) => entry.line.message)).toEqual([
      "line 200",
      "line 201",
      "line 202",
      "line 203",
    ]);
    expect(runtimeLogNewerCursor(state)).toBe("a:204");

    state = mergeRuntimeLogPage(state, page(198, 2), "prepend").state;
    expect(runtimeLogFeedLines(state).map((entry) => entry.line.message)).toEqual([
      "line 198",
      "line 199",
      "line 200",
      "line 201",
      "line 202",
      "line 203",
    ]);
    expect(runtimeLogOlderCursor(state)).toBe("b:198");
    // Chunk keys are stable and unique across prepend/append.
    expect(new Set(runtimeLogFeedLines(state).map((entry) => entry.key)).size).toBe(6);
  });

  it("keeps the window and stores an advanced watermark for an empty page", () => {
    let state = mergeRuntimeLogPage(initialRuntimeLogFeed(), page(0, 3), "initial").state;
    expect(runtimeLogNewerCursor(state)).toBe("a:3");
    const empty = mergeRuntimeLogPage(state, { lines: [], source: "file", next_after: "a:9" }, "append");
    expect(empty.reset).toBe(false);
    // Nothing rendered changed, but the scanned boundary did.
    expect(runtimeLogLineCount(empty.state)).toBe(3);
    expect(runtimeLogNewerCursor(empty.state)).toBe("a:9");
    expect(empty.state.revision).toBe(state.revision);
    // An empty older page means there is nothing more to load, and it must not
    // move the newer watermark.
    const olderEmpty = mergeRuntimeLogPage(empty.state, { lines: [], source: "file", next_after: "a:0" }, "prepend");
    expect(runtimeLogOlderCursor(olderEmpty.state)).toBe("b:0");
    expect(runtimeLogNewerCursor(olderEmpty.state)).toBe("a:9");
  });

  it("can poll incrementally from an empty initial page", () => {
    const initial = mergeRuntimeLogPage(
      initialRuntimeLogFeed(),
      { lines: [], source: "file", next_after: "a:0" },
      "initial",
    );
    expect(initial.reset).toBe(true);
    expect(runtimeLogLineCount(initial.state)).toBe(0);
    expect(runtimeLogNewerCursor(initial.state)).toBe("a:0");
    expect(initial.state.source).toBe("file");
  });

  it("replaces the window on a gap instead of concatenating the page", () => {
    let state = mergeRuntimeLogPage(initialRuntimeLogFeed(), page(200, 2), "initial").state;
    const merged = mergeRuntimeLogPage(state, page(0, 2, { gap: true }), "prepend");
    expect(merged.reset).toBe(true);
    expect(merged.gap).toBe(true);
    expect(merged.state.revision).toBe(state.revision + 1);
    expect(runtimeLogFeedLines(merged.state).map((entry) => entry.line.message)).toEqual(["line 0", "line 1"]);
  });

  it("never mixes pages from two storage sources", () => {
    let state = mergeRuntimeLogPage(initialRuntimeLogFeed(), page(10, 2), "initial").state;
    const merged = mergeRuntimeLogPage(state, { ...page(12, 2), source: "memory" }, "append");
    expect(merged.reset).toBe(true);
    expect(merged.sourceChanged).toBe(true);
    expect(merged.state.source).toBe("memory");
    expect(runtimeLogFeedLines(merged.state).map((entry) => entry.line.message)).toEqual(["line 12", "line 13"]);
  });

  it("bounds the browser window while keeping both page directions addressable", () => {
    let state = initialRuntimeLogFeed();
    for (let index = 0; index < 11; index += 1) {
      state = mergeRuntimeLogPage(state, page(index * RUNTIME_LOG_PAGE_LIMIT, RUNTIME_LOG_PAGE_LIMIT), index === 0 ? "initial" : "append").state;
    }
    expect(runtimeLogLineCount(state)).toBe(RUNTIME_LOG_MAX_LINES);
    expect(runtimeLogFeedLines(state)[0]!.line.message).toBe("line 200");
    expect(runtimeLogOlderCursor(state)).toBe("b:200");

    // The dropped older page is fetched again with that cursor and prepended;
    // the newest page falls off the far side instead of growing forever.
    state = mergeRuntimeLogPage(state, page(0, RUNTIME_LOG_PAGE_LIMIT), "prepend").state;
    expect(runtimeLogLineCount(state)).toBe(RUNTIME_LOG_MAX_LINES);
    expect(runtimeLogFeedLines(state)[0]!.line.message).toBe("line 0");
    expect(runtimeLogFeedLines(state).at(-1)!.line.message).toBe(`line ${10 * RUNTIME_LOG_PAGE_LIMIT - 1}`);
    // The watermark survives the newest page being trimmed from the window.
    expect(runtimeLogNewerCursor(state)).toBe(`a:${11 * RUNTIME_LOG_PAGE_LIMIT}`);
  });
});
