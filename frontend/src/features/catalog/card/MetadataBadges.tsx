import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { ExternalValue } from "./ExternalValue";
import { metadataMissingLabel } from "../../../lib/labels";

/** Metadata bindings: connected Bangumi / TMDB render external links to the id; missing links merge
 *  into one phrase, using the same wording as the card chip (the copy guide's automation-blocking
 *  reasons). For a subscribed entry what is missing follows the server's metadata_hold.missing; an
 *  unsubscribed entry follows the bindings themselves. Used in the config dialog's title area. */
export function MetadataBadges({ entry }: { entry: CatalogEntryView }) {
  const subscribed = entry.subscriptionState !== undefined;
  const { bgmId, tmdbWorkId, tmdbSeason } = entry.metadataMatch;
  const holdMissing = entry.metadataHold?.held ? entry.metadataHold.missing : [];
  const bgmMissing = holdMissing.includes("bgm") || (!subscribed && !bgmId);
  const tmdbMissing = holdMissing.includes("tmdb") || (!subscribed && !tmdbWorkId);
  const missing = [...(bgmMissing ? ["bgm"] : []), ...(tmdbMissing ? ["tmdb"] : [])];

  return (
    <span className="catalog-metadata-badges">
      {bgmId ? (
        <span className="catalog-metadata-group">
          <span className="text-badge catalog-metadata-label">Bangumi</span>
          <ExternalValue href={`https://bgm.tv/subject/${bgmId}`}>{bgmId}</ExternalValue>
        </span>
      ) : !bgmMissing ? (
        <span className="catalog-metadata-group">
          <span className="text-badge catalog-metadata-label">Bangumi</span>
          <span className="catalog-metadata-empty">-</span>
        </span>
      ) : null}
      {tmdbWorkId ? (
        <span className="catalog-metadata-group">
          <span className="text-badge catalog-metadata-label">TMDB</span>
          <ExternalValue href={`https://www.themoviedb.org/tv/${tmdbWorkId}`}>
            {tmdbWorkId}
            {tmdbSeason !== undefined ? ` · S${tmdbSeason}` : ""}
          </ExternalValue>
        </span>
      ) : !tmdbMissing ? (
        <span className="catalog-metadata-group">
          <span className="text-badge catalog-metadata-label">TMDB</span>
          <span className="catalog-metadata-empty">-</span>
        </span>
      ) : null}
      {missing.length > 0 && (
        <span className="catalog-metadata-group">
          <span className="catalog-metadata-missing">{metadataMissingLabel(missing)}</span>
        </span>
      )}
    </span>
  );
}
