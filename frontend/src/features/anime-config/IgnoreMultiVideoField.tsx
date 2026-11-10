import { useState } from "react";
import {
  isVersionConflict,
  setAnimeIgnoreMultiVideoReleases,
  toUserMessage,
  type AnimeWorkspace,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { HelpTip } from "../../ui/base";
import { SwitchRow } from "../../ui/SaveFooter";
import { toast } from "../../ui/toast";
import type { WorkspaceCapability } from "../workspace/capability";

/**
 * 「忽略多视频文件种子」开关。草稿里随整份草稿保存；订阅后随时可改，改动立即保存
 * 并同时收口在途种子（开=多视频种子记为已过滤，关=重新评价）。
 */
export function IgnoreMultiVideoField({
  checked,
  onChange,
  loading = false,
  disabled = false,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  loading?: boolean;
  disabled?: boolean;
}) {
  return (
    <section className="policy-card" aria-label={t("anime.ignoreMultiVideo.label")}>
      <div className="policy-card-body">
        <SwitchRow
          label={t("anime.ignoreMultiVideo.label")}
          description={(
            <>
              {t("anime.ignoreMultiVideo.description")}
              <HelpTip label={t("anime.ignoreMultiVideo.helpLabel")} className="tw:ml-1 tw:align-middle">{t("anime.ignoreMultiVideo.help")}</HelpTip>
            </>
          )}
          checked={checked}
          onCheckedChange={onChange}
          disabled={disabled}
          loading={loading}
        />
      </div>
    </section>
  );
}

/** Subscribed: saves immediately in any subscription state (CAS on the subscription version). */
export function OwnerIgnoreMultiVideo({
  animeID,
  workspace,
  capability,
  onChanged,
}: {
  animeID: string;
  workspace: Pick<AnimeWorkspace, "subscription">;
  capability: WorkspaceCapability;
  onChanged: () => void;
}) {
  const subscription = workspace.subscription;
  const [saving, setSaving] = useState(false);
  async function change(next: boolean) {
    if (capability.readOnly || saving) return;
    setSaving(true);
    try {
      await setAnimeIgnoreMultiVideoReleases(animeID, { expected_version: subscription.version, enabled: next });
    } catch (cause) {
      toast.error(isVersionConflict(cause) ? t("catalog.changed") : toUserMessage(cause, t("anime.ignoreMultiVideo.saveFailed")));
    } finally {
      setSaving(false);
      onChanged();
    }
  }
  return (
    <IgnoreMultiVideoField
      checked={subscription.ignore_multi_video_releases}
      onChange={(next) => void change(next)}
      loading={saving}
      disabled={capability.readOnly}
    />
  );
}
