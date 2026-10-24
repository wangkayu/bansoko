import { useState } from "react";
import {
  isVersionConflict,
  setAnimeNewReleasesOnly,
  toUserMessage,
  type AnimeWorkspace,
} from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { HelpTip } from "../../ui/base";
import { SwitchRow } from "../../ui/SaveFooter";
import { toast } from "../../ui/toast";
import type { WorkspaceCapability } from "../workspace/capability";

/**
 * 「只获取新种子」开关。基线是创建订阅的时间：草稿里随整份草稿保存；未开始的订阅
 * 上立即保存；开始后只读并附一句说明（要改只能结束订阅后重新订阅）。
 */
export function NewReleasesOnlyField({
  checked,
  onChange,
  locked = false,
  loading = false,
  disabled = false,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  /** The subscription has started: read-only, the note becomes a cannot-change notice. */
  locked?: boolean;
  loading?: boolean;
  disabled?: boolean;
}) {
  return (
    <section className="policy-card" aria-label={t("anime.newReleasesOnly.label")}>
      <div className="policy-card-body">
        <SwitchRow
          label={t("anime.newReleasesOnly.label")}
          description={(
            <>
              {locked ? t("anime.newReleasesOnly.locked") : t("anime.newReleasesOnly.description")}
              <HelpTip label={t("anime.newReleasesOnly.helpLabel")} className="tw:ml-1 tw:align-middle">{t("anime.newReleasesOnly.help")}</HelpTip>
            </>
          )}
          checked={checked}
          onCheckedChange={onChange}
          disabled={locked || disabled}
          loading={loading}
        />
      </div>
    </section>
  );
}

/** Subscribed: saves immediately while not started (CAS on the subscription version); read-only afterwards. */
export function OwnerNewReleasesOnly({
  animeID,
  workspace,
  capability,
  onChanged,
}: {
  animeID: string;
  workspace: AnimeWorkspace;
  capability: WorkspaceCapability;
  onChanged: () => void;
}) {
  const subscription = workspace.subscription;
  const [saving, setSaving] = useState(false);
  const pending = subscription.state === "pending" && !capability.readOnly;
  async function change(next: boolean) {
    if (!pending || saving) return;
    setSaving(true);
    try {
      await setAnimeNewReleasesOnly(animeID, { expected_version: subscription.version, enabled: next });
    } catch (cause) {
      toast.error(isVersionConflict(cause) ? t("catalog.changed") : toUserMessage(cause, t("anime.newReleasesOnly.saveFailed")));
    } finally {
      setSaving(false);
      onChanged();
    }
  }
  return (
    <NewReleasesOnlyField
      checked={subscription.new_releases_only}
      onChange={(next) => void change(next)}
      locked={!pending}
      loading={saving}
    />
  );
}
