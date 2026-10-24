import type { ParserKindOption } from "../../lib/parserKind";
import { Select } from "../../ui/base";
import { t } from "../../lib/i18n";

/**
 * 这次任务的解析器选择：与实例「集数解析器」同一组选项。改动只进这次任务的草稿，
 * 由调用方重读预览；不写回订阅、预设或全局。
 */
export function OneShotParserSelect({
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
