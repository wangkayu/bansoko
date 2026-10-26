import { useEffect, useLayoutEffect, useRef, type PointerEvent as ReactPointerEvent, type ReactNode, type RefObject } from "react";
import { Dialog as BaseDialog } from "@base-ui/react/dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";
import { currentDialogOpener, installDialogFocusReturn } from "./focusReturn";
import { LAYER, LayerContext, useLayerZIndex } from "./layers";
import { t } from "../../lib/i18n";
import "./base.css";

// Dialog — the product dialog tiers:
//   S 440px  confirmations / short forms; bottom sheet on phones
//   M 760px  log, search, editors (`fixedHeight` for the log and global search)
//   L 1080px legacy centered wide surface
//   R        right-side sheet, desktop min(760px, 100vw) full height, phone
//            full screen — long manage surfaces kept open while browsing
//            (anime config, downloader manager, file manager, execution
//            records). Sheets never stack on sheets; centered dialogs may
//            stack on a sheet.
//   F        full-bleed workspace (settings)
// sheet, M/L/F go full screen. A non-blocking S sheet can be dragged down by
// its handle or header to request close (past a distance or a flick speed;
// otherwise it springs back). Blocking sheets have no handle and do not drag. Header / body /
// footer are fixed slots so every dialog shares padding, title type and close
// placement; only the body scrolls (flex column, body `flex: 1; min-height: 0`,
// dvh units).
//
// Stacking: a dialog outside any other dialog is `top` (1000), one opened from
// inside a dialog is `nested` (1100, any depth; at most three levels in
// practice), `blocking` confirmations are 1200 and always on top.
//
// Closing is always a *request*: Esc, backdrop (not for blocking dialogs) and the close button (hidden when blocking) all call
// onRequestClose, so callers can route it through useDirtyConfirm. Focus goes
// back to the element that opened the dialog — the focused element, or the
// element just pressed when a tap/click did not focus it (touch, Safari); pass
// `returnFocus` when that element will be gone by then (e.g. a menu item).

export type DialogTier = "S" | "M" | "L" | "R" | "F";

// Must match `@custom-variant phone` in base.css.
const PHONE_QUERY = "(width <= 600px)";
const REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)";
/** Drag-to-dismiss: release past this distance (px) or this share of the sheet height… */
const DISMISS_DISTANCE_PX = 96;
const DISMISS_HEIGHT_SHARE = 0.3;
/** …or flick faster than this (px/ms) over at least DISMISS_FLICK_MIN_PX. */
const DISMISS_VELOCITY = 0.5;
const DISMISS_FLICK_MIN_PX = 16;
const SPRING_BACK_MS = 200;

function mediaMatches(query: string): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(query).matches;
}

type DragState = { pointerId: number; startY: number; prevY: number; prevAt: number; lastY: number; lastAt: number };

