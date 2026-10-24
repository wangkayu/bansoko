import { useCallback, useEffect, useRef } from "react";
import { useBlocker, type BlockerFunction } from "react-router-dom";
import type { RouteDialogParam } from "./routeDialogs";

/**
 * Blocks browser Back / address changes that would close a route-hosted dialog
 * while it holds unsaved edits. A blocked navigation is handed to `onBlocked`
 * with a `proceed` callback (the caller shows its useDirtyConfirm and calls
 * `proceed` on discard). Cancelling the confirmation resets the navigation, so
 * the dialog stays open and the address is restored. Must be rendered under a
 * data router (`createHashRouter` in App, `createMemoryRouter` in tests).
 *
 * Limits: react-router cannot block a POP to a history entry it did not create
 * (an address typed into the bar on the hash router); the dialog handles that
 * after the fact (AnimeConfigDialog `routeOpen`). Document-level navigations
 * (reload, a different path or site) get the browser's own `beforeunload`
 * prompt while dirty.
 */
export function RouteDialogLeaveGuard({
  param,
  isDirty,
  confirmOpen,
  onBlocked,
  onCancelled,
}: {
  /** The query param that identifies this dialog (`anime`). */
  param: RouteDialogParam;
  isDirty: () => boolean;
  confirmOpen: boolean;
  onBlocked: (proceed: () => void) => void;
  onCancelled: () => void;
}) {
  const shouldBlock = useCallback<BlockerFunction>(
    ({ currentLocation, nextLocation }) => {
      if (!isDirty()) return false;
      const current = new URLSearchParams(currentLocation.search).get(param);
      const next = nextLocation.pathname === currentLocation.pathname ? new URLSearchParams(nextLocation.search).get(param) : null;
      // Only leaving the dialog is guarded; putting the address back (current
      // null, e.g. after a hand-edited hash) is not.
      return current !== null && current !== next;
    },
    [isDirty, param],
  );
  const blocker = useBlocker(shouldBlock);

  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!isDirty()) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [isDirty]);
  const promptedRef = useRef(false);

  useEffect(() => {
    if (blocker.state !== "blocked") {
      promptedRef.current = false;
      return;
    }
    if (!confirmOpen && promptedRef.current) {
      // The confirmation was cancelled while the history navigation is still
      // blocked: reset it so the address goes back to the dialog.
      blocker.reset();
      promptedRef.current = false;
      onCancelled();
      return;
    }
    if (confirmOpen || promptedRef.current) return;
    promptedRef.current = true;
    onBlocked(() => blocker.proceed());
  }, [blocker, confirmOpen, onBlocked, onCancelled]);

  return null;
}
