// Dialog focus return for presses that do not focus their target.
//
// Base UI returns focus to the element that was focused when a dialog opened.
// A touch tap (and a mouse click in Safari/WebKit) does not focus the button
// it lands on, so that element is the body or the parent dialog and focus
// would not come back to the trigger. This module remembers the last pressed
// focusable element; Dialog treats it as the opener when the dialog opens
// right after the press.
//
// It is kept free of React/Base UI imports so the app entry can install the
// listener at startup (dialog chunks load lazily, after the opening press).

const FOCUSABLE =
  "button, a[href], input, select, textarea, summary, [tabindex]:not([tabindex='-1']), [role='button'], [role='tab'], [role='menuitem']";
/** A dialog opening this soon after a press was opened by that press (covers a lazy chunk load). */
const PRESS_WINDOW_MS = 1500;

let lastPress: { element: HTMLElement; at: number } | undefined;
let installed = false;

/** Idempotent; call once at app startup (Dialog also calls it). */
export function installDialogFocusReturn(): void {
  if (installed || typeof document === "undefined") return;
  installed = true;
  document.addEventListener(
    "pointerdown",
    (event) => {
      const target = event.target;
      const element = target instanceof Element ? target.closest<HTMLElement>(FOCUSABLE) : null;
      lastPress = element ? { element, at: Date.now() } : undefined;
    },
    true,
  );
}

/** The element that most plausibly opened a dialog that is opening now. */
export function currentDialogOpener(): HTMLElement | null {
  if (lastPress && lastPress.element.isConnected && Date.now() - lastPress.at <= PRESS_WINDOW_MS) return lastPress.element;
  const active = document.activeElement;
  return active instanceof HTMLElement && active !== document.body ? active : null;
}
