import { useState } from "react";
import { toast } from "../../ui/toast";
import { LoaderCircle, RotateCw } from "lucide-react";
import {
  discoverProviderEntryGroups,
  toUserMessage,
  type ProviderGroupDiscoveryGroup,
  type RSSListObservationRef,
  type UpstreamStatus,
} from "../../lib/api/client";
import { nonErrorUpstream, UpstreamStatusBlock } from "../../ui/UpstreamStatusBlock";
import { Button } from "../../ui";
import { HelpTip } from "../../ui/base";
import { t } from "../../lib/i18n";

// Proactive subgroup fetching for an unsubscribed entry: only the "refresh subgroups" button
// remains. Discovery is observation, not preference: discovered groups are handed upward as
// observed rows (SelectionOverrideFields's observed) and never written into the draft's
// entries, so no score override is manufactured; only an explicit user require/exclude choice
// produces an entry. Seeds are not shown here (seed viewing belongs to the RSS preview tab).
// Discovery itself creates neither a subscription nor a source.
export function DraftSubgroupRefresh({
  provider,
  providerEntryRef,
  onDiscovered,
  showHint = true,
}: {
  provider: "anibt" | "mikan";
  providerEntryRef: string;
  /** observation is the snapshot identity this discovery persisted: the RSS preview uses it to list subgroups not yet configured. */
  onDiscovered: (groups: ProviderGroupDiscoveryGroup[], observation: RSSListObservationRef) => void;
  /** The explanatory text is only needed on the subgroups tab; the RSS tab keeps only the button. */
  showHint?: boolean;
}) {
  const [discovering, setDiscovering] = useState(false);
  const [upstream, setUpstream] = useState<UpstreamStatus>();

  async function discover() {
    if (discovering) return;
    setDiscovering(true);
    try {
      const result = await discoverProviderEntryGroups(provider, providerEntryRef);
      setUpstream(result.upstream);
      if (result.snapshot_version !== undefined) onDiscovered(result.groups, {
        provider: result.provider,
        provider_entry_ref: result.provider_entry_ref,
        snapshot_version: result.snapshot_version,
      });
      if (result.upstream && result.upstream.state !== "available") return;
      if (result.groups.length === 0) {
        toast.info(t("config.discovery.none"));
      } else {
        toast.success(t("config.discovery.found", { n: result.groups.length }));
      }
    } catch (cause) {
      const status = nonErrorUpstream(cause);
      setUpstream(status);
      if (!status) toast.error(toUserMessage(cause, t("config.discovery.failed")));
    } finally {
      setDiscovering(false);
    }
  }

  return (
    <div className="tw:min-w-0">
    <span className="tw:inline-flex tw:items-center tw:gap-1.5">
      <Button size="sm" variant="secondary" disabled={discovering} onClick={() => void discover()}>
        {discovering ? (
          <LoaderCircle className="draft-refresh-icon" />
        ) : (
          <RotateCw className="draft-refresh-icon" />
        )}
        {discovering ? t("act.refreshing") : t("config.discovery.refresh")}
      </Button>
      {showHint && (
        <HelpTip label={t("anime.draftRefresh")}>
          {t("anime.draftRefresh")}
        </HelpTip>
      )}
    </span>
    <UpstreamStatusBlock status={upstream} emptyTitle={t("config.discovery.none")} onRetry={() => void discover()} retryDisabled={discovering} />
    </div>
  );
}
