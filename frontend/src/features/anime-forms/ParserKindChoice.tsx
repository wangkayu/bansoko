import type { ReactNode } from "react";
import { Segmented } from "../../ui/base";
import type { ParserKindOption } from "../../lib/parserKind";

/**
 * Compact episode parser selector (Plan B): a single-row Segmented picker
 * (`reltitle` first, then `regex`), paired inline with the active option's
 * one-line note and an optional trailing action (e.g. "View regex (N)").
 */
export function ParserKindChoice({
  label,
  value,
  options,
  onChange,
  readOnly = false,
  showLabel = false,
  noteSuffix,
  action,
}: {
  label: string;
  value: string;
  options: ParserKindOption[];
  onChange: (next: string) => void;
  readOnly?: boolean;
  /** Render the label visibly above the control (used inside modal forms); omitted inside cards whose header already names Parsing. */
  showLabel?: boolean;
  /** Extra inline note appended after the active option's description (e.g. kept pattern count). */
  noteSuffix?: ReactNode;
  /** Optional trailing action button placed on the right of the row. */
  action?: ReactNode;
}) {
  const selected = options.find((option) => option.value === value);
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-1.5">
      {showLabel && <span className="tw:text-xs tw:font-medium tw:text-muted-foreground">{label}</span>}
      <div className="tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-x-3 tw:gap-y-2">
        <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-x-3 tw:gap-y-1.5">
          <Segmented<string>
            aria-label={label}
            size="toolbar"
            value={value}
            onValueChange={onChange}
            disabled={readOnly}
            options={options.map((option) => ({
              value: option.value,
              label: option.label,
              tone: "solid" as const,
            }))}
          />
          {(selected?.description || noteSuffix) && (
            <span className="tw:min-w-0 tw:text-xs tw:text-muted-foreground">
              {selected?.description}
              {noteSuffix ? <> {noteSuffix}</> : null}
            </span>
          )}
        </div>
        {action}
      </div>
    </div>
  );
}