// Drag-to-dismiss for the phone bottom sheet. Moves the popup with an inline
// transform (Tailwind positions it with `translate`, which composes with it).
function useSheetDrag({
  enabled,
  open,
  popupRef,
  onRequestClose,
}: {
  enabled: boolean;
  open: boolean;
  popupRef: RefObject<HTMLDivElement | null>;
  onRequestClose: () => void;
}) {
  const dragRef = useRef<DragState | null>(null);
  const openRef = useRef(open);
  useEffect(() => {
    openRef.current = open;
    const popup = popupRef.current;
    if (open && popup) {
      popup.style.transform = "";
      popup.style.transition = "";
    }
  }, [open, popupRef]);

  function springBack(popup: HTMLDivElement) {
    if (mediaMatches(REDUCED_MOTION_QUERY)) {
      popup.style.transition = "";
      popup.style.transform = "";
      return;
    }
    popup.style.transition = `transform ${SPRING_BACK_MS}ms ease-out`;
    popup.style.transform = "";
    window.setTimeout(() => {
      if (!dragRef.current) popup.style.transition = "";
    }, SPRING_BACK_MS + 20);
  }

  function onPointerDown(event: ReactPointerEvent<HTMLElement>) {
    if (!enabled || !mediaMatches(PHONE_QUERY)) return;
    if (event.pointerType === "mouse" && event.button !== 0) return;
    const target = event.target;
    if (target instanceof Element && target.closest("button, a, input, select, textarea, [role='button']")) return;
    const popup = popupRef.current;
    if (!popup) return;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const now = performance.now();
    dragRef.current = { pointerId: event.pointerId, startY: event.clientY, prevY: event.clientY, prevAt: now, lastY: event.clientY, lastAt: now };
    popup.style.transition = "none";
  }

  function onPointerMove(event: ReactPointerEvent<HTMLElement>) {
    const drag = dragRef.current;
    const popup = popupRef.current;
    if (!drag || drag.pointerId !== event.pointerId || !popup) return;
    const now = performance.now();
    drag.prevY = drag.lastY;
    drag.prevAt = drag.lastAt;
    drag.lastY = event.clientY;
    drag.lastAt = now;
    popup.style.transform = `translateY(${Math.max(0, event.clientY - drag.startY)}px)`;
  }

  function finish(event: ReactPointerEvent<HTMLElement>, cancelled: boolean) {
    const drag = dragRef.current;
    const popup = popupRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    dragRef.current = null;
    event.currentTarget.releasePointerCapture?.(event.pointerId);
    if (!popup) return;
    const now = performance.now();
    const distance = Math.max(0, event.clientY - drag.startY);
    const velocity = (event.clientY - drag.prevY) / Math.max(1, now - drag.prevAt);
    const threshold = Math.max(DISMISS_DISTANCE_PX, popup.getBoundingClientRect().height * DISMISS_HEIGHT_SHARE);
    const dismiss = !cancelled && (distance >= threshold || (distance >= DISMISS_FLICK_MIN_PX && velocity >= DISMISS_VELOCITY));
    if (!dismiss) {
      springBack(popup);
      return;
    }
    // Close is still a request (the caller may hold unsaved edits and keep the
    // sheet open behind a confirmation): spring back if it is still open.
    popup.style.transition = "";
    onRequestClose();
    window.setTimeout(() => {
      if (openRef.current && popupRef.current) springBack(popupRef.current);
    }, 0);
  }

  if (!enabled) return {};
  return {
    onPointerDown,
    onPointerMove,
    onPointerUp: (event: ReactPointerEvent<HTMLElement>) => finish(event, false),
    onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => finish(event, true),
  };
}

// Positioning per tier. Each tier owns its full geometry so no two
// unconditional classes compete for one property (cn() has no tailwind-merge).
// The open/close starting transform is also per tier: centered tiers scale
// from 0.98, the sheet slides in from the right edge.
const centered = "tw:left-1/2 tw:top-1/2 tw:-translate-x-1/2 tw:-translate-y-1/2 tw:rounded-dialog tw:border tw:border-border tw:data-starting-style:scale-[0.98]";
const phoneFullScreen =
  "tw:phone:inset-0 tw:phone:w-full tw:phone:h-dvh tw:phone:translate-none tw:phone:rounded-none tw:phone:border-0";
// S and M size to their content up to the cap (header/footer fixed, body
// scrolls). M with `fixedHeight` pins the height at the cap instead, so content
// that changes while open (filters, search results) does not make the dialog
// jump; on phones M is full screen either way. L, R and F are always fixed.
// (Class strings stay literal so Tailwind's scanner can see them.)
function popupTier(tier: DialogTier, fixedHeight: boolean): string {
  switch (tier) {
    case "S":
      return cn(
        centered,
        "tw:w-[min(440px,calc(100vw-2rem))] tw:max-h-[calc(100dvh-2rem)]",
        "tw:phone:inset-x-0 tw:phone:top-auto tw:phone:bottom-0 tw:phone:w-full tw:phone:max-h-[85dvh] tw:phone:translate-none",
        "tw:phone:rounded-t-dialog tw:phone:rounded-b-none tw:phone:border-x-0 tw:phone:border-b-0 tw:phone:pb-[env(safe-area-inset-bottom)]",
      );
    case "M":
      return cn(
        centered,
        "tw:w-[min(760px,calc(100vw-2rem))]",
        fixedHeight ? "tw:h-[min(640px,calc(100dvh-2rem))]" : "tw:max-h-[min(640px,calc(100dvh-2rem))] tw:phone:max-h-none",
        phoneFullScreen,
      );
    case "L":
      return cn(centered, "tw:w-[min(1080px,calc(100vw-2rem))] tw:h-[min(760px,calc(100dvh-2rem))]", phoneFullScreen);
    case "R":
      // Right-side sheet: flush to the right edge, full height, 760px cap,
      // rounded leading edge and a single left stroke; full screen on phones.
      return cn(
        // Radius uses the 4-value shorthand so the phone rounded-none shorthand
        // (emitted later, inside the phone media query) reliably overrides it;
        // same reason the stroke is `border` on all sides instead of border-l.
        "tw:right-0 tw:top-0 tw:h-dvh tw:w-[min(760px,100vw)] tw:rounded-[var(--radius-dialog)_0_0_var(--radius-dialog)] tw:border tw:border-border",
        "tw:data-starting-style:translate-x-6 tw:data-ending-style:translate-x-6",
        "tw:phone:inset-0 tw:phone:w-full tw:phone:rounded-none tw:phone:border-0",
      );
    case "F":
      return "tw:inset-0 tw:w-full tw:h-dvh tw:data-starting-style:scale-[0.98]";
  }
}

