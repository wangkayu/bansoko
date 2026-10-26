import { describe, expect, it } from "vitest";
import type { FeedSite } from "./api/client";
import { feedSiteForHost, feedUrlHost } from "./feedSites";

const sites: FeedSite[] = [
  { id: "nyaa", name: "nyaa", hosts: ["nyaa.si"], identity: "feed", match: "host" },
  { id: "miobt", name: "MioBT", hosts: ["miobt.com", "comicat.org", "kisssub.org"], identity: "feed", match: "host" },
  { id: "unit3d", name: "UNIT3D", hosts: [], identity: "expansion", match: "content" },
];

describe("feed site matching", () => {
  it("matches a declared host and its subdomains", () => {
    expect(feedSiteForHost(sites, "nyaa.si")?.id).toBe("nyaa");
    expect(feedSiteForHost(sites, "sukebei.nyaa.si")?.id).toBe("nyaa");
    expect(feedSiteForHost(sites, "www.comicat.org")?.id).toBe("miobt");
  });

  it("never matches a content-detected architecture by host", () => {
    expect(feedSiteForHost(sites, "tracker.example.test")).toBeUndefined();
  });

  it("does not match a lookalike host", () => {
    expect(feedSiteForHost(sites, "notnyaa.si")).toBeUndefined();
    expect(feedSiteForHost(sites, "share.acgnx.se")).toBeUndefined();
  });

  it("reads the host only from absolute HTTP(S) URLs", () => {
    expect(feedUrlHost(" https://Nyaa.si/?page=rss ")).toBe("nyaa.si");
    expect(feedUrlHost("magnet:?xt=urn:btih:abc")).toBeUndefined();
    expect(feedUrlHost("nyaa.si/rss")).toBeUndefined();
    expect(feedUrlHost("")).toBeUndefined();
  });
});
