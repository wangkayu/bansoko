import { useEffect, useState, type RefObject } from "react";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { isAuthedImagePath, useAuthedImageState } from "../../../lib/useAuthedImage";



/** The poster candidate chain (shared by cards and the config dialog's title area): the provider
 *  cover proxy first, the BGM poster proxy as fallback, accepting only same-origin /api/v1 paths;
 *  when the current source fails (a session fetch or an <img> error) it falls to the next.
 *  A false `hasCandidate` means the entry has no loadable image source at all. With containerRef
 *  passed, requests happen only while it nears the scroll viewport. */
export function usePosterSource(entry: CatalogEntryView, containerRef?: RefObject<Element | null>) {
  const ordered = [...new Set([entry.coverUrl, entry.posterUrl].filter(isAuthedImagePath))];
  // Failures are recorded by URL rather than position: after the entry changes (a polling refresh)
//   a failed source is not retried, and a newly appearing source is untouched by the old failure.
  const [failedUrls, setFailedUrls] = useState<ReadonlySet<string>>(new Set());
  const current = ordered.find((url) => !failedUrls.has(url));
  const { src, failed: fetchFailed } = useAuthedImageState(current, containerRef);
  useEffect(() => {
    if (fetchFailed && current) {
      setFailedUrls((prev) => new Set(prev).add(current));
    }
  }, [fetchFailed, current]);
  const onImageError = () => {
    if (current) {
      setFailedUrls((prev) => new Set(prev).add(current));
    }
  };
  return { src, hasCandidate: ordered.length > 0, onImageError };
}
