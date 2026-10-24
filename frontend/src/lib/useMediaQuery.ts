import { useSyncExternalStore } from "react";

// Reactive media query. jsdom has no matchMedia: the query reports false
// (the desktop layout), so tests render one unambiguous tree instead of two
// CSS-hidden variants.

/** Matches the `phone` custom variant in ui/base/base.css (width ≤ 600px). */
export const PHONE_MEDIA_QUERY = "(width <= 600px)";

export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      if (typeof window === "undefined" || typeof window.matchMedia !== "function") return () => {};
      const mql = window.matchMedia(query);
      mql.addEventListener("change", onChange);
      return () => mql.removeEventListener("change", onChange);
    },
    () => (typeof window !== "undefined" && typeof window.matchMedia === "function" ? window.matchMedia(query).matches : false),
    () => false,
  );
}
