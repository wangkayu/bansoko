import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Select, StateBlock } from "../../ui/base";
import { Save } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "../../ui/toast";
import {
  ApiError,
  getFileEffectPolicyDefault,
  getRetentionPolicyDefault,
  saveFileEffectPolicyDefault,
  saveRetentionPolicyDefault,
  toUserMessage,
  type FileEffectPolicyDefaultView,
} from "../../lib/api/client";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { useSavePart } from "../../lib/saveParts";
import { ConflictBlock } from "../../ui";
import {
  admissionOptions,
  draftFromView,
  draftsEqual,
  inputFromDraft,
  type RetentionDraft,
} from "./retentionDraft";
import { t } from "../../lib/i18n";
import { QualityCompareEditor } from "../retention/QualityCompareEditor";

// Retention and cleanup: the global default a new subscription copies at creation. Existing
//   subscriptions hold their own frozen copy, so changes here affect only subscriptions created
//   later (the backend contract: existing instances never re-inherit).

function conflictVersion(cause: unknown): number | undefined {
  return cause instanceof ApiError && cause.status === 409 ? (cause.currentVersion ?? 0) : undefined;
}

export function SettingsRetention({ onDirtyChange }: { onDirtyChange?: (dirty: boolean) => void }) {
  const flags = useRef({ retention: false, unclassified: false });
  const report = useCallback(
    (key: "retention" | "unclassified", dirty: boolean) => {
      flags.current[key] = dirty;
      onDirtyChange?.(flags.current.retention || flags.current.unclassified);
    },
    [onDirtyChange],
  );
  const reportRetention = useCallback((dirty: boolean) => report("retention", dirty), [report]);
  const reportUnclassified = useCallback((dirty: boolean) => report("unclassified", dirty), [report]);
  return (
    <div className="settings-policy">
      <RetentionSection onDirtyChange={reportRetention} />
      <UnclassifiedSection onDirtyChange={reportUnclassified} />
    </div>
  );
}

// "Files with unrecognized episodes" is its own section: while retention/cleanup is hidden this
//   is the only card. It relates to the naming templates' "unknown content" section without
//   depending on the retention/cleanup policy.
const noop = () => {};
export function SettingsUnclassified({ onDirtyChange }: { onDirtyChange?: (dirty: boolean) => void }) {
  return <UnclassifiedSection onDirtyChange={onDirtyChange ?? noop} />;
}

