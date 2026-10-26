import type { FeedSite } from "./api/client";

/** The lower-cased host of an absolute HTTP(S) feed URL, or undefined. */
export function feedUrlHost(raw: string | null | undefined): string | undefined {
  const value = raw?.trim();
  if (!value) return undefined;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    const host = parsed.hostname.toLowerCase().replace(/\.$/, "");
    return host || undefined;
  } catch {
    return undefined;
  }
}

/**
 * The declared site whose adapter parses a feed on this host: the host equals a
 * declared host or is a subdomain of one. Mirrors the server's selection.
 */
export function feedSiteForHost(sites: readonly FeedSite[], host: string): FeedSite | undefined {
  return sites.find(
    (site) => site.match !== "content" && Array.isArray(site.hosts) && site.hosts.some((declared) => host === declared || host.endsWith(`.${declared}`)),
  );
}

