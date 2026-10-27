import type { BusinessSnapshot } from "../../lib/api/client";
import { Field, Select } from "../../ui/base";
import { UnclassifiedRouteField } from "./UnclassifiedRouteField";
import { admissionOptions } from "../settings/replacementDraft";
import { QualityCompareEditor } from "../replacement/QualityCompareEditor";
import { t } from "../../lib/i18n";

/**
 * Typed controls for the subscription's own copy of the business snapshot.
 * Defaults come from Settings → 同集多版本; this copy is edited per subscription.
 * Loaded provenance (versions, program digests) is evidence, not an editable value, and is not shown.
 * The parser kind is not edited here: it belongs with the episode rules in the
 * rules tab's 解析 block (DraftParserChoice), never as a separate control.
 */
export function BusinessSnapshotFields({
  value,
  onChange,
  managedAnimeID,
}: {
  value: BusinessSnapshot;
  onChange: (value: BusinessSnapshot) => void;
  /** Existing subscription: scopes comparison value suggestions to it. */
  managedAnimeID?: string;
}) {
  const update = (change: (next: BusinessSnapshot) => void) => {
    const next = structuredClone(value);
    change(next);
    onChange(next);
  };
  const replacement = value.replacement;

  return (
    <fieldset className="business-fields">
      <legend className="business-legend text-body">{t("config.snapshot.legend")}</legend>

      <Field label={t("replacement.admissionTitle")} className="tw:max-w-sm">
        <Select
          label={t("replacement.admissionTitle")}
          value={replacement.admission_mode}
          options={admissionOptions().map((option) => ({ value: option.value, label: option.label }))}
          onChange={(next) => update((v) => { v.replacement.admission_mode = next; })}
        />
      </Field>

      <QualityCompareEditor
        value={replacement.quality_profile}
        admissionMode={replacement.admission_mode}
        managedAnimeID={managedAnimeID}
        onChange={(profile) => update((v) => { v.replacement.quality_profile = profile; })}
      />

      <UnclassifiedRouteField value={value.unclassified_route} onChange={(next) => update((v) => { v.unclassified_route = next; })} />
    </fieldset>
  );
}