function RetentionSection({ onDirtyChange }: { onDirtyChange: (dirty: boolean) => void }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: fileMaintenanceKeys.retentionDefault, queryFn: getRetentionPolicyDefault });
  const data = query.data;
  const [draft, setDraft] = useState<RetentionDraft>();
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
      const saved = await saveRetentionPolicyDefault(inputFromDraft(data, draft));
      queryClient.setQueryData(fileMaintenanceKeys.retentionDefault, saved);
      setDraft(draftFromView(saved));
      toast.success(t("settings.retention.saved"));
    } catch (cause) {
      const version = conflictVersion(cause);
      if (version !== undefined) setConflict(version);
      setError(toUserMessage(cause, t("settings.retention.saveFailed")));
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

  const update = (change: (current: RetentionDraft) => RetentionDraft) =>
    setDraft((current) => (current ? change(current) : current));

  return (
    <section className="settings-policy-section" aria-label={t("settings.retention.title")}>
      <div className="settings-policy-header">
        <div>
          <h2 className="settings-policy-title text-title">{t("settings.retention.title")}</h2>
          <p className="settings-policy-description text-caption">{t("settings.retention.description")}</p>
        </div>
        <Button variant="primary" size="sm" aria-label={saving ? t("settings.retention.saving") : t("settings.retention.saveAria")} disabled={!dirty || saving} onClick={() => void save()}>
          <Save size={14} />
          {saving ? t("settings.retention.saving") : t("settings.retention.save")}
        </Button>
      </div>
      {conflict !== undefined ? (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={() => void refreshAfterConflict()} />
      ) : (
        error && <Alert tone="error" title={error} />
      )}
      {query.isLoading && (
        <StateBlock kind="busy" title={t("settings.retention.loading")} />
      )}
      {query.error && <Alert tone="error" title={toUserMessage(query.error, t("settings.retention.loadFailed"))} />}
      {draft && (
        <>
          <div className="settings-policy-card">
            <h3 className="settings-policy-card-title text-body">{t("settings.retention.admissionCard")}</h3>
            <div className="settings-retention-field">
              <Select label={t("retention.admissionTitle")} value={draft.admission_mode} options={admissionOptions().map((option) => ({ value: option.value, label: option.label }))} onChange={(value) => update((current) => ({ ...current, admission_mode: value }))} />
            </div>
            <p className="settings-policy-card-description text-caption">
              {admissionOptions().find((option) => option.value === draft.admission_mode)?.hint}
            </p>
            {draft.admission_mode !== "all" && (
              <p className="settings-policy-card-description text-caption">{t("settings.retention.replacedNote")}</p>
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

const routeLabels = (): Record<FileEffectPolicyDefaultView["unclassified_route"], { label: string; hint: string }> => ({
  skip: { label: t("file.unclassified.skip"), hint: t("settings.retention.unclassified.skipHint") },
  route: { label: t("file.unclassified.route"), hint: t("settings.retention.unclassified.routeHint") },
});

function UnclassifiedSection({ onDirtyChange }: { onDirtyChange: (dirty: boolean) => void }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: fileMaintenanceKeys.fileEffectDefault, queryFn: getFileEffectPolicyDefault });
  const data = query.data;
  const [route, setRoute] = useState<FileEffectPolicyDefaultView["unclassified_route"]>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  const dirty = !!data && route !== undefined && route !== data.unclassified_route;

  useEffect(() => {
    if (data && !dirty) setRoute(data.unclassified_route);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- version identifies the server value
  }, [data?.version]);
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  // Edited in place: the selection is a draft, committed by the settings dialog footer's single save.
  async function save() {
    if (!data || !route) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const saved = await saveFileEffectPolicyDefault({ expected_version: data.version, unclassified_route: route });
      queryClient.setQueryData(fileMaintenanceKeys.fileEffectDefault, saved);
      setRoute(saved.unclassified_route);
      return { ok: true as const, message: t("settings.retention.unclassified.saved") };
    } catch (cause) {
      const version = conflictVersion(cause);
      if (version !== undefined) setConflict(version);
      setError(toUserMessage(cause, t("settings.retention.unclassified.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }
  useSavePart("unclassified", { count: dirty ? 1 : 0, label: t("file.unclassifiedTitle"), busy: saving, save });

  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await query.refetch();
    if (fresh.data) setRoute(fresh.data.unclassified_route);
  }

  return (
    <section className="settings-policy-section" aria-label={t("file.unclassifiedTitle")}>
      <div className="settings-policy-header">
        <div>
          <h2 className="settings-policy-title text-title">{t("file.unclassifiedTitle")}</h2>
          <p className="settings-policy-description text-caption">{t("settings.retention.unclassified.description")}</p>
        </div>
      </div>
      {conflict !== undefined ? (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={() => void refreshAfterConflict()} />
      ) : (
        error && <Alert tone="error" title={error} />
      )}
      {query.isLoading && (
        <StateBlock kind="busy" title={t("settings.retention.unclassified.loading")} />
      )}
      {query.error && <Alert tone="error" title={toUserMessage(query.error, t("settings.retention.unclassified.loadFailed"))} />}
      {route && (
        <div className="settings-policy-card">
          <div className="settings-retention-field">
            <Select label={t("settings.retention.unclassified.selectLabel")} value={route} options={(Object.keys(routeLabels()) as (keyof ReturnType<typeof routeLabels>)[]).map((value) => ({ value, label: routeLabels()[value].label }))} disabled={saving} onChange={(value) => setRoute(value)} />
          </div>
          <p className="settings-policy-card-description text-caption">{routeLabels()[route].hint}</p>
        </div>
      )}
    </section>
  );
}
