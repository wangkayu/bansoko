import { useEffect, useRef, useState } from "react";
import { Copy, Download, Search } from "lucide-react";
import { Button, Input, Select } from "../ui/base/controls";
import { StateBlock } from "../ui/StateBlock";
import { toUserMessage, type RuntimeLogLine } from "../lib/api/client";
import { downloadTextFile } from "../features/settings/agentSetup";
import { t } from "../lib/i18n";
import { RuntimeLogRows } from "./RuntimeLogRows";
import { RUNTIME_LOG_PAGE_LIMIT } from "./runtimeLogFeed";
import { useRuntimeLogFeed, type RuntimeLogLevel } from "./useRuntimeLogFeed";

// The runtime log tab reads the retained on-disk history: the newest matching
// page first, "load older" backwards, and a 5s cursor-based incremental read
// while the tab is visible. Level, text and work-item filters are server
// filters, so every loaded page holds matching lines. Copy and download build
// plain text from the lines currently shown, not from the whole retained
// history.

export const RUNTIME_LOG_LINE_LIMIT = RUNTIME_LOG_PAGE_LIMIT;
const SEARCH_DEBOUNCE_MS = 300;

type Level = RuntimeLogLevel;

/** One plain-text line: time, level, source, message, then the structured fields that are present. */
export function rawLogText(lines: readonly RuntimeLogLine[]): string {
  return lines
    .map((line) => {
      const fields = (
        [
          ["kind", line.kind],
          ["work_item_id", line.work_item_id],
          ["stage", line.stage],
          ["state_before", line.state_before],
          ["state_after", line.state_after],
          ["disposition", line.disposition],
          ["owner_type", line.owner_type],
          ["attempt", line.attempt === undefined ? undefined : String(line.attempt)],
          ["duration_ms", line.duration_ms === undefined ? undefined : String(line.duration_ms)],
          ["retryable", line.retryable === undefined ? undefined : String(line.retryable)],
          ["outcome", line.outcome],
          ["reason_code", line.reason_code],
          ["error_class", line.error_class],
          ["request_id", line.request_id],
          ["correlation_id", line.correlation_id],
        ] as const
      )
        .filter(([, value]) => !!value)
        .map(([key, value]) => `${key}=${value}`);
      const source = [line.component, line.operation].filter(Boolean).join(".");
      return [line.at, line.level.toUpperCase(), source, line.message, ...fields].filter(Boolean).join(" ");
    })
    .join("\n");
}

function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = globalThis.setTimeout(() => setDebounced(value), delay);
    return () => globalThis.clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

