import { useState } from "react";
import { ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Button, Select } from "../ui/base/controls";
import { StateBlock } from "../ui/StateBlock";
import { getRuntimeLog, toUserMessage, type RuntimeLogLine } from "../lib/api/client";
import { observabilityKeys } from "../lib/queryKeys";
import { downloadTextFile } from "../features/settings/agentSetup";
import { t } from "../lib/i18n";

// Raw log sub-view of the diagnostics tab: the server's in-memory runtime log (redacted,
// cleared on restart), newest last. Copy and download build plain text from the lines shown.

const LINE_LIMIT = 500;

type Level = "all" | "warn" | "error";

/** One plain-text line: time, level, source, message, then the structured fields that are present. */
export function rawLogText(lines: readonly RuntimeLogLine[]): string {
  return lines
    .map((line) => {
      const fields = (
        [
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

export function LogRawView({ onBack }: { onBack: () => void }) {
  const [level, setLevel] = useState<Level>("all");
  const [copied, setCopied] = useState(false);
  const query = useQuery({
    queryKey: observabilityKeys.runtimeLog(level),
    queryFn: () => getRuntimeLog({ level, limit: LINE_LIMIT }),
  });
  const lines = query.data?.lines ?? [];
  const text = rawLogText(lines);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      globalThis.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  let body: React.ReactNode;
  if (query.error) {
    body = (
      <StateBlock
        kind="error"
        title={toUserMessage(query.error, t("log.rawlog.loadFailed"))}
        action={
          <Button size="sm" variant="link" onClick={() => void query.refetch()}>
            {t("log.retry")}
          </Button>
        }
      />
    );
  } else if (!query.data) {
    body = <StateBlock kind="busy" title={t("log.rawlog.loading")} />;
  } else if (lines.length === 0) {
    body = <StateBlock kind="empty" title={t("log.rawlog.empty")} />;
  } else {
    body = (
      <pre className="tw:m-0 tw:max-h-[50vh] tw:overflow-auto tw:whitespace-pre-wrap tw:break-all tw:rounded-[10px] tw:border tw:border-border tw:bg-muted/30 tw:p-3 tw:font-mono tw:text-xs">
        {text}
      </pre>
    );
  }

  return (
    <div className="tw:grid tw:gap-3">
      <Button size="sm" variant="link" className="tw:justify-self-start" onClick={onBack}>
        <ArrowLeft aria-hidden className="tw:size-3.5" />
        {t("log.rawlog.back")}
      </Button>
      <h3 className="tw:m-0 tw:text-sm tw:font-medium">{t("log.rawlog.title")}</h3>
      <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("log.rawlog.note", { n: LINE_LIMIT })}</p>
      <div className="tw:flex tw:items-center tw:gap-2">
        <div className="tw:min-w-0 tw:flex-1">
          <Select<Level>
            label={t("log.rawlog.level")}
            value={level}
            options={[
              { value: "all", label: t("log.rawlog.levelAll") },
              { value: "warn", label: t("log.rawlog.levelWarn") },
              { value: "error", label: t("log.rawlog.levelError") },
            ]}
            onChange={setLevel}
          />
        </div>
        <Button disabled={lines.length === 0} onClick={() => void copy()}>
          {copied ? t("log.rawlog.copied") : t("log.rawlog.copy")}
        </Button>
        <Button disabled={lines.length === 0} onClick={() => downloadTextFile("bansoko-log.txt", text, "text/plain")}>
          {t("log.rawlog.download")}
        </Button>
      </div>
      {body}
    </div>
  );
}
