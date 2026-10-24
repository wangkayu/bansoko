import { useEffect, useRef, useState } from "react";

function scrollParent(node: HTMLElement): HTMLElement | null {
  for (let current = node.parentElement; current; current = current.parentElement) {
    const { overflowY } = window.getComputedStyle(current);
    if (overflowY === "auto" || overflowY === "scroll") return current;
  }
  return null;
}

/**
 * Collapses the phone toolbar while scrolling: it stays collapsed after scrolling down past the
 *   toolbar's height, reappears immediately on scrolling up, and never collapses while focus is
 *   in a toolbar input. Returns a ref to attach to the toolbar and the current collapsed state.
 */
export function useCollapseOnScroll<T extends HTMLElement>(enabled: boolean) {
  const ref = useRef<T>(null);
  const [collapsed, setCollapsed] = useState(false);
  useEffect(() => {
    const node = ref.current;
    if (!enabled || !node) return;
    const scroller = scrollParent(node);
    const read = () => (scroller ? scroller.scrollTop : window.scrollY);
    let last = read();
    const onScroll = () => {
      const top = read();
      const delta = top - last;
      if (Math.abs(delta) < 8 && top > node.offsetHeight) return;
      last = top;
      // No collapsing while the filter has focus; after clicking a column or button the focus stays in the toolbar and does not count.
      const active = document.activeElement;
      const keep = active instanceof HTMLInputElement && node.contains(active);
      setCollapsed(!keep && delta > 0 && top > node.offsetHeight);
    };
    const target: HTMLElement | Window = scroller ?? window;
    target.addEventListener("scroll", onScroll, { passive: true });
    return () => target.removeEventListener("scroll", onScroll);
  }, [enabled]);
  return { ref, collapsed: enabled && collapsed };
}
