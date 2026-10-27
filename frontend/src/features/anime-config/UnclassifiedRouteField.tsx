import type { BusinessSnapshot } from "../../lib/api/client";
import { Field, Select } from "../../ui/base";
import { t } from "../../lib/i18n";

/** The handling choice for files without an episode number, shared by every surface that edits a business snapshot copy. */
export function UnclassifiedRouteField({
  value,
  onChange,
  hint,
}: {
  value: BusinessSnapshot["unclassified_route"];
  onChange: (value: BusinessSnapshot["unclassified_route"]) => void;
  /** Secondary line under the control. */
  hint?: React.ReactNode;
}) {
  return (
    <Field label={t("file.unclassifiedTitle")} className="tw:max-w-sm" hint={hint}>
      <Select
        label={t("file.unclassifiedTitle")}
        value={value}
        options={[
          { value: "skip", label: t("file.unclassified.skip") },
          { value: "route", label: t("file.unclassified.route") },
        ]}
        onChange={onChange}
      />
    </Field>
  );
}
