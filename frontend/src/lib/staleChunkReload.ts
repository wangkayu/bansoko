const reloadedAtKey = "bansoko:stale-chunk-reload-at";
const reloadWindowMs = 10_000;

/**
 * A page opened before the server was updated still names the previous build's
 * lazy chunks; the new build no longer serves them, so opening a lazy dialog
 * fails with "Failed to fetch dynamically imported module". Reload once to pick
 * up the current build. A second failure inside the window is a real error and
 * surfaces normally instead of looping.
 *
 * Preventing the default stops Vite's helper from rethrowing, which also makes it
 * resolve the failed import with undefined instead of rejecting; lazyEntry
 * (lib/lazyEntry.ts) turns that into "still loading" so the reload lands on the
 * stage fallback rather than the router's error page.
 */
export function installStaleChunkReload(target: Window = window, storage: Storage = window.sessionStorage, now: () => number = Date.now) {
  target.addEventListener("vite:preloadError", (event) => {
    const last = Number(storage.getItem(reloadedAtKey) ?? 0);
    if (now() - last < reloadWindowMs) return;
    storage.setItem(reloadedAtKey, String(now()));
    event.preventDefault();
    target.location.reload();
  });
}
