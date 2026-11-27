import { useState } from "react";
import {
  discoverProviderEntryGroups,
  toUserMessage,
  type ProviderGroupDiscoveryGroup,
  type RSSListObservationRef,
  type UpstreamStatus,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { toast } from "../../ui/toast";
import { nonErrorUpstream } from "../../ui/UpstreamStatusBlock";

export type DraftSubgroupDiscoverySource = {
  provider: "anibt" | "mikan";
  providerEntryRef: string;
  onDiscovered: (groups: ProviderGroupDiscoveryGroup[], observation: RSSListObservationRef) => void;
};

export type DraftSubgroupDiscovery = {
  discovering: boolean;
  upstream?: UpstreamStatus;
  discover: () => Promise<void>;
};

// Discovery supplies observed groups and the snapshot identity for RSS preview; it never edits the draft.
export function useDraftSubgroupDiscovery(source?: DraftSubgroupDiscoverySource): DraftSubgroupDiscovery | undefined {
  const [discovering, setDiscovering] = useState(false);
  const [upstream, setUpstream] = useState<UpstreamStatus>();
  async function discover() {
    if (discovering || !source) return;
    const { provider, providerEntryRef, onDiscovered } = source;
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

  return source ? { discovering, upstream, discover } : undefined;
}