export function LogRawView({ active }: { active: boolean }) {
  const [level, setLevel] = useState<Level>("all");
  const [search, setSearch] = useState("");
  const [workItemId, setWorkItemId] = useState("");
  // The filter to restore when leaving a one-click task trace.
  const savedFilters = useRef<{ level: Level; search: string } | null>(null);
  const [copied, setCopied] = useState(false);
  const q = useDebounced(search.trim(), SEARCH_DEBOUNCE_MS);
  // A task trace deliberately asks for all levels and no text filter; q may
  // still be inside its debounce window, so ignore it while tracing.
  const effectiveQ = workItemId === "" ? q : "";
  const feed = useRuntimeLogFeed({ active, level, q: effectiveQ, workItemId });
  const lines = feed.lines.map((entry) => entry.line);
  const text = rawLogText(lines);
  // Until the debounce matches the input, `text` still belongs to the previous
  // search; copy and download stay disabled so they can never save non-matching lines.
  const pending = workItemId === "" && search.trim() !== q;
  const filtered = level !== "all" || search.trim() !== "" || workItemId !== "";
  const scopeKey = `${level}\u0000${effectiveQ}\u0000${workItemId}`;

  useEffect(() => {
    if (!copied) return;
    const timer = globalThis.setTimeout(() => setCopied(false), 1500);
    return () => globalThis.clearTimeout(timer);
  }, [copied]);

  const changeLevel = (next: Level) => {
    if (workItemId !== "") {
      savedFilters.current = null;
      setWorkItemId("");
    }
    setLevel(next);
  };

  const changeSearch = (next: string) => {
    if (workItemId !== "") {
      savedFilters.current = null;
      setWorkItemId("");
    }
    setSearch(next);
  };

  const viewTask = (id: string) => {
    if (workItemId === "") savedFilters.current = { level, search };
    setLevel("all");
    setSearch("");
    setWorkItemId(id);
  };

  const returnToFilters = () => {
    const saved = savedFilters.current;
    savedFilters.current = null;
    setWorkItemId("");
    setLevel(saved?.level ?? "all");
    setSearch(saved?.search ?? "");
  };

  const clearFilters = () => {
    savedFilters.current = null;
    setWorkItemId("");
    setLevel("all");
    setSearch("");
  };

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };

  let body: React.ReactNode;
  if (feed.status === "error" && lines.length === 0) {
    body = (
      <StateBlock
        kind="error"
        title={toUserMessage(feed.error, t("log.rawlog.loadFailed"))}
        action={
          <Button size="sm" variant="link" onClick={feed.retry}>
            {t("log.retry")}
          </Button>
        }
      />
    );
  } else if (feed.status === "loading" && lines.length === 0) {
    body = <StateBlock kind="busy" title={t("log.rawlog.loading")} />;
  } else if (lines.length === 0) {
    body = (
      <StateBlock
        kind="empty"
        title={t("log.rawlog.empty")}
        action={
          filtered ? (
            <Button size="sm" variant="link" onClick={clearFilters}>
              {t("log.rawlog.clearFilters")}
            </Button>
          ) : undefined
        }
      />
    );
  } else {
    body = <RuntimeLogRows entries={feed.lines} scopeKey={scopeKey} revision={feed.state.revision} onViewTask={viewTask} />;
  }

  return (
    <div className="tw:flex tw:min-h-0 tw:flex-1 tw:flex-col tw:gap-3 tw:[@media(max-height:500px)]:gap-1.5">
      <div className="tw:flex tw:shrink-0 tw:items-center tw:gap-2 tw:phone:min-h-11 tw:phone:gap-1">
        <div className="tw:w-32 tw:shrink-0 tw:phone:w-28">
          <Select<Level>
            label={t("log.rawlog.level")}
            value={level}
            options={[
              { value: "all", label: t("log.rawlog.levelAll") },
              { value: "warn", label: t("log.rawlog.levelWarn") },
              { value: "error", label: t("log.rawlog.levelError") },
            ]}
            onChange={changeLevel}
          />
        </div>
        <div className="tw:min-w-0 tw:flex-1">
          <Input
            type="search"
            aria-label={t("log.rawlog.search")}
            placeholder={t("log.rawlog.searchPlaceholder")}
            leadingIcon={<Search className="tw:size-3.5" />}
            value={search}
            maxLength={512}
            onChange={(event) => changeSearch(event.target.value)}
          />
        </div>
        <Button
          aria-label={copied ? t("log.rawlog.copied") : t("log.rawlog.copy")}
          title={copied ? t("log.rawlog.copied") : t("log.rawlog.copyHint", { n: lines.length })}
          className="tw:phone:size-9 tw:phone:p-0"
          disabled={lines.length === 0 || pending}
          onClick={() => void copy()}
        >
          <Copy size={15} aria-hidden className="tw:hidden tw:phone:block" />
          <span className="tw:phone:sr-only">{copied ? t("log.rawlog.copied") : t("log.rawlog.copy")}</span>
        </Button>
        <Button
          aria-label={t("log.rawlog.download")}
          title={t("log.rawlog.downloadHint", { n: lines.length })}
          className="tw:phone:size-9 tw:phone:p-0"
          disabled={lines.length === 0 || pending}
          onClick={() => downloadTextFile("bansoko-log.txt", text, "text/plain")}
        >
          <Download size={15} aria-hidden className="tw:hidden tw:phone:block" />
          <span className="tw:phone:sr-only">{t("log.rawlog.download")}</span>
        </Button>
      </div>

      {workItemId !== "" && (
        <div
          data-runtime-task-filter=""
          className="tw:flex tw:min-w-0 tw:shrink-0 tw:items-center tw:justify-between tw:gap-2 tw:rounded-[8px] tw:border tw:border-border tw:bg-muted/40 tw:px-2 tw:py-1 tw:text-xs"
        >
          <span className="tw:min-w-0 tw:wrap-anywhere">{t("log.rawlog.taskFilter", { id: workItemId })}</span>
          <Button size="sm" variant="link" className="tw:shrink-0" onClick={returnToFilters}>
            {t("log.rawlog.backToFilters")}
          </Button>
        </div>
      )}

      {(feed.state.gap || feed.state.sourceChanged || feed.state.source === "memory") && (
        <div role="status" className="tw:grid tw:shrink-0 tw:gap-1 tw:text-xs tw:text-muted-foreground">
          {feed.state.gap && <p className="tw:m-0">{t("log.rawlog.historyGap")}</p>}
          {feed.state.source === "memory" && <p className="tw:m-0">{t("log.rawlog.sourceMemory")}</p>}
          {feed.state.sourceChanged && feed.state.source !== "memory" && (
            <p className="tw:m-0">{t("log.rawlog.sourceChanged")}</p>
          )}
        </div>
      )}

      {lines.length > 0 && (
        <div className="tw:flex tw:shrink-0 tw:items-center tw:justify-between tw:gap-2">
          <Button
            size="sm"
            variant="ghost"
            data-runtime-action="load-older"
            disabled={feed.older === "loading" || feed.older === "done"}
            onClick={() => void feed.loadOlder()}
          >
            {feed.older === "loading"
              ? t("log.rawlog.loadingOlder")
              : feed.older === "done"
                ? t("log.rawlog.noMoreOlder")
                : t("log.rawlog.loadOlder")}
          </Button>
          <span className="tw:text-xs tw:text-muted-foreground">{t("log.rawlog.shownCount", { n: feed.lineCount })}</span>
        </div>
      )}

      {feed.pollFailed && (
        <p role="status" className="tw:m-0 tw:shrink-0 tw:text-xs tw:text-muted-foreground">
          {t("log.rawlog.refreshFailed")}
        </p>
      )}
      {body}
    </div>
  );
}
