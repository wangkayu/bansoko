import * as React from "react";
import { CircleHelp } from "lucide-react";
import { cn } from "@/lib/utils";
import { Popover } from "./overlays";

/**
 * Explanations default to one line; longer ones go into this "?" tip.
 * Opened by click or keyboard so touch works too — not a hover tooltip.
 */
export function HelpTip({
  children,
  label,
  className,
  title,
}: {
  /** Tip content. */
  children: React.ReactNode;
  /** Accessible name of the trigger button, written like "About: xxx" so tests can find it by label. */
  label: string;
  className?: string;
  title?: React.ReactNode;
}) {
  return (
    <Popover
      title={title}
      side="top"
      trigger={
        <button
          type="button"
          aria-label={label}
          className={cn(
            "tw:inline-flex tw:size-5 tw:shrink-0 tw:items-center tw:justify-center tw:rounded-full tw:border-0 tw:bg-transparent tw:p-0 tw:text-muted-foreground tw:cursor-pointer tw:hover:text-foreground tw:focus-visible:outline-2 tw:focus-visible:outline-ring",
            className,
          )}
        >
          <CircleHelp aria-hidden="true" className="tw:size-3.5" />
        </button>
      }
    >
      <div className="tw:grid tw:gap-1.5 tw:text-xs tw:leading-relaxed tw:text-muted-foreground">{children}</div>
    </Popover>
  );
}
