import { useLayoutEffect, useRef } from "react";
import type { RuntimeLogLine } from "../lib/api/client";
import { getLocale, t } from "../lib/i18n";
import type { RuntimeLogFeedLine } from "./runtimeLogFeed";

// The runtime log history is a terminal-like list, not a table: one line per
// write, with every allowlisted structured field the server projected wrapped
// underneath. Appending follows new lines only while the reader stays at the
// bottom; prepending older history (or trimming the far end of the bounded
// window) keeps the same content under the reader.

const NEAR_BOTTOM_PX = 24;

type RuntimeField = { key: string; value: string | number | boolean };

// Every optional field of the contract, in the same order as the copied text.
const FIELD_ORDER = [
  "kind",
  "work_item_id",
  "stage",
  "state_before",
  "state_after",
  "disposition",
  "owner_type",
  "attempt",
  "duration_ms",
  "retryable",
  "outcome",
  "script_name",
  "exit_code",
  "http_status",
  "script_summary",
  "reason_code",
  "error_class",
  "request_id",
  "correlation_id",
] as const satisfies readonly (keyof RuntimeLogLine)[];

const LEVEL_TONE: Record<RuntimeLogLine["level"], string> = {
  error: "tw:text-destructive",
  warn: "tw:text-warning",
  info: "tw:text-muted-foreground",
  debug: "tw:text-muted-foreground",
};

function runtimeFields(line: RuntimeLogLine): RuntimeField[] {
  const fields: RuntimeField[] = [];
  for (const key of FIELD_ORDER) {
    const value = line[key];
    if (value !== undefined && value !== "") fields.push({ key, value });
  }
  return fields;
}

function logTime(at: string): string {
  const date = new Date(at);
  if (Number.isNaN(date.getTime())) return at;
  return date.toLocaleString(getLocale(), {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

type ScrollMetrics = {
  scopeKey: string;
  revision: number;
  firstKey?: string;
  scrollHeight: number;
  scrollTop: number;
};

export function RuntimeLogRows({
  entries,
  scopeKey,
  revision,
  onViewTask,
}: {
  entries: readonly RuntimeLogFeedLine[];
  scopeKey: string;
  revision: number;
  onViewTask: (workItemId: string) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const follows = useRef(true);
  const metrics = useRef<ScrollMetrics | null>(null);

  useLayoutEffect(() => {
    const node = scroller.current;
    if (!node) return;
    const previous = metrics.current;
    const firstKey = entries[0]?.key;
    // A new filter scope or a gap/source reset starts a fresh reading position.
    if (!previous || previous.scopeKey !== scopeKey || previous.revision !== revision) {
      follows.current = true;
      node.scrollTop = node.scrollHeight;
    } else if (previous.firstKey !== firstKey && !follows.current) {
      // Older lines were prepended (or the oldest bounded pages were trimmed)
      // while the reader is above the bottom: keep the same content under the
      // reader instead of jumping.
      node.scrollTop = previous.scrollTop + (node.scrollHeight - previous.scrollHeight);
    } else if (follows.current) {
      node.scrollTop = node.scrollHeight;
    }
    metrics.current = {
      scopeKey,
      revision,
      firstKey,
      scrollHeight: node.scrollHeight,
      scrollTop: node.scrollTop,
    };
  }, [entries, scopeKey, revision]);

  return (
    <div
      ref={scroller}
      role="region"
      aria-label={t("log.rawlog.region")}
      tabIndex={0}
      data-runtime-region=""
      onScroll={(event) => {
        const node = event.currentTarget;
        follows.current = node.scrollHeight - node.scrollTop - node.clientHeight < NEAR_BOTTOM_PX;
        if (metrics.current) metrics.current.scrollTop = node.scrollTop;
      }}
      className="tw:min-h-0 tw:min-w-0 tw:overflow-y-auto tw:overscroll-contain tw:rounded-[10px] tw:border tw:border-border tw:bg-card tw:focus-visible:outline-2 tw:focus-visible:-outline-offset-2 tw:focus-visible:outline-ring"
    >
      <ol className="tw:m-0 tw:list-none tw:p-0">
        {entries.map(({ key, line }) => {
          const source = [line.component, line.operation].filter(Boolean).join(".");
          const fields = runtimeFields(line);
          const message = line.message.trim() || t("log.rawlog.noMessage");
          return (
            <li
              key={key}
              data-runtime-row=""
              data-runtime-key={key}
              className="tw:min-w-0 tw:border-b tw:border-border/50 tw:px-2 tw:py-1 tw:font-mono tw:text-xs tw:leading-[1.4] tw:last:border-b-0"
            >
              <time dateTime={line.at} title={line.at} className="tw:mr-2 tw:whitespace-nowrap tw:tabular-nums tw:text-muted-foreground">
                {logTime(line.at)}
              </time>
              <span className={`tw:mr-2 tw:font-semibold ${LEVEL_TONE[line.level]}`}>{line.level.toUpperCase()}</span>
              {source && <code className="tw:mr-2 tw:wrap-anywhere tw:text-muted-foreground">{source}</code>}
              <span className="tw:wrap-anywhere">{message}</span>
              {line.work_item_id && (
                <button
                  type="button"
                  data-runtime-action="view-task"
                  onClick={() => onViewTask(line.work_item_id!)}
                  className="tw:ml-2 tw:cursor-pointer tw:whitespace-nowrap tw:rounded-[0.25rem] tw:border tw:border-border tw:bg-transparent tw:px-1.5 tw:py-0 tw:font-sans tw:text-badge tw:leading-4 tw:text-primary tw:hover:bg-accent"
                >
                  {t("log.rawlog.viewTask")}
                </button>
              )}
              {fields.length > 0 && (
                <div className="tw:wrap-anywhere tw:text-muted-foreground">
                  {fields.map((field) => (
                    <span key={field.key} data-runtime-field={field.key} className="tw:mr-2">
                      <span title={field.key}>{field.key}</span>=<span>{String(field.value)}</span>
                    </span>
                  ))}
                </div>
              )}
            </li>
          );
        })}
      </ol>
    </div>
  );
}
