import { useEffect, useRef, useState, type RefObject } from "react";

import { ApiError, fetchAuthedAssetObjectUrl } from "./api/client";

// A season page renders 100–300 cards. The cache holds a whole page so a
// revisit (tab switch, filter toggle) shows every cover without re-queueing,
// and an entry still shown by a mounted image is never evicted.
export const MAX_AUTHED_IMAGE_CACHE_ENTRIES = 400;

// A browser keeps about six connections per host. If every card fetched at
// once, the cold covers would occupy all the connections and the API calls
// behind a card click (candidates, Global preset) would queue until they
// drain. Cap the in-flight image fetches so the remaining connections stay
// free for interaction; cached images are unaffected.
const MAX_CONCURRENT_IMAGE_LOADS = 4;

// Cards start loading this far outside the scroll viewport, so a normal
// scroll finds them ready; cards further away wait until they come near.
const NEAR_VIEWPORT_MARGIN = "600px 0px";

/** One mounted image that wants a path. `visible` (on screen now, not just
 *  near) moves its load ahead of near-but-offscreen ones. */
type Interest = { visible: boolean };

type WaitingLoad = {
  path: string;
  seq: number;
  interests: Set<Interest>;
  promise: Promise<string>;
  resolve: (objectUrl: string) => void;
  reject: (error: unknown) => void;
};

class ImageLoadWithdrawn extends Error {}

let activeImageLoads = 0;
let waitSeq = 0;
const waitingLoads = new Map<string, WaitingLoad>();
const startedLoads = new Map<string, Promise<string>>();
const imageCache = new Map<string, string>();
const imagesInUse = new Map<string, number>();

function promote(path: string, objectUrl: string): void {
  imageCache.delete(path);
  imageCache.set(path, objectUrl);
}

function evictIfNeeded(arriving: string): void {
  if (imageCache.size <= MAX_AUTHED_IMAGE_CACHE_ENTRIES) return;
  // Oldest first, skipping anything a mounted image still shows (revoking it
  // would blank that image on its next render) and the entry just loaded,
  // which its requesters are about to show.
  for (const [path, objectUrl] of imageCache) {
    if (imageCache.size <= MAX_AUTHED_IMAGE_CACHE_ENTRIES) return;
    if (path === arriving || imagesInUse.has(path)) continue;
    imageCache.delete(path);
    URL.revokeObjectURL(objectUrl);
  }
}

function retainImage(path: string): void {
  imagesInUse.set(path, (imagesInUse.get(path) ?? 0) + 1);
}

function releaseImage(path: string): void {
  const count = (imagesInUse.get(path) ?? 0) - 1;
  if (count > 0) imagesInUse.set(path, count);
  else imagesInUse.delete(path);
}

/** Next load to start: on-screen requests first, then near ones, each in
 *  request order (page order on first render). */
function nextWaitingLoad(): WaitingLoad | undefined {
  let best: WaitingLoad | undefined;
  let bestVisible = false;
  for (const load of waitingLoads.values()) {
    const visible = [...load.interests].some((interest) => interest.visible);
    if (!best || (visible && !bestVisible) || (visible === bestVisible && load.seq < best.seq)) {
      best = load;
      bestVisible = visible;
    }
  }
  return best;
}

function pumpImageLoads(): void {
  while (activeImageLoads < MAX_CONCURRENT_IMAGE_LOADS) {
    const load = nextWaitingLoad();
    if (!load) return;
    waitingLoads.delete(load.path);
    activeImageLoads += 1;
    const started = fetchAuthedAssetObjectUrl(load.path)
      .then((objectUrl) => {
        promote(load.path, objectUrl);
        evictIfNeeded(load.path);
        return objectUrl;
      })
      .finally(() => {
        startedLoads.delete(load.path);
        activeImageLoads -= 1;
        pumpImageLoads();
      });
    startedLoads.set(load.path, started);
    started.then(load.resolve, load.reject);
  }
}

function requestImage(path: string, interest: Interest): Promise<string> {
  const cached = imageCache.get(path);
  if (cached) {
    promote(path, cached);
    return Promise.resolve(cached);
  }
  const started = startedLoads.get(path);
  if (started) return started;
  const waiting = waitingLoads.get(path);
  if (waiting) {
    waiting.interests.add(interest);
    return waiting.promise;
  }
  let resolve!: (objectUrl: string) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<string>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  waitingLoads.set(path, { path, seq: (waitSeq += 1), interests: new Set([interest]), promise, resolve, reject });
  pumpImageLoads();
  return promise;
}

/** A card that scrolled away before its load started gives up its place in
 *  the queue; a load already in flight finishes and lands in the cache. */
function withdrawImage(path: string, interest: Interest): void {
  const waiting = waitingLoads.get(path);
  if (!waiting) return;
  waiting.interests.delete(interest);
  if (waiting.interests.size === 0) {
    waitingLoads.delete(path);
    waiting.reject(new ImageLoadWithdrawn());
  }
}

export function resetAuthedImageCache(): void {
  for (const objectUrl of imageCache.values()) {
    URL.revokeObjectURL(objectUrl);
  }
  imageCache.clear();
  imagesInUse.clear();
  for (const waiting of waitingLoads.values()) waiting.reject(new ImageLoadWithdrawn());
  waitingLoads.clear();
  startedLoads.clear();
}

// --- viewport presence -----------------------------------------------------

type PresenceKind = "near" | "visible";
type PresenceListener = (kind: PresenceKind, intersecting: boolean) => void;
type PresenceObservers = { near: IntersectionObserver; visible: IntersectionObserver };

const presenceListeners = new Map<Element, PresenceListener>();
const presenceObservers = new Map<Element | null, PresenceObservers>();

