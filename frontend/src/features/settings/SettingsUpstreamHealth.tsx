import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { getSystemUpstreamHealth, toUserMessage, type UpstreamOutcomeCounts } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { formatDateTime } from "../../lib/labels";
import { Button, StateBlock, StatePill } from "../../ui/base";
import { useUpstreamWait } from "../../ui/UpstreamStatusBlock";
import type { SystemUpstreamHealth } from "../../lib/api/client";

const outcomes = ["available", "empty", "not_indexed", "rate_limited", "cooldown", "error"] as const;

export function UpstreamCounts({ counts }: { counts: UpstreamOutcomeCounts }) {
  return (
    <dl className="tw:flex tw:flex-wrap tw:gap-x-4 tw:gap-y-1 text-caption" aria-label={t("upstream.health.outcomes")}>
      {outcomes.map((state) => (
        <div key={state} className="tw:flex tw:gap-1">
          <dt>{t(`upstream.outcomes.${state}`)}</dt>
          <dd className="tw:font-medium tw:tabular-nums">{counts[state]}</dd>
        </div>
      ))}
    </dl>
  );
}

function QuotaRow({ quota, checkedAt }: { quota: SystemUpstreamHealth["quotas"][number]; checkedAt: string }) {
  const seconds = useUpstreamWait({ state: "cooldown", checked_at: checkedAt, cooldown_until: quota.cooldown_until });
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-1 tw:border-b tw:border-border tw:pb-2">
      <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-3 tw:gap-y-1">
        <span className="text-body tw:break-all">{quota.bucket}</span>
        {seconds > 0 ? (
          <span data-upstream-state="cooldown" className="tw:inline-flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1">
            <StatePill tone="info">{t("upstream.outcomes.cooldown")}</StatePill>
            <span className="text-caption tw:tabular-nums">{t("upstream.waitSeconds", { n: seconds })}</span>
          </span>
        ) : <StatePill tone="muted">{t("upstream.health.cooldownEnded")}</StatePill>}
      </div>
      <p className="text-caption">{t("upstream.health.lastLimited", { at: formatDateTime(quota.last_limited_at) })}</p>
    </div>
  );
}

export function SettingsUpstreamHealth() {
  const health = useQuery({ queryKey: ["system", "upstream-health"], queryFn: getSystemUpstreamHealth });
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-5">
      <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-3">
        <h2 className="text-title">{t("upstream.health.title")}</h2>
        <Button size="sm" variant="secondary" disabled={health.isFetching} onClick={() => void health.refetch()}>
          <RefreshCw size={14} /> {t("upstream.health.refresh")}
        </Button>
      </div>
      {health.isPending && <StateBlock kind="busy" title={t("upstream.health.loading")} />}
      {health.isError && <StateBlock kind="error" title={toUserMessage(health.error, t("upstream.health.failed"))} />}
      {health.data && (
        <>
          <section className="tw:grid tw:gap-2">
            <h3 className="text-body tw:font-semibold">{t("upstream.health.quotas")}</h3>
            {health.data.quotas.length === 0 && <p className="text-caption">{t("upstream.health.noQuota")}</p>}
            {health.data.quotas.map((quota) => (
              <QuotaRow key={quota.bucket} quota={quota} checkedAt={health.data.checked_at} />
            ))}
          </section>
          <section className="tw:grid tw:gap-2">
            <h3 className="text-body tw:font-semibold">{t("upstream.health.refreshes")}</h3>
            {health.data.refreshes.length === 0 && <p className="text-caption">{t("upstream.health.noRefresh")}</p>}
            {health.data.refreshes.map((row) => (
              <div key={`${row.provider}:${row.kind}`} className="tw:grid tw:gap-1 tw:border-b tw:border-border tw:pb-2">
                <p className="text-body">{row.provider} · {t(`upstream.health.${row.kind}`)}</p>
                <p className="text-caption">{t("upstream.health.lastAttempt", { at: formatDateTime(row.last_attempt_at) })}</p>
                {row.last_success_at && <p className="text-caption">{t("upstream.health.lastSuccess", { at: formatDateTime(row.last_success_at) })}</p>}
                <UpstreamCounts counts={row.outcomes} />
              </div>
            ))}
          </section>
        </>
      )}
    </div>
  );
}
