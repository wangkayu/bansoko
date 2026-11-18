import type { Page, Route } from "@playwright/test";
import type { RuntimeLogLine, Schema } from "../../src/lib/api/client";

// Fixture projection of the runtime-log cursor contract. It is deliberately
// explicit, not an evaluator: filters run before the limit, pages are
// oldest-first, `next_after` names the next unread line and is present even on
// an empty page, and a cursor from the other storage source reports a gap.

type RuntimeLogPage = Schema<"RuntimeLogPage">;
type Source = RuntimeLogPage["source"];

type Cursor = { source: Source; kind: "a" | "b"; index: number };

function encodeCursor(source: Source, kind: "a" | "b", index: number): string {
  return `${source}:${kind}:${index}`;
}

function decodeCursor(value: string | null): Cursor | null {
  if (!value) return null;
  const [source, kind, raw] = value.split(":");
  if ((source !== "file" && source !== "memory") || (kind !== "a" && kind !== "b")) return null;
  const index = Number(raw);
  if (!Number.isInteger(index) || index < 0) return null;
  return { source, kind, index };
}

export type RuntimeLogFixture = {
  requests: URL[];
  lines: RuntimeLogLine[];
  addLines(lines: RuntimeLogLine[]): void;
  setSource(source: Source): void;
  setGapOnce(): void;
};

export async function installRuntimeLogFixture(
  page: Page,
  initialLines: RuntimeLogLine[],
  options: { source?: Source; gapOnce?: boolean } = {},
): Promise<RuntimeLogFixture> {
  const lines = [...initialLines];
  const requests: URL[] = [];
  let source: Source = options.source ?? "file";
  let gapNext = options.gapOnce === true;

  const handle = async (route: Route): Promise<void> => {
    const url = new URL(route.request().url());
    requests.push(url);
    const level = url.searchParams.get("level") ?? "all";
    const terms = (url.searchParams.get("q") ?? "").toLowerCase().split(/\s+/).filter(Boolean);
    const workItemId = url.searchParams.get("work_item_id") ?? "";
    const limit = Number(url.searchParams.get("limit") ?? "200");
    const after = decodeCursor(url.searchParams.get("after"));
    const before = decodeCursor(url.searchParams.get("before"));

    const indexed = lines
      .map((line, index) => ({ line, index }))
      .filter(({ line }) =>
        level === "error" ? line.level === "error" : level === "warn" ? line.level === "warn" || line.level === "error" : true,
      )
      .filter(({ line }) => workItemId === "" || line.work_item_id === workItemId)
      .filter(({ line }) => terms.every((term) => JSON.stringify(line).toLowerCase().includes(term)));

    let gap = gapNext;
    gapNext = false;
    let slice: { line: RuntimeLogLine; index: number }[];
    // `after` is inclusive: the cursor names the next unread line. `before`
    // is exclusive: it names the first line of the region already shown.
    if (before) {
      if (before.source !== source) {
        gap = true;
        slice = indexed.slice(-limit);
      } else {
        slice = indexed.filter(({ index }) => index < before.index).slice(-limit);
      }
    } else if (after) {
      if (after.source !== source) {
        gap = true;
        // A cursor from the other source restarts at the oldest retained line,
        // matching the server's recovery behavior.
        slice = indexed.slice(0, limit);
      } else {
        slice = indexed.filter(({ index }) => index >= after.index).slice(0, limit);
      }
    } else {
      slice = indexed.slice(-limit);
    }

    let nextAfter: string;
    if (before) {
      // Older paging does not change the newer boundary. On a source switch the
      // whole retained history is in view, so the boundary is its end.
      nextAfter = encodeCursor(source, "a", before.source === source ? before.index : lines.length);
    } else if (slice.length > 0 && slice.length === limit && after) {
      // The page was capped, so the next poll starts right after the last line
      // returned.
      nextAfter = encodeCursor(source, "a", slice[slice.length - 1]!.index + 1);
    } else {
      // The scan reached the end of what exists, even when nothing matched.
      nextAfter = encodeCursor(source, "a", lines.length);
    }

    const body: RuntimeLogPage = {
      lines: slice.map(({ line }) => line),
      source,
      ...(slice.length === 0 ? {} : { next_before: encodeCursor(source, "b", slice[0]!.index) }),
      next_after: nextAfter,
      ...(gap ? { gap: true } : {}),
    };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  };

  await page.route("**/api/v1/observability/runtime-log**", handle);

  return {
    requests,
    lines,
    addLines(next) {
      lines.push(...next);
    },
    setSource(next) {
      source = next;
    },
    setGapOnce() {
      gapNext = true;
    },
  };
}
