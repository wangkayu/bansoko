import { createContext, useContext } from "react";

// Stacking tiers: every overlay derives its z-index from here so no component
// picks its own number.
//   page      the in-page ceiling (sticky top nav is z 50)
//   top       top-level dialog
//   nested    a dialog opened from inside another dialog
//   blocking  blocking confirmation, above everything else
// Floating popups (select, menu, popover, tooltip) sit POPUP_OFFSET above the
// layer that contains them.
export const LAYER = { page: 50, top: 1000, nested: 1100, blocking: 1200 } as const;
export type LayerName = keyof typeof LAYER;
export const POPUP_OFFSET = 10;

/** z-index of the innermost overlay layer around the current component. */
export const LayerContext = createContext<number>(LAYER.page);

export function useLayerZIndex(): number {
  return useContext(LayerContext);
}

export function usePopupZIndex(): number {
  return useContext(LayerContext) + POPUP_OFFSET;
}
