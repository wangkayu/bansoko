import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Select, StateBlock } from "../../ui/base";
import { Save } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "../../ui/toast";
import {
  ApiError,
  getReplacementPolicyDefault,
  saveReplacementPolicyDefault,
  toUserMessage,
} from "../../lib/api/client";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { ConflictBlock } from "../../ui";
import {
  admissionOptions,
  draftFromView,
  draftsEqual,
  inputFromDraft,
  type ReplacementDraft,
} from "./replacementDraft";
import { t } from "../../lib/i18n";
import { QualityCompareEditor } from "../replacement/QualityCompareEditor";

// Episode versions (replacement policy): the global default a new subscription copies at creation.
//   Existing subscriptions hold their own frozen copy, so changes here affect only subscriptions
//   created later (the backend contract: existing instances never re-inherit). Modes are all /
//   per_group / single_copy; a policy change decides nothing at once, existing files are judged
//   at the next cycle (new candidate, completed scan, resumed subscription).

function conflictVersion(cause: unknown): number | undefined {
  return cause instanceof ApiError && cause.status === 409 ? (cause.currentVersion ?? 0) : undefined;
}

export function SettingsReplacement({ onDirtyChange }: { onDirtyChange?: (dirty: boolean) => void }) {
  return (
    <div className="settings-policy">
      <ReplacementSection onDirtyChange={onDirtyChange ?? noop} />
    </div>
  );
}

const noop = () => {};

function ReplacementSection({ onDirtyChange }: { onDirtyChange: (dirty: boolean) => void }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: fileMaintenanceKeys.replacementDefault, queryFn: getReplacementPolicyDefault });
  const data = query.data;
  const [draft, setDraft] = useState<ReplacementDraft>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  const baseline = data ? draftFromView(data) : undefined;
  const dirty = !!draft && !!baseline && !draftsEqual(draft, baseline);

  // Adopt the server value whenever it changes and there is no local edit to lose.
  useEffect(() => {
    if (data && !dirty) setDraft(draftFromView(data));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- version identifies the server value
  }, [data?.version]);
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  async function save() {
    if (!data || !draft || saving) return;
    setSaving(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const saved = await saveReplacementPolicyDefault(inputFromDraft(data, draft));
      queryClient.setQueryData(fileMaintenanceKeys.replacementDefault, saved);
      setDraft(draftFromView(saved));
      toast.success(t("settings.replacement.saved"));
    } catch (cause) {
      const version = conflictVersion(cause);
      if (version !== undefined) setConflict(version);
      setError(toUserMessage(cause, t("settings.replacement.saveFailed")));
    } finally {
      setSaving(false);
    }
  }

  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await query.refetch();
    if (fresh.data) setDraft(draftFromView(fresh.data));
  }

  const update = (change: (current: ReplacementDraft) => ReplacementDraft) =>
    setDraft((current) => (current ? change(current) : current));

  return (
    <section className="settings-policy-section" aria-label={t("settings.replacement.title")}>
      <div className="settings-policy-header">
        <div>
          <h2 className="settings-policy-title text-title">{t("settings.replacement.title")}</h2>
          <p className="settings-policy-description text-caption">{t("settings.replacement.description")}</p>
        </div>
        <Button variant="primary" size="sm" aria-label={saving ? t("settings.replacement.saving") : t("settings.replacement.saveAria")} disabled={!dirty || saving} onClick={() => void save()}>
          <Save size={14} />
          {saving ? t("settings.replacement.saving") : t("settings.replacement.save")}
        </Button>
      </div>
      {conflict !== undefined ? (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={() => void refreshAfterConflict()} />
      ) : (
        error && <Alert tone="error" title={error} />
      )}
      {query.isLoading && (
        <StateBlock kind="busy" title={t("settings.replacement.loading")} />
      )}
      {query.error && <Alert tone="error" title={toUserMessage(query.error, t("settings.replacement.loadFailed"))} />}
      {draft && (
        <>
          <div className="settings-policy-card">
            <h3 className="settings-policy-card-title text-body">{t("settings.replacement.admissionCard")}</h3>
            <div className="settings-replacement-field">
              <Select label={t("replacement.admissionTitle")} value={draft.admission_mode} options={admissionOptions().map((option) => ({ value: option.value, label: option.label }))} onChange={(value) => update((current) => ({ ...current, admission_mode: value }))} />
            </div>
            <p className="settings-policy-card-description text-caption">
              {admissionOptions().find((option) => option.value === draft.admission_mode)?.hint}
            </p>
            {draft.admission_mode !== "all" && (
              <p className="settings-policy-card-description text-caption">{t("settings.replacement.replacedNote")}</p>
            )}
          </div>

          <div className="settings-policy-card">
            <QualityCompareEditor
              value={draft.quality_profile}
              admissionMode={draft.admission_mode}
              onChange={(quality_profile) => update((current) => ({ ...current, quality_profile }))}
            />
          </div>


        </>
      )}
    </section>
  );
}
