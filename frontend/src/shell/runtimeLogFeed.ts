import type { RuntimeLogLine, Schema } from "../lib/api/client";

// The runtime log feed keeps the rendered window bounded while still paging
// both ways. The server returns matching pages oldest-first plus the cursors
// that continue them:
//
//   - `next_before` asks for matching lines older than the first line returned;
//   - `next_after` names the next unread position for incremental refresh;
//   - every page carries `next_after`, including an empty one: an incremental
//     read with no matches advances to the end of what it scanned. The feed
//     stores that boundary even when it renders no new line, so the next poll
//     does not re-read the same range.
//
// Chunks are kept as pages instead of one flat array: trimming a whole page
// updates the cursor for that side exactly, so the other direction can keep
// paging without duplicates or a silent hole. A state-level watermark survives
// trimming the newest chunk, which is why the incremental cursor is not read
// from the last chunk alone.

export type RuntimeLogPage = Schema<"RuntimeLogPage">;
export type RuntimeLogSource = RuntimeLogPage["source"];

export const RUNTIME_LOG_PAGE_LIMIT = 200;
/** Bound on browser memory and DOM: older/newer pages fall off this window. */
export const RUNTIME_LOG_MAX_LINES = 2000;
export const RUNTIME_LOG_MAX_CHUNKS = 40;

export type RuntimeLogChunk = {
  key: number;
  lines: RuntimeLogLine[];
  /** Cursor before this chunk's first line, for loading older. */
  nextBefore?: string;
  /** Cursor after this chunk's last line, for incremental refresh. */
  nextAfter?: string;
  source: RuntimeLogSource;
};

export type RuntimeLogFeedState = {
  /** Oldest page first. */
  chunks: RuntimeLogChunk[];
  source?: RuntimeLogSource;
  /**
   * Latest server watermark (`next_after`), kept even when an incremental page
   * was empty or newest chunks were trimmed from the window.
   */
  watermark?: string;
  /** A supplied cursor was no longer retained; the view restarted from this page. */
  gap: boolean;
  /** The storage source changed; the view restarted without mixing sources. */
  sourceChanged: boolean;
  nextKey: number;
  /** Increments whenever the view is replaced, so the reader can reset its scroll. */
  revision: number;
};

export type RuntimeLogFeedLine = {
  /** Stable across append/prepend (chunk key + line index within the chunk). */
  key: string;
  line: RuntimeLogLine;
};

export type RuntimeLogMerge = {
  state: RuntimeLogFeedState;
  /** The chunks were replaced rather than extended. */
  reset: boolean;
  gap: boolean;
  sourceChanged: boolean;
};

export function initialRuntimeLogFeed(): RuntimeLogFeedState {
  return { chunks: [], gap: false, sourceChanged: false, nextKey: 0, revision: 0 };
}

/** Cursor for the next older page; undefined means there is nothing to page back from. */
export function runtimeLogOlderCursor(state: RuntimeLogFeedState): string | undefined {
  return state.chunks[0]?.nextBefore;
}

/** Incremental watermark for the next newer page; undefined starts from the newest line. */
export function runtimeLogNewerCursor(state: RuntimeLogFeedState): string | undefined {
  return state.watermark ?? state.chunks[state.chunks.length - 1]?.nextAfter;
}

export function runtimeLogLineCount(state: RuntimeLogFeedState): number {
  return state.chunks.reduce((total, chunk) => total + chunk.lines.length, 0);
}

export function runtimeLogFeedLines(state: RuntimeLogFeedState): RuntimeLogFeedLine[] {
  const out: RuntimeLogFeedLine[] = [];
  for (const chunk of state.chunks) {
    for (let index = 0; index < chunk.lines.length; index += 1) {
      out.push({ key: `${chunk.key}:${index}`, line: chunk.lines[index]! });
    }
  }
  return out;
}

type Direction = "initial" | "append" | "prepend";

/**
 * Adds one server page to the feed. A gap or a storage-source change replaces
 * the whole window with the returned page: lines from two sources, or from
 * both sides of an evicted cursor, never sit in one list.
 */
export function mergeRuntimeLogPage(
  state: RuntimeLogFeedState,
  page: RuntimeLogPage,
  direction: Direction,
): RuntimeLogMerge {
  const gap = page.gap === true;
  const switched = direction !== "initial" && state.chunks.length > 0 && state.source !== undefined && page.source !== state.source;
  if (direction === "initial" || gap || switched) {
    const nextKey = state.nextKey + 1;
    const chunks =
      page.lines.length === 0
        ? []
        : [
            {
              key: nextKey,
              lines: page.lines,
              nextBefore: page.next_before,
              nextAfter: page.next_after,
              source: page.source,
            },
          ];
    return {
      state: {
        chunks,
        source: page.source,
        watermark: page.next_after ?? state.watermark,
        gap,
        sourceChanged: switched,
        nextKey,
        revision: state.revision + 1,
      },
      reset: true,
      gap,
      sourceChanged: switched,
    };
  }
  if (page.lines.length === 0) {
    // No matching lines in this direction. An incremental page still carries
    // the boundary it scanned to, so store it: the next poll starts there
    // instead of re-reading the same range. A backwards page must not move the
    // newer watermark.
    const watermark =
      direction === "append" && page.next_after !== undefined ? page.next_after : state.watermark;
    return {
      state: {
        ...state,
        source: page.source,
        watermark,
      },
      reset: false,
      gap: false,
      sourceChanged: false,
    };
  }
  const chunk: RuntimeLogChunk = {
    key: state.nextKey + 1,
    lines: page.lines,
    nextBefore: page.next_before,
    nextAfter: page.next_after,
    source: page.source,
  };
  const chunks = direction === "prepend" ? [chunk, ...state.chunks] : [...state.chunks, chunk];
  return {
    state: trimRuntimeLogFeed(
      {
        ...state,
        chunks,
        source: page.source,
        watermark: direction === "append" ? (page.next_after ?? state.watermark) : state.watermark,
        nextKey: chunk.key,
      },
      direction,
    ),
    reset: false,
    gap: false,
    sourceChanged: false,
  };
}

/**
 * Drops whole pages from the side the reader is not paging toward. The cursor
 * of the new boundary page is still the correct cursor for that side, so the
 * dropped lines can be fetched again later (and nothing is duplicated while
 * they are dropped).
 */
function trimRuntimeLogFeed(state: RuntimeLogFeedState, direction: Direction): RuntimeLogFeedState {
  let { chunks } = state;
  const overBound = () =>
    chunks.length > RUNTIME_LOG_MAX_CHUNKS ||
    chunks.reduce((total, chunk) => total + chunk.lines.length, 0) > RUNTIME_LOG_MAX_LINES;
  // Appending trims the oldest pages; loading older trims the newest pages.
  while (chunks.length > 1 && overBound()) {
    chunks = direction === "prepend" ? chunks.slice(0, -1) : chunks.slice(1);
  }
  return chunks === state.chunks ? state : { ...state, chunks };
}
