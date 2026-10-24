import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Dialog, Input, SpinnerIcon, StateBlock } from "../../ui/base";
import { useState } from "react";
import {
  getFileMaintenancePolicy,
  saveFileMaintenancePolicy,
  toUserMessage,
  type GlobalSectionView,
  type ProviderGroupDefaults,
  type ProviderNameKeywordRules,
  type SubgroupScoreEntry,
} from "../../lib/api/client";
import { useSettingsSection } from "../../lib/settings";
import { useSavePart } from "../../lib/saveParts";
import { SwitchRow } from "../../ui/SaveFooter";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { ConflictBlock } from "../../ui";
import { MetadataRefreshSettings } from "./MetadataRefreshSettings";
import { t } from "../../lib/i18n";

// Subscription policy: the site-agnostic subscription policy (the subgroup cap, field max_rss).
//   The site-scoped subgroup rosters (provider_defaults), name scoring (name_keywords), catalog
//   auto-refresh and hidden anime have each moved into their own sidebar entries, the AniBT and
//   Mikan configuration pages (SettingsProviderDetail) — this page's draft still passes through
//   provider_defaults / name_keywords / entries, so the section-wide CAS on save never wipes fields
//   edited on the provider pages.
//
// Edited in place: the subgroup cap and automatic file alignment are both this section's draft,
//   submitted in order by the settings dialog footer's single save (the subscription policy section
//   first, then alignment).

type PolicyDraft = {
  max_rss: number;
  entries: SubgroupScoreEntry[];
  provider_defaults: ProviderGroupDefaults;
  name_keywords: ProviderNameKeywordRules;
};

function draftFromSection(section?: GlobalSectionView): PolicyDraft {
  const value = section?.value ?? {};
  return {
    max_rss: typeof value.max_rss === "number" ? value.max_rss : 100,
    entries: Array.isArray(value.entries) ? value.entries as SubgroupScoreEntry[] : [],
    provider_defaults: (value.provider_defaults ?? {}) as ProviderGroupDefaults,
    name_keywords: (value.name_keywords ?? {}) as ProviderNameKeywordRules,
  };
}

export function SettingsPolicy({ onDirtyChange }: { onDirtyChange?: (dirty: boolean) => void }) {
  const {
    sectionsQuery: sections,
    draft,
    changed,
    saving,
    error,
    conflict,
    updateDraft,
    submit,
    refreshAfterConflict,
  } = useSettingsSection<PolicyDraft>({
    sectionName: "subgroups",
    initialDraft: () => draftFromSection(),
    draftFromSection,
    successMessage: t("settings.policy.saved"),
    failureFallback: t("settings.policy.saveFailed"),
    onDirtyChange,
  });
  useSavePart("section", { count: changed ? 1 : 0, label: t("settings.policy.title"), busy: saving, save: submit });

  return (
    <div className="settings-policy">
      <section className="settings-policy-section">
        <div className="settings-policy-header">
          <div>
            <h2 className="settings-policy-title text-title">{t("settings.policy.title")}</h2>
            <p className="settings-policy-description text-caption">{t("settings.policy.description")}</p>
          </div>
        </div>
        {conflict !== undefined ? (
          <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
        ) : (
          error && <Alert tone="error" title={error} />
        )}
        {sections.isLoading && (
          <StateBlock kind="busy" title={t("settings.policy.loading")} />
        )}
        {sections.error && <Alert tone="error" title={toUserMessage(sections.error, t("settings.policy.loadFailed"))} />}
        <MaxRssCard value={draft.max_rss} disabled={saving} onChange={(value) => updateDraft((current) => ({ ...current, max_rss: value }))} />
        <GlobalAlignmentPolicy />
        <MetadataRefreshSettings />
      </section>
    </div>
  );
}

