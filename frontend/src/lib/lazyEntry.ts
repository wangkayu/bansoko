import { lazy, type ComponentType, type LazyExoticComponent } from "react";

// installStaleChunkReload reloads the page when a lazy chunk of the previous build is gone, but
// preventing the `vite:preloadError` default also makes Vite's preload helper resolve that failed
// import with undefined instead of rejecting. Reading the entry off that undefined rejects inside
// the promise and paints the router's error page for the moment before the reload lands;
// suspending instead keeps the stage fallback until the reload replaces the page.

/** Never settles: the stale-chunk reload replaces the document. */
const pendingReload = new Promise<never>(() => {});

/**
 * React.lazy for a lazily loaded entry module: resolves the module namespace and picks the
 * component out of it. A missing module (the reload above is in flight) keeps the Suspense
 * fallback on screen.
 */
export function lazyEntry<M, P>(
  load: () => Promise<M>,
  pick: (module: M) => ComponentType<P>,
): LazyExoticComponent<ComponentType<P>> {
  return lazy(async () => {
    const module = (await load()) as M | undefined;
    if (module === undefined) return pendingReload;
    return { default: pick(module) };
  });
}
