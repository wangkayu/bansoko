import { LoaderCircle, RotateCw } from "lucide-react";
import { UpstreamStatusBlock } from "../../ui/UpstreamStatusBlock";
import { Button } from "../../ui";
import { HelpTip } from "../../ui/base";
import { t } from "../../lib/i18n";
import { useDraftSubgroupDiscovery, type DraftSubgroupDiscovery, type DraftSubgroupDiscoverySource } from "./useDraftSubgroupDiscovery";

export function DraftSubgroupDiscoveryButton({ discovery, showHint = false }: {
  discovery?: DraftSubgroupDiscovery;
  /** The explanatory text is only needed on the subgroups tab; the RSS tab keeps only the button. */
  showHint?: boolean;
}) {
  if (!discovery) return null;
  return (
    <span className="tw:inline-flex tw:items-center tw:gap-1.5">
      <Button size="sm" variant="secondary" disabled={discovery.discovering} onClick={() => void discovery.discover()}>
        {discovery.discovering ? (
          <LoaderCircle className="draft-refresh-icon" />
        ) : (
          <RotateCw className="draft-refresh-icon" />
        )}
        {discovery.discovering ? t("act.refreshing") : t("config.discovery.refresh")}
      </Button>
      {showHint && (
        <HelpTip label={t("anime.draftRefresh")}>
          {t("anime.draftRefresh")}
        </HelpTip>
      )}
    </span>
  );
}

export function DraftSubgroupDiscoveryStatus({ discovery }: { discovery?: DraftSubgroupDiscovery }) {
  if (!discovery) return null;
  return (
    <UpstreamStatusBlock
      status={discovery.upstream}
      emptyTitle={t("config.discovery.none")}
      onRetry={() => void discovery.discover()}
      retryDisabled={discovery.discovering}
    />
  );
}

export function DraftSubgroupRefresh(props: DraftSubgroupDiscoverySource & {
  showHint?: boolean;
}) {
  const discovery = useDraftSubgroupDiscovery(props);
  return (
    <div className="tw:min-w-0">
      <DraftSubgroupDiscoveryButton discovery={discovery} showHint={props.showHint ?? true} />
      <DraftSubgroupDiscoveryStatus discovery={discovery} />
    </div>
  );
}
