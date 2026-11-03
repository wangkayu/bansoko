import type { ReactNode } from "react";
import { cn } from "../../lib/utils";

// TaskSplitLayout — the step-two shell one-shot and file-manager recognition
// share: the wide preview column on the left, the narrow settings column
// (TaskConfigPanel) on the right, each scrolling on its own inside the fixed
// dialog; one stacked, page-scrolled column on phones. The explicit grid rows
// are what pin the columns to the dialog height so their own scrollbars
// engage — an auto row would size to content and let it grow past the dialog
// body, which clips (the body is overflow-hidden). The two-column split holds
// at every width above phone: the columns stay side by side down to the
// phone breakpoint instead of restacking at a middle width.
export function TaskSplitLayout({ intro, children }: { intro?: ReactNode; children: ReactNode }) {
  return (
    <div
      className={cn(
        "tw:grid tw:min-h-0 tw:flex-1 tw:grid-cols-[minmax(0,1fr)_minmax(0,24rem)] tw:phone:grid-cols-1 tw:phone:grid-rows-none tw:phone:auto-rows-max tw:phone:overflow-y-auto tw:phone:overscroll-contain",
        intro === undefined ? "tw:grid-rows-[minmax(0,1fr)]" : "tw:grid-rows-[auto_minmax(0,1fr)]",
      )}
    >
      {intro === undefined ? null : <div className="tw:col-span-2 tw:px-6 tw:pt-3 tw:phone:col-span-1 tw:phone:px-4">{intro}</div>}
      {children}
    </div>
  );
}

/** The wide left column: the task's preview, scrolling on its own on desktop. */
export const taskPreviewColumnClass =
  "tw:grid tw:min-h-0 tw:scroll-mt-12 tw:content-start tw:gap-2 tw:overflow-y-auto tw:px-6 tw:pt-3 tw:pb-4 tw:phone:overflow-visible tw:phone:px-4";

/** The narrow right column: the task's settings panel, scrolling on its own on desktop. */
export const taskPanelColumnClass =
  "tw:min-h-0 tw:min-w-0 tw:overflow-y-auto tw:border-l tw:border-border tw:px-5 tw:pt-3 tw:pb-4 tw:phone:overflow-visible tw:phone:border-l-0 tw:phone:border-t tw:phone:px-4 tw:phone:pt-4";
