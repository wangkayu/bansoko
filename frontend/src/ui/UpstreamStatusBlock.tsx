import { useEffect, useMemo, useState } from "react";
import { ApiError, toUserMessage, type UpstreamStatus } from "../lib/api/client";
import { t } from "../lib/i18n";
import { Alert, Button } from "./base";
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
    receivedAtMs: Date.parse(cause.receivedAt),
    reason_code: cause.upstream?.reason_code ?? cause.code,
    retry_after_seconds: seconds,
  };
}

/** True only for real upstream interruptions/blocks; normal `available` and `empty` belong to each surface's own UI. */
export function isUpstreamNotice(status?: UpstreamStatus): status is UpstreamStatus {
  return Boolean(status && status.state !== "available" && status.state !== "empty");
}

function noticeTitle(status: UpstreamStatus, cause?: unknown, errorTitle?: string): string {
  return status.state === "not_indexed" ? t("upstream.notIndexed")
    : status.state === "rate_limited" ? t("upstream.rateLimited")
    : status.state === "cooldown" ? t("upstream.cooldown")
    : toUserMessage(cause, errorTitle ?? t("upstream.error"));
}

export function UpstreamStatusBlock({ status, cause, errorTitle, variant = "banner", onRetry, retryLabel, retryDisabled }: {
  status?: UpstreamStatus;
  cause?: unknown;
  errorTitle?: string;
  /** `banner` renders a compact Alert for regions with existing controls or content; `state` replaces an empty region's sole StateBlock. */
  variant?: "banner" | "state";
  onRetry?: () => void;
  retryLabel?: string;
  retryDisabled?: boolean;
}) {
  const seconds = useUpstreamWait(status);
  if (!isUpstreamNotice(status)) return null;
  const waiting = status.state === "rate_limited" || status.state === "cooldown";
  const title = noticeTitle(status, cause, errorTitle);
  const description = seconds > 0 ? t("upstream.waitSeconds", { n: seconds }) : undefined;
  const action = onRetry ? (
    <Button size="sm" disabled={retryDisabled} onClick={onRetry}>
      {retryLabel ?? (waiting ? t("upstream.retryLater") : t("act.retry"))}
    </Button>
  ) : undefined;
  if (variant === "state") {
    return (
      <div data-upstream-state={status.state}>
        <StateBlock
          kind={status.state === "error" ? "error" : "empty"}
          title={title}
          description={description}
          action={action}
        />
      </div>
    );
  }
  const tone = status.state === "error" ? "error" : waiting ? "warning" : "info";
  return (
    <div data-upstream-state={status.state}>
      <Alert tone={tone} title={title} description={description} action={action} />
    </div>
  );
}

/** Compact single-line upstream notice for use inside a list row (e.g. a clickable subgroup entry). */
export function UpstreamRowNotice({ status, cause, errorTitle }: {
  status?: UpstreamStatus;
  cause?: unknown;
  errorTitle?: string;
}) {
  const seconds = useUpstreamWait(status);
  if (!isUpstreamNotice(status)) return null;
  const title = noticeTitle(status, cause, errorTitle);
  const text = seconds > 0 ? `${title} · ${t("upstream.waitSeconds", { n: seconds })}` : title;
  return (
    <span
      data-upstream-state={status.state}
      className={status.state === "error" ? "tw:text-xs tw:text-destructive" : "tw:text-xs tw:text-muted-foreground"}
    >
      {text}
    </span>
  );
}

export function useUpstreamWait(status?: UpstreamStatus): number {
  const waiting = status?.state === "rate_limited" || status?.state === "cooldown";
  const receipt = status?.receivedAtMs;
  const checkedAt = status?.checked_at;
  const cooldownUntil = status?.cooldown_until;
  const seconds = status?.retry_after_seconds;
  const state = status?.state;
  // A new observation without a receipt time starts a new countdown, even if its duration is unchanged.
  const deadline = useMemo(() => {
    if (seconds !== undefined && Number.isFinite(seconds)) return (receipt ?? Date.now()) + seconds * 1000;
    return cooldownUntil ? Date.parse(cooldownUntil) : 0;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- checkedAt and state identify the observation.
  }, [receipt, checkedAt, cooldownUntil, seconds, state]);
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
