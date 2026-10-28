import { useEffect, useRef, useState, type RefObject } from "react";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { isAuthedImagePath, useAuthedImageState } from "../../../lib/useAuthedImage";

// A card left with nothing to show only because of transient failures retries after 15 s, 1 min,
// 4 min, then every 10 min, and at once when the window regains focus or the network comes back.
const RETRY_BASE_MS = 15_000;
const RETRY_MAX_MS = 10 * 60_000;

/** The poster candidate chain (shared by cards and the config dialog's title area): the provider
 *  cover proxy first, the BGM poster proxy as fallback, accepting only same-origin /api/v1 paths;
 *  when the current source fails (a session fetch or an <img> error) it falls to the next.
 *  A false `hasCandidate` means the entry has no loadable image source at all. With containerRef
 *  passed, requests happen only while it nears the scroll viewport. */
export function usePosterSource(entry: CatalogEntryView, containerRef?: RefObject<Element | null>) {
  const ordered = [...new Set([entry.coverUrl, entry.posterUrl].filter(isAuthedImagePath))];
  // Failures are recorded by URL rather than position: after the entry changes (a polling refresh)
  // a failed source keeps its verdict, and a newly appearing source is untouched by the old failure.
  // A 404 or a broken image fails for good; a transient failure (network, 408, 429, 5xx) only skips
  // the source until the next retry round.
  const [failedUrls, setFailedUrls] = useState<ReadonlySet<string>>(new Set());
  const [skippedUrls, setSkippedUrls] = useState<ReadonlySet<string>>(new Set());
  const [retryRound, setRetryRound] = useState(0);
  const retryStepRef = useRef(0);
  const current = ordered.find((url) => !failedUrls.has(url) && !skippedUrls.has(url));
  const { src, failed: fetchFailed, retryable } = useAuthedImageState(current, containerRef, retryRound);
  useEffect(() => {
    if (fetchFailed && current) {
      (retryable ? setSkippedUrls : setFailedUrls)((prev) => new Set(prev).add(current));
    }
  }, [fetchFailed, retryable, current]);
  useEffect(() => {
    if (src) retryStepRef.current = 0;
  }, [src]);
  // Retries only while nothing is shown, so a card already showing the fallback never flickers.
  const awaitingRetry = !current && ordered.some((url) => skippedUrls.has(url));
  useEffect(() => {
    if (!awaitingRetry) return;
    let fired = false;
    const retry = () => {
      if (fired) return;
      fired = true;
      retryStepRef.current += 1;
      setSkippedUrls(new Set());
      setRetryRound((round) => round + 1);
    };
    const retryWhenVisible = () => {
      if (document.visibilityState === "visible") retry();
    };
    const timer = window.setTimeout(retry, Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 4 ** retryStepRef.current));
    window.addEventListener("focus", retry);
    window.addEventListener("online", retry);
    document.addEventListener("visibilitychange", retryWhenVisible);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("focus", retry);
      window.removeEventListener("online", retry);
      document.removeEventListener("visibilitychange", retryWhenVisible);
    };
  }, [awaitingRetry]);
  const onImageError = () => {
    if (current) {
      setFailedUrls((prev) => new Set(prev).add(current));
    }
  };
  return { src, hasCandidate: ordered.length > 0, onImageError };
}
