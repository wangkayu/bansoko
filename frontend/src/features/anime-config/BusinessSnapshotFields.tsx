import type { BusinessSnapshot } from "../../lib/api/client";
import { Field, HelpTip, Select } from "../../ui/base";
import { admissionOptions } from "../settings/replacementDraft";
import { QualityCompareEditor } from "../replacement/QualityCompareEditor";
import { t } from "../../lib/i18n";

/**
 * Typed controls for the subscription's own copy of the business snapshot.
 * Renders two separate policy cards matching the subscribed rules tab:
 * 1. Unknown-episode handling (`unclassified_route`)
 * 2. Multi-version episode replacement (`replacement`)
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
    <>
      <section className="policy-card" aria-label={t("file.unclassifiedTitle")}>
        <h3 className="policy-card-title text-body">{t("file.unclassifiedTitle")}</h3>
        <div className="policy-card-body">
          <Select
            label={t("file.unclassifiedTitle")}
            value={value.unclassified_route}
            options={[
              { value: "skip", label: t("file.unclassified.skip") },
              { value: "route", label: t("file.unclassified.route") },
            ]}
            onChange={(next) => update((v) => { v.unclassified_route = next; })}
            className="tw:w-full tw:max-w-xs"
          />
          <p className="policy-card-note text-caption">
            {t("anime.unclassifiedHint")}
            <HelpTip label={t("anime.unclassifiedHelpLabel")} className="tw:ml-1 tw:align-middle">{t("anime.unclassifiedHelp")}</HelpTip>
          </p>
        </div>
      </section>

      <section className="policy-card" aria-label={t("config.replacementAria")}>
        <h3 className="policy-card-title text-body">{t("config.snapshot.legend")}</h3>
        <div className="policy-card-body">
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
        </div>
      </section>
    </>
  );
}

