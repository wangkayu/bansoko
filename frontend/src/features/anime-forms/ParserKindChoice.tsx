import { RadioGroup } from "../../ui/base";
import type { ParserKindOption } from "../../lib/parserKind";

// The episode parser's either/or choice (regex / reltitle): shared by the instance's rules tab
// and the settings' default rules. Each option gets a one-line note; read-only disables the whole
// group but keeps the selection, and the save button is the caller's job.
export function ParserKindChoice({
  label,
  value,
  options,
  onChange,
  readOnly = false,
}: {
  label: string;
  value: string;
  options: ParserKindOption[];
  onChange: (next: string) => void;
  readOnly?: boolean;
}) {
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-1">
      <span className="tw:text-xs tw:text-muted-foreground">{label}</span>
      <RadioGroup<string>
        aria-label={label}
        variant="card"
        orientation="vertical"
        value={value}
        options={options}
        onValueChange={onChange}
        disabled={readOnly}
      />
    </div>
  );
}
