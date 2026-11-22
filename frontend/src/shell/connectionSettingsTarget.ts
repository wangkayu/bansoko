import type { Connection } from "../lib/api/client";

/** The downloader (the qBittorrent connection) is configured in the download section. */
export const DOWNLOADER_SETTINGS = "/settings/download";

/** Settings-section target of each connection row: AniBT/Mikan have their own site
 *  sections; qB is configured under the download section; the other connections
 *  land on the connections section — with a configured row it carries a
 *  `?connection=` deep link that opens that connection's edit dialog directly. */
export function settingsTargetFor(kind: Connection["kind"], connection: Connection | undefined): string {
  if (kind === "anibt" || kind === "mikan") return `/settings/${kind}`;
  if (kind === "qbittorrent") return DOWNLOADER_SETTINGS;
  if (connection) return `/settings/connections?connection=${encodeURIComponent(connection.id)}`;
  return "/settings/connections";
}
