import { Segmented } from "../../ui/base";
import type { ParserKindOption } from "../../lib/parserKind";

/**
 * Compact episode parser selector: a single-row Segmented picker
 * (`reltitle` first, then `regex`), paired inline with the active option's
 * one-line description.
 */
export function ParserKindChoice({
  label,
  value,
  options,
  onChange,
  readOnly = false,
  showLabel = false,
}: {
  label: string;
  value: string;
  options: ParserKindOption[];
  onChange: (next: string) => void;
  readOnly?: boolean;
  /** Render the label visibly above the control (used inside modal forms); omitted inside cards whose header already names Parsing. */
  showLabel?: boolean;
}) {
  const selected = options.find((option) => option.value === value);
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-1.5">
      {showLabel && <span className="tw:text-xs tw:font-medium tw:text-muted-foreground">{label}</span>}
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
        {selected?.description && (
          <span className="tw:min-w-0 tw:text-xs tw:text-muted-foreground">
            {selected.description}
          </span>
        )}
      </div>
    </div>
  );
}