function notifyPresence(kind: PresenceKind) {
  return (entries: IntersectionObserverEntry[]) => {
    for (const entry of entries) presenceListeners.get(entry.target)?.(kind, entry.isIntersecting);
  };
}

/** The content scrolls inside a container (the app main pane), which clips
 *  intersections against the viewport; observing relative to that container
 *  is what lets the margin reach cards just below the fold. */
function scrollRootOf(element: Element): Element | null {
  for (let node = element.parentElement; node; node = node.parentElement) {
    const overflowY = getComputedStyle(node).overflowY;
    if (overflowY === "auto" || overflowY === "scroll" || overflowY === "overlay") return node;
  }
  return null;
}

function observePresence(element: Element, listener: PresenceListener): () => void {
  const root = scrollRootOf(element);
  let observers = presenceObservers.get(root);
  if (!observers) {
    observers = {
      near: new IntersectionObserver(notifyPresence("near"), { root, rootMargin: NEAR_VIEWPORT_MARGIN }),
      visible: new IntersectionObserver(notifyPresence("visible"), { root }),
    };
    presenceObservers.set(root, observers);
  }
  presenceListeners.set(element, listener);
  observers.near.observe(element);
  observers.visible.observe(element);
  const owned = observers;
  return () => {
    presenceListeners.delete(element);
    owned.near.unobserve(element);
    owned.visible.unobserve(element);
  };
}

type Presence = { near: boolean; visible: boolean };

/** Whether the target is near / inside the scroll viewport. Without a target
 *  (or without IntersectionObserver) everything counts as visible, so plain
 *  callers load eagerly as before. */
function useViewportPresence(target?: RefObject<Element | null>): Presence {
  const observed = !!target && typeof IntersectionObserver !== "undefined";
  const [presence, setPresence] = useState<Presence>(() => ({ near: !observed, visible: !observed }));
  useEffect(() => {
    const element = target?.current;
    if (!observed || !element) return;
    return observePresence(element, (kind, intersecting) => {
      setPresence((prev) => (prev[kind] === intersecting ? prev : { ...prev, [kind]: intersecting }));
    });
  }, [observed, target]);
  return presence;
}

// --- hooks -------------------------------------------------------------------

/** Only same-origin API paths (/api/v1/images/...) are loaded: the server no longer hands out
 *  any third-party image URLs and the frontend does not render them (the CSP img-src
 *  'self' data: blob: would block them anyway). */
export function isAuthedImagePath(src?: string): src is string {
  return !!src && src.startsWith("/api/v1/");
}

type AuthedImageResult = { source: string; round: number; objectUrl?: string; failed: boolean; retryable: boolean };

export type AuthedImageState = {
  /** The blob objectURL; present only after a successful load. */
  src?: string;
  /** The session fetch failed (404/502/network). The caller moves to the next candidate or a placeholder on this. */
  failed: boolean;
  /** The failure is transient (network error, 408, 429, 5xx: the service restarting or an upstream
   *  timeout), so the same path may load later; a 404 is final. */
  retryable: boolean;
};

/** A failed image load worth retrying later: the request never reached the service, or the service
 *  or the image upstream was briefly unavailable. */
function isTransientImageError(error: unknown): boolean {
  if (!(error instanceof ApiError)) return true;
  return error.status === 0 || error.status === 408 || error.status === 429 || error.status >= 500;
}

// An <img> cannot carry the Bearer session; images pointing at the own /api/v1 (the
// poster/cover proxy) must be fetched as a blob through the session and handed to <img> as an
// objectURL. Non-API paths (including third-party URLs) are not loaded and yield the empty
// state. With a target (a card container) given, the request goes out only when it nears the
// scroll viewport, cards inside the viewport first; without one it requests immediately.
// A new retryRound requests the same path again: the result of an earlier round is never reused.
export function useAuthedImageState(src?: string, target?: RefObject<Element | null>, retryRound = 0): AuthedImageState {
  const path = isAuthedImagePath(src) ? src : undefined;
  const { near, visible } = useViewportPresence(target);
  const [result, setResult] = useState<AuthedImageResult>();
  const interestRef = useRef<Interest>({ visible });
  const settledRef = useRef<string | undefined>(undefined);

  useEffect(() => {
    interestRef.current.visible = visible;
  }, [visible]);

  useEffect(() => {
    const settledKey = path && `${retryRound}:${path}`;
    if (!path || !near || settledRef.current === settledKey) return;
    let cancelled = false;
    const interest = interestRef.current;
    requestImage(path, interest)
      .then((objectUrl) => {
        if (cancelled) return;
        settledRef.current = settledKey;
        setResult({ source: path, round: retryRound, objectUrl, failed: false, retryable: false });
      })
      .catch((error: unknown) => {
        if (cancelled || error instanceof ImageLoadWithdrawn) return;
        settledRef.current = settledKey;
        setResult({ source: path, round: retryRound, failed: true, retryable: isTransientImageError(error) });
      });
    return () => {
      cancelled = true;
      withdrawImage(path, interest);
    };
  }, [path, near, retryRound]);

  // Results are reconciled by path and round: the first frame after src changes (or a retry starts)
  // never inherits the previous state.
  const current = result && result.source === path && result.round === retryRound ? result : undefined;
  const shown = current?.objectUrl ? current.source : undefined;

  // A shown image pins its cache entry so eviction never revokes it.
  useEffect(() => {
    if (!shown) return;
    retainImage(shown);
    return () => releaseImage(shown);
  }, [shown]);

  return { src: current?.objectUrl, failed: current?.failed ?? false, retryable: current?.retryable ?? false };
}

export function useAuthedImage(src?: string): string | undefined {
  return useAuthedImageState(src).src;
}
