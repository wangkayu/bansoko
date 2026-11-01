import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Alert, HelpTip } from "../../ui/base";
import { listGlobalSections, putGlobalSection, toUserMessage, type GlobalSectionView } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { useSavePart } from "../../lib/saveParts";
import { SwitchRow } from "../../ui/SaveFooter";
import { t } from "../../lib/i18n";
import { sectionSourceLabel } from "./sectionBadge";

// "Ignore multi-video releases" default under settings' acquisition section:
//   the subscription_defaults Global section, a one-time input like tag_filters.
//   A new draft copies it once at creation (and again on reset to defaults); existing
//   subscriptions keep their own switch, and evaluation never reads this row.

const SECTION = "subscription_defaults";

export function SubscriptionDefaultsSettings() {
  const queryClient = useQueryClient();
  const sections = useQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
  const view = sections.data?.find((section) => section.section_name === SECTION);
  const [draft, setDraft] = useState<boolean>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const saved = ignoreMultiVideoFromSection(view);
  const dirty = view !== undefined && draft !== undefined && draft !== saved;

  async function save() {
    if (!view || draft === undefined) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    try {
      const stored = await putGlobalSection(SECTION, { schema_version: view.schema_version, value: { ignore_multi_video_releases: draft } }, view.version);
      queryClient.setQueryData<GlobalSectionView[]>(configKeys.globalSections, (current) =>
        current?.map((section) => (section.section_name === SECTION ? stored : section)),
      );
      void queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
      return { ok: true as const, message: t("subscriptionDefaults.saved") };
    } catch (cause) {
      setError(toUserMessage(cause, t("subscriptionDefaults.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }
  useSavePart("subscription-defaults", { count: dirty ? 1 : 0, label: t("subscriptionDefaults.title"), busy: saving, save });

  // The section list itself is reported by the page; without this section there is nothing to show.
  if (!view) return null;

  return (
    <div className="settings-shared-card">
      <div className="settings-shared-header">
        <h3 className="settings-shared-card-title text-body">{t("subscriptionDefaults.title")}</h3>
        <span className="settings-shared-muted text-badge">{sectionSourceLabel(view)}</span>
      </div>
      <div className="settings-shared-offset">
        {error && <Alert tone="error" title={error} />}
        <SwitchRow
          label={t("anime.ignoreMultiVideo.description")}
          description={(
            <>
              {t("subscriptionDefaults.description")}
              <HelpTip label={t("anime.ignoreMultiVideo.helpLabel")} className="tw:ml-1 tw:align-middle">{t("anime.ignoreMultiVideo.help")}</HelpTip>
            </>
          )}
          checked={draft ?? saved}
          disabled={saving}
          onCheckedChange={setDraft}
        />
      </div>
    </div>
  );
}

function ignoreMultiVideoFromSection(view?: GlobalSectionView): boolean {
  const value = view?.value.ignore_multi_video_releases;
  return typeof value === "boolean" ? value : false;
}
