import type * as React from "react";
import { Segmented, type SegmentedTone } from "./base/toggles";

// SegmentedControl — enum picker (season switcher, scope switch, small option
// sets). Kept as the established call signature over the ui/base Segmented
// (radio semantics, arrow-key movement), so there is one segmented picker.

export function SegmentedControl<T extends string>({
  options,
  value,
  onChange,
  ariaLabel,
  className,
  compact = false,
  title,
}: {
  options: ReadonlyArray<{ value: T; label: React.ReactNode; tone?: SegmentedTone }>;
  value: T;
  onChange: (value: T) => void;
  ariaLabel: string;
  className?: string;
  /** Dense inline rows (e.g. per-subgroup policy) use the small light-border variant instead of a row of big buttons. */
  compact?: boolean;
  title?: string;
}) {
  return (
    <Segmented
      aria-label={ariaLabel}
      title={title}
      value={value}
      onValueChange={onChange}
      options={options}
      size={compact ? "compact" : "default"}
      className={className}
    />
  );
}
