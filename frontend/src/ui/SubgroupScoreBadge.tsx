import type * as React from "react";
import { Popover } from "./base";

// A subgroup score badge: one compact chip in a wrapping row, clicking opens a popover with the
// detail line (and, where the surface owns controls, the editing widgets).

const chipClass =
  "tw:inline-flex tw:max-w-full tw:cursor-pointer tw:items-center tw:gap-1 tw:rounded-[0.25rem] tw:border " +
  "tw:border-border tw:bg-muted/60 tw:px-2 tw:py-0.5 tw:text-left tw:text-xs tw:font-medium tw:leading-normal " +
  "tw:hover:bg-muted tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring";

/**
 * A subgroup's chip: name + score (plus a selection-state word when one exists, tinted when the
 * group is effectively selected). The popover names the group and shows the composition/detail
 * line; `children` (the surface's own controls) render below it.
 */
export function SubgroupScoreBadge({
  name,
  score,
  detail,
  ariaLabel,
  children,
}: {
  name: string;
  /** The score text shown inside the chip, already localized. */
  score: string;
  /** The popover's detail line; absent or empty text shows nothing. */
  detail?: { text: string; reasonTitle?: string };
  /** The chip's accessible name, supplied by the surface so it stays localized. */
  ariaLabel: string;
  /** Controls below the detail line (score editing). */
  children?: React.ReactNode;
}) {
  return (
    <Popover
      title={name}
      trigger={
        <button
          type="button"
          className={chipClass}
          aria-label={ariaLabel}
          title={name}
        >
          <span className="tw:truncate">{name}</span>
          <span className="tw:shrink-0 tw:text-muted-foreground">{score}</span>
        </button>
      }
    >
      <div className="tw:grid tw:gap-2">
        {detail !== undefined && detail.text !== "" && (
          <p className="tw:m-0 tw:text-xs tw:leading-relaxed tw:text-muted-foreground" title={detail.reasonTitle}>
            {detail.text}
          </p>
        )}
        {children}
      </div>
    </Popover>
  );
}