function GlobalAlignmentPolicy() {
  const queryClient = useQueryClient();
  const policy = useQuery({ queryKey: fileMaintenanceKeys.policy, queryFn: getFileMaintenancePolicy });
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [draft, setDraft] = useState<boolean>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const data = policy.data;
  const saved = data?.alignment_enabled ?? false;
  const dirty = data !== undefined && draft !== undefined && draft !== saved;

  async function save() {
    if (!data || draft === undefined) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    try {
      const stored = await saveFileMaintenancePolicy({ enabled: data.enabled, alignment_enabled: draft, expected_version: data.version });
      queryClient.setQueryData(fileMaintenanceKeys.policy, stored);
      void queryClient.invalidateQueries({ queryKey: fileMaintenanceKeys.policy });
      return { ok: true as const, message: t("settings.policy.alignment.saved") };
    } catch (cause) {
      setError(toUserMessage(cause, t("settings.policy.alignment.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }
  useSavePart("alignment", { count: dirty ? 1 : 0, label: t("settings.policy.alignment.title"), busy: saving, save });

  return (
    <div className="settings-policy-card">
      {policy.isLoading && <SpinnerIcon />}
      {policy.error && <Alert tone="error" title={toUserMessage(policy.error, t("settings.policy.alignment.loadFailed"))} />}
      {error && <Alert tone="error" title={error} />}
      {data && (
        <SwitchRow
          label={t("settings.policy.alignment.title")}
          description={t("settings.policy.alignment.description")}
          checked={draft ?? saved}
          disabled={saving}
          onCheckedChange={(checked) => (checked && !saved ? setConfirmOpen(true) : setDraft(checked))}
        />
      )}
      <Dialog
        open={confirmOpen}
        tier="S"
        title={t("settings.policy.alignment.confirmTitle")}
        onRequestClose={() => setConfirmOpen(false)}
        footer={
          <>
            <Button onClick={() => setConfirmOpen(false)}>{t("settings.policy.alignment.cancel")}</Button>
            <Button
              variant="primary"
              onClick={() => {
                setDraft(true);
                setConfirmOpen(false);
              }}
            >
              {t("settings.policy.alignment.confirm")}
            </Button>
          </>
        }
      >
        <div className="tw:grid tw:gap-1 tw:text-sm">
          <p className="tw:m-0">{t("settings.policy.alignment.confirmLine1")}</p>
          <p className="tw:m-0 tw:text-muted-foreground">{t("settings.policy.alignment.confirmLine2")}</p>
        </div>
      </Dialog>
    </div>
  );
}

// The subgroup cap: an in-place input. A legal value (an integer 0-100) writes the draft directly;
//   an illegal input stays in the field with a hint and is never written back.
function MaxRssCard({ value, disabled, onChange }: { value: number; disabled: boolean; onChange: (value: number) => void }) {
  const [pendingText, setPendingText] = useState<string>();
  const text = pendingText ?? String(value);
  function change(next: string) {
    const trimmed = next.trim();
    const parsed = Number(trimmed);
    if (/^\d+$/.test(trimmed) && parsed >= 0 && parsed <= 100) {
      setPendingText(undefined);
      onChange(parsed);
      return;
    }
    setPendingText(next);
  }
  return (
    <div className="settings-policy-card tw:grid tw:gap-2">
      <h3 className="settings-policy-card-title text-body">{t("settings.policy.maxRss")}</h3>
      <div className="settings-policy-field text-caption">
        <Input
          type="text"
          inputMode="numeric"
          value={text}
          aria-label={t("settings.policy.maxRssField")}
          aria-invalid={pendingText !== undefined}
          disabled={disabled}
          fullWidth={false}
          className="tw:w-24"
          onChange={(event) => change(event.target.value)}
        />
        {pendingText !== undefined && <span role="alert" className="settings-policy-error text-caption">{t("settings.policy.maxRssInvalid")}</span>}
      </div>
      <p className="settings-policy-card-description tw:m-0 text-caption">{t("settings.policy.maxRssSummary", { n: value })}</p>
    </div>
  );
}
