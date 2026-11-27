import { useEffect, useState } from "react";
import { ApiError, toUserMessage, type UpstreamStatus } from "../lib/api/client";
import { t } from "../lib/i18n";
import { Button } from "./base/controls";
import { StateBlock } from "./StateBlock";

export function nonErrorUpstream(cause: unknown): UpstreamStatus | undefined {
  if (!(cause instanceof ApiError)) return undefined;
  if (cause.upstream && cause.upstream.state !== "error") return cause.upstream;
  const state = cause.status === 429 && cause.code === "PROVIDER_RATE_LIMITED" ? "rate_limited"
    : cause.status === 429 && cause.code === "PROVIDER_COOLDOWN" ? "cooldown"
    : cause.status === 404 && cause.code === "ANIME_NOT_FOUND" ? "not_indexed"
    : undefined;
  if (!state) return undefined;
  const seconds = cause.retryAfterSeconds;
  return {
    state,
    checked_at: cause.receivedAt,
    reason_code: cause.upstream?.reason_code ?? cause.code,
    retry_after_seconds: seconds,
  };
}

export function UpstreamStatusBlock({ status, cause, emptyTitle, errorTitle, onRetry, retryDisabled }: {
  status?: UpstreamStatus;
  cause?: unknown;
  emptyTitle?: string;
  errorTitle?: string;
  onRetry?: () => void;
  retryDisabled?: boolean;
}) {
  const seconds = useUpstreamWait(status);
  const waiting = status?.state === "rate_limited" || status?.state === "cooldown";
  if (!status || status.state === "available") return null;
  const title = status.state === "empty" ? emptyTitle ?? t("search.noMatch")
    : status.state === "not_indexed" ? t("upstream.notIndexed")
    : status.state === "rate_limited" ? t("upstream.rateLimited")
    : status.state === "cooldown" ? t("upstream.cooldown")
    : toUserMessage(cause, errorTitle ?? t("upstream.error"));
  return (
    <div data-upstream-state={status.state}>
      <StateBlock
        kind={status.state === "error" ? "error" : "empty"}
        title={title}
        description={seconds > 0 ? t("upstream.waitSeconds", { n: seconds }) : undefined}
        action={onRetry && <Button size="sm" disabled={retryDisabled || seconds > 0} onClick={onRetry}>{waiting ? t("upstream.retryLater") : t("act.retry")}</Button>}
      />
    </div>
  );
}

export function useUpstreamWait(status?: UpstreamStatus): number {
  const waiting = status?.state === "rate_limited" || status?.state === "cooldown";
  const deadline = status?.cooldown_until ? Date.parse(status.cooldown_until)
    : status?.retry_after_seconds === undefined ? 0 : Date.parse(status.checked_at) + status.retry_after_seconds * 1000;
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!waiting || !Number.isFinite(deadline) || deadline <= Date.now()) return;
    const tick = () => {
      const current = Date.now();
      setNow(current);
      if (current >= deadline) window.clearInterval(timer);
    };
    const timer = window.setInterval(tick, 250);
    tick();
    return () => window.clearInterval(timer);
  }, [deadline, waiting]);
  return waiting && Number.isFinite(deadline) ? Math.max(0, Math.ceil((deadline - Math.max(now, Date.now())) / 1000)) : 0;
}
