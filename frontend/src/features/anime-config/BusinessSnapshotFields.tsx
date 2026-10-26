import type { BusinessSnapshot } from "../../lib/api/client";
import { Field, Select } from "../../ui/base";
import { admissionOptions } from "../settings/replacementDraft";
import { QualityCompareEditor } from "../replacement/QualityCompareEditor";
import { t } from "../../lib/i18n";

/**
 * Typed controls for the subscription's own copy of the business snapshot.
 * Defaults come from Settings → 保留与清理; this copy is edited per subscription.
 * Loaded provenance (versions, program digests) is evidence, not an editable value, and is not shown.
 * The parser kind is not edited here: it belongs with the episode rules in the
 * rules tab's 解析 block (DraftParserChoice), never as a separate control.
 */
export function BusinessSnapshotFields({
  value,
  onChange,
  managedAnimeID,
  hideReplacement = false,
}: {
  value: BusinessSnapshot;
  onChange: (value: BusinessSnapshot) => void;
  /** Existing subscription: scopes comparison value suggestions to it. */
  managedAnimeID?: string;
  /**
   * 当前版本隐藏按订阅的保留与清理（同集多版本保留方式、替换版本比较）：
   * 不渲染图例和这两项控件，只留未识别集数。被隐藏的取值原样留在快照里，
   * 保存时照常提交。只有番剧配置弹窗传入它；恢复时删掉那两处传参即可
   * （清单见任务 10-19-hide-anime-replacement 的 PRD）。
   */
  hideReplacement?: boolean;
}) {
  const update = (change: (next: BusinessSnapshot) => void) => {
    const next = structuredClone(value);
    change(next);
    onChange(next);
  };
  const replacement = value.replacement;

  return (
    <fieldset className="business-fields">
      {!hideReplacement && (
        <>
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
        </>
      )}

      <Field label={t("file.unclassifiedTitle")} className="tw:max-w-sm">
        <Select
          label={t("file.unclassifiedTitle")}
          value={value.unclassified_route}
          options={[
            { value: "skip", label: t("file.unclassified.skip") },
            { value: "route", label: t("file.unclassified.route") },
          ]}
          onChange={(next) => update((v) => { v.unclassified_route = next; })}
        />
      </Field>
    </fieldset>
  );
}