export function Dialog({
  open,
  onRequestClose,
  tier = "M",
  blocking = false,
  title,
  description,
  headerStart,
  headerExtra,
  footer,
  children,
  bodyClassName,
  bodyLayout = "scroll",
  closeLabel = t("ui.close"),
  returnFocus,
  initialFocus,
  focusTitle = false,
  dismissOnOverlay,
  fixedHeight = false,
  closeDisabled = false,
}: {
  open: boolean;
  onRequestClose: () => void;
  tier?: DialogTier;
  /** Blocking confirmation: top stacking tier (1200) and `alertdialog` role. */
  blocking?: boolean;
  title: ReactNode;
  description?: ReactNode;
  /** Leading header content left of the title block (e.g. a poster thumbnail). */
  headerStart?: ReactNode;
  headerExtra?: ReactNode;
  footer?: ReactNode;
  children: ReactNode;
  bodyClassName?: string;
  /**
   * `scroll` (default): padded body that scrolls. `fill`: an unpadded,
   * non-scrolling flex column for content that lays out its own fixed strip,
   * scroll region and footer (anime config tabs).
   */
  bodyLayout?: "scroll" | "fill";
  closeLabel?: string;
  /** Element to focus after closing, when the opener will no longer exist. */
  returnFocus?: RefObject<HTMLElement | null>;
  /** Element to focus on open (default: Base UI's first tabbable). */
  initialFocus?: RefObject<HTMLElement | null>;
  /**
   * Non-form dialogs: land initial focus on the title
   * (`tabIndex=-1`, no focus ring) instead of the first tabbable — otherwise a
   * deep-linked or refreshed dialog opens with a focus ring on the close X or
   * the first link. Ignored when `initialFocus` is given.
   */
  focusTitle?: boolean;
  /**
   * Whether a backdrop press requests close. Defaults to true, and to false for
   * `blocking` dialogs (Esc and the cancel button still close them). A blocking
   * dialog also hides the title-bar close X.
   */
  dismissOnOverlay?: boolean;
  /**
   * M tier only: hold the height at the tier cap instead of sizing to content,
   * for dialogs whose content changes while open (log filters, search results).
   */
  fixedHeight?: boolean;
  /** Disable the title-bar close button (a save is in flight). */
  closeDisabled?: boolean;
}) {
  const parentLayer = useLayerZIndex();
  const popupRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  useEffect(installDialogFocusReturn, []);
  // Capture before Base UI moves focus into the popup.
  useLayoutEffect(() => {
    if (open) openerRef.current = currentDialogOpener();
  }, [open]);
  const overlayDismisses = dismissOnOverlay ?? !blocking;
  const draggable = tier === "S" && !blocking;
  const dragHandlers = useSheetDrag({ enabled: draggable, open, popupRef, onRequestClose });
  const zIndex = blocking ? LAYER.blocking : parentLayer >= LAYER.top ? LAYER.nested : LAYER.top;

  return (
    <BaseDialog.Root
      open={open}
      disablePointerDismissal={!overlayDismisses}
      onOpenChange={(next) => {
        if (next) return;
        onRequestClose();
      }}
    >
      <BaseDialog.Portal>
        <BaseDialog.Backdrop
          style={{ zIndex }}
          className="tw:fixed tw:inset-0 tw:bg-overlay tw:transition-opacity tw:duration-200 tw:data-starting-style:opacity-0 tw:data-ending-style:opacity-0"
        />
        <BaseDialog.Popup
          // Spread, not role={undefined}: an explicit undefined would erase Base UI's own role="dialog".
          {...(blocking ? { role: "alertdialog" } : {})}
          initialFocus={initialFocus ?? (focusTitle ? titleRef : undefined)}
          ref={popupRef}
          finalFocus={() => {
            if (returnFocus?.current) return returnFocus.current;
            const opener = openerRef.current;
            if (!opener?.isConnected) return true;
            // Focus already moved on to another surface (e.g. a dialog that
            // replaced this one): respect it and let Base UI decide. Focus that
            // sits in this popup, on the body, or in the surface holding the
            // opener (a parent dialog re-focusing itself) still returns.
            const active = document.activeElement;
            if (!(active instanceof HTMLElement) || active === document.body || popupRef.current?.contains(active)) return opener;
            const activeSurface = active.closest("[role='dialog'], [role='alertdialog']");
            if (activeSurface === null || activeSurface.contains(opener)) return opener;
            return true;
          }}
          style={{ zIndex }}
          className={cn(
            "tw:fixed tw:flex tw:flex-col tw:overflow-hidden tw:bg-popover tw:text-popover-foreground tw:shadow-2 tw:outline-none",
            "tw:transition-[opacity,scale,translate] tw:duration-200 tw:ease-out tw:data-starting-style:opacity-0 tw:data-ending-style:opacity-0",
            popupTier(tier, fixedHeight),
          )}
          data-tier={tier}
        >
          <LayerContext.Provider value={zIndex}>
            {draggable ? (
              <div
                aria-hidden="true"
                data-drag-handle=""
                className="tw:hidden tw:phone:flex tw:flex-none tw:justify-center tw:pt-2 tw:touch-none"
                {...dragHandlers}
              >
                <div className="tw:h-1 tw:w-9 tw:rounded-full tw:bg-border" />
              </div>
            ) : null}
            <header
              className={cn(
                "tw:flex tw:flex-none tw:items-start tw:gap-3 tw:border-b tw:border-border tw:px-6 tw:pt-5 tw:pb-4 tw:phone:px-4 tw:phone:pt-4 tw:phone:pb-3",
                draggable && "tw:phone:touch-none",
              )}
              {...dragHandlers}
            >
              {headerStart}
              <div className="tw:min-w-0 tw:flex-1">
                <BaseDialog.Title
                  ref={titleRef}
                  {...(focusTitle ? { tabIndex: -1 } : {})}
                  className="tw:m-0 tw:font-display tw:text-base tw:font-semibold tw:leading-6 tw:tracking-tight tw:outline-none"
                >
                  {title}
                </BaseDialog.Title>
                {description ? (
                  <BaseDialog.Description className="tw:m-0 tw:mt-0.5 tw:text-xs tw:text-muted-foreground">
                    {description}
                  </BaseDialog.Description>
                ) : null}
              </div>
              {headerExtra}
              {blocking ? null : (
              <button
                type="button"
                aria-label={closeLabel}
                disabled={closeDisabled}
                onClick={onRequestClose}
                className="tw:-mr-1 tw:grid tw:disabled:pointer-events-none tw:disabled:opacity-50 tw:size-8 tw:flex-none tw:place-items-center tw:rounded-md tw:text-muted-foreground tw:hover:bg-muted tw:hover:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-ring"
              >
                <X className="tw:size-[18px]" />
              </button>
              )}
            </header>
            <div
              className={cn(
                "tw:min-h-0 tw:flex-1",
                bodyLayout === "fill" ? "tw:flex tw:flex-col tw:overflow-hidden" : "tw:overflow-y-auto tw:px-6 tw:py-4 tw:phone:px-4",
                bodyClassName,
              )}
            >
              {children}
            </div>
            {footer ? (
              <footer className="tw:flex tw:flex-none tw:flex-wrap tw:items-center tw:justify-end tw:gap-2 tw:border-t tw:border-border tw:px-6 tw:py-3 tw:phone:px-4">
                {footer}
              </footer>
            ) : null}
          </LayerContext.Provider>
        </BaseDialog.Popup>
      </BaseDialog.Portal>
    </BaseDialog.Root>
  );
}
