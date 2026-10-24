import { useRef, useState } from "react";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { usePosterSource } from "../catalog/card";
import { Dialog } from "../../ui/base";
import { t } from "../../lib/i18n";

/** The anime config title area's left poster: the same candidate chain as the cards (provider
 *  cover first, BGM poster as fallback). An entry with no image source at all takes no space;
 *  while loading or after total failure it is a quiet, non-clickable placeholder; with an image
 *  it is a button that opens the full image in a nested dialog (Esc / overlay / close button
 *  close only the image). */
export function HeaderPoster({ entry }: { entry: CatalogEntryView }) {
  const slotRef = useRef<HTMLSpanElement>(null);
  const { src, hasCandidate, onImageError } = usePosterSource(entry, slotRef);
  const [open, setOpen] = useState(false);
  if (!hasCandidate) return null;
  return (
    <span ref={slotRef} className="anime-config-head-poster">
      {src ? (
        <>
          <button type="button" className="anime-config-head-poster-open" aria-label={t("config.viewPoster")} onClick={() => setOpen(true)}>
            <img src={src} alt="" className="anime-config-head-poster-image" onError={onImageError} />
          </button>
          <Dialog
            open={open}
            onRequestClose={() => setOpen(false)}
            tier="M"
            fixedHeight
            focusTitle
            bodyLayout="fill"
            title={
              <span className="tw:block tw:truncate" title={entry.title}>
                {entry.title}
              </span>
            }
          >
            <div className="anime-config-poster-view">
              <img src={src} alt={entry.title} className="anime-config-poster-view-image" />
            </div>
          </Dialog>
        </>
      ) : (
        <span aria-hidden className="poster-fallback" />
      )}
    </span>
  );
}
