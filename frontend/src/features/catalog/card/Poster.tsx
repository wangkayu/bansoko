import { useRef } from "react";
import { Clapperboard } from "lucide-react";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { usePosterSource } from "./usePosterSource";

/** The shared poster atom: renders by the priority candidate chain; when the current source fails
 *  to load (a session fetch failure or an <img> error) it falls to the next, and only after all fail
 *  does a brand-colored gradient + the title's first character take over — never a flat grey block.
 *  Candidates accept only same-origin /api/v1 image paths (the poster proxy, the cover proxy);
 *  third-party URLs are ignored outright.
 *  Every card shares one order: the provider cover proxy first (the original is larger), the BGM
 *  poster proxy as fallback — an entry without a BGM binding still has an image; a custom
 *  subscription has no provider cover and naturally lands on the meta poster.
 *  Images are requested only when a card is close to the scroll viewport (in-viewport ones take
 *  priority). The caller owns the container shape (poster-wall aspect-[2/3] / the wide card's left
 *  slot); this component fills the container. */
export function Poster({
  entry,
  scrim = false,
}: {
  entry: CatalogEntryView;
  scrim?: boolean;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const { src: posterSrc, onImageError } = usePosterSource(entry, containerRef);
  const initial = entry.title.trim().charAt(0);
  return (
    <div ref={containerRef} className="poster">
      {posterSrc ? (
        <img
          src={posterSrc}
          alt={entry.title}
          className="poster-image"
          loading="lazy"
          onError={onImageError}
        />
      ) : (
        <div className="poster-fallback">
          {initial ? (
            <span aria-hidden className="poster-initial">
              {initial}
            </span>
          ) : (
            <Clapperboard className="poster-icon" />
          )}
        </div>
      )}
      {/* The bottom gradient: a readability backing for the poster-wall card's info strip (wide
          cards keep their info on the right and need none). The backing color goes through
          --card-scrim: a dark scrim on the light theme (see the :root comment), a dark base on dark. */}
      {scrim && (
        <div className="poster-scrim card-scrim" />
      )}
    </div>
  );
}
