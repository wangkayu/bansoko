import type { ParserKindOption } from "../../lib/parserKind";
import { Select } from "../../ui/base";
import { t } from "../../lib/i18n";

/**
 * The episode parser choice for one operation: the same option set as the
 * instance's episode parser. A one-shot download keeps the change in its task
 * draft; file-manager recognition keeps it in the recognition request. Neither
 * writes back to the subscription, a preset or the global settings.
 */
export function ParserKindSelect({
  value,
  options,
  onChange,
  disabled,
  className,
}: {
  value: string;
  options: ParserKindOption[];
  onChange: (kind: string) => void;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <Select
      label={t("policy.parser.kindLabel")}
      value={value}
      options={options.map((option) => ({ value: option.value, label: option.label }))}
      onChange={onChange}
      disabled={disabled}
      className={className}
    />
  );
}
