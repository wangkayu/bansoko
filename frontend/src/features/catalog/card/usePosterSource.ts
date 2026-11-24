import { useEffect, useRef, useState, type RefObject } from "react";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { isAuthedImagePath, useAuthedImageState } from "../../../lib/useAuthedImage";

// A card left with nothing to show only because of transient failures retries after 15 s, 1 min,
// 4 min, then every 10 min, and at once when the window regains focus or the network comes back.
const RETRY_BASE_MS = 15_000;
const RETRY_MAX_MS = 10 * 60_000;

export type PosterImageSize = "thumb" | "full";

/** Appends `?size=thumb` to same-origin cover/poster proxy paths; full-size or other paths stay verbatim. */
export function withImageSize(path: string, size: PosterImageSize): string {
  if (size !== "thumb") return path;
  if (!path.startsWith("/api/v1/images/cover/") && !path.startsWith("/api/v1/images/poster/")) return path;
  if (/[?&]size=/.test(path)) return path;
  return `${path}${path.includes("?") ? "&" : "?"}size=thumb`;
}

/** The poster candidate chain (shared by cards and the config dialog's title area): the provider
 *  cover proxy first, the BGM poster proxy as fallback, accepting only same-origin /api/v1 paths;
 *  when the current source fails (a session fetch or an <img> error) it falls to the next.
 *  Cards default to `?size=thumb` while exposing `originalUrl` for full-size zoom/lightbox views.
 *  A false `hasCandidate` means the entry has no loadable image source at all. With containerRef
 *  passed, requests happen only while it nears the scroll viewport. */
export function usePosterSource(
  entry: CatalogEntryView,
  containerRef?: RefObject<Element | null>,
  options?: { size?: PosterImageSize },
) {
  const size = options?.size ?? "thumb";
  const ordered = [...new Set([entry.coverUrl, entry.posterUrl].filter(isAuthedImagePath))];
  // Failures are recorded by canonical original URL rather than position: after the entry changes
  // (a polling refresh) a failed source keeps its verdict, and a newly appearing source is
  // untouched by the old failure. A 404 or a broken image fails for good; a transient failure
  // (network, 408, 429, 5xx) only skips the source until the next retry round.
  const [failedUrls, setFailedUrls] = useState<ReadonlySet<string>>(new Set());
  const [skippedUrls, setSkippedUrls] = useState<ReadonlySet<string>>(new Set());
  const [retryRound, setRetryRound] = useState(0);
  const retryStepRef = useRef(0);
  const currentOriginal = ordered.find((url) => !failedUrls.has(url) && !skippedUrls.has(url));
  const currentRequest = currentOriginal ? withImageSize(currentOriginal, size) : undefined;
  const { src, failed: fetchFailed, retryable } = useAuthedImageState(currentRequest, containerRef, retryRound);
  useEffect(() => {
    if (fetchFailed && currentOriginal) {
      (retryable ? setSkippedUrls : setFailedUrls)((prev) => new Set(prev).add(currentOriginal));
    }
  }, [fetchFailed, retryable, currentOriginal]);
  useEffect(() => {
    if (src) retryStepRef.current = 0;
  }, [src]);
  // Retries only while nothing is shown, so a card already showing the fallback never flickers.
  const awaitingRetry = !currentOriginal && ordered.some((url) => skippedUrls.has(url));
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
    if (currentOriginal) {
      setFailedUrls((prev) => new Set(prev).add(currentOriginal));
    }
  };
  return { src, originalUrl: currentOriginal, hasCandidate: ordered.length > 0, onImageError };
}
