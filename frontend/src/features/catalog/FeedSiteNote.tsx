import { useQuery } from "@tanstack/react-query";
import { listFeedSites } from "../../lib/api/client";
import { contentDetectedSites, feedSiteForHost, feedUrlHost } from "../../lib/feedSites";
import { catalogKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";
import { joinList } from "../../lib/i18n/format";

/**
 * Says whether a custom feed URL's host has a parsing adapter. The list is the
 * server's declaration (GET /catalog/feed-sites); nothing is shown until it is
 * known or when the URL is not an absolute HTTP(S) address. On an unlisted host
 * it names the architectures detected from the feed's content (UNIT3D). Never
 * blocks input.
 */
export function FeedSiteNote({ url, className }: { url: string | null | undefined; className?: string }) {
  const host = feedUrlHost(url);
  const sites = useQuery({
    queryKey: catalogKeys.feedSites,
    queryFn: listFeedSites,
    enabled: host !== undefined,
    staleTime: Infinity,
    retry: false,
  });
  // The response is an external boundary: an unreadable list shows nothing
  // rather than a wrong label.
  const list = sites.data?.sites;
  if (!host || !Array.isArray(list)) return null;
  const site = feedSiteForHost(list, host);
  const detected = contentDetectedSites(list).map((entry) => entry.name);
  const unsupported = detected.length > 0 ? t("feedSite.unsupportedDetected", { names: joinList(detected) }) : t("feedSite.unsupported");
  return (
    <p
      className={["text-caption tw:m-0", site ? "tw:text-muted-foreground" : "tw:text-warning", className].filter(Boolean).join(" ")}
      data-supported={site ? "true" : "false"}
    >
      {site ? t("feedSite.supported", { name: site.name }) : unsupported}
    </p>
  );
}
