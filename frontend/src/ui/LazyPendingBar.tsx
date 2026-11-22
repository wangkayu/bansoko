import { useEffect, useState } from "react";
import { t } from "../lib/i18n";

const DELAY_MS = 120;

/**
 * Non-blocking suspense fallback for lazily loaded dialogs and overlays.
 * Screen readers receive an immediate status announcement, while the slim top
 * progress bar only appears after a 120ms delay so cached chunks never flicker.
 */
export function LazyPendingBar({ label }: { label?: string }) {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const timer = window.setTimeout(() => setVisible(true), DELAY_MS);
    return () => window.clearTimeout(timer);
  }, []);

  return (
    <>
      <span className="sr-only" role="status">
        {label ?? t("nav.stageLoading")}
      </span>
      {visible ? (
        <div
          aria-hidden="true"
          data-testid="lazy-pending-bar"
          className="tw:pointer-events-none tw:fixed tw:inset-x-0 tw:top-0 tw:z-50 tw:h-0.5 tw:overflow-hidden tw:bg-primary/20"
        >
          <div className="tw:h-full tw:w-1/3 tw:bg-primary tw:motion-safe:animate-lazy-bar tw:motion-reduce:w-full" />
        </div>
      ) : null}
    </>
  );
}
