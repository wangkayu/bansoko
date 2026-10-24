import { useState } from "react";
import { useSavePart } from "../../lib/saveParts";
import { SwitchRow } from "../../ui/SaveFooter";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getInstanceFileEffectPolicy,
  getFileMaintenanceAnimePolicy,
  isVersionConflict,
  listReconciliationPathDiffs,
  saveFileMaintenanceAnimePolicy,
  saveInstanceFileEffectPolicy,
  toUserMessage,
  type ReconciliationPathDiffView,
  type UnclassifiedRoutePolicyValue,
} from "../../lib/api/client";
import { StatePill } from "../../ui";
import { Button, Dialog, HelpTip, Select, type SelectOption } from "../../ui/base";
import { fileMaintenanceKeys, reconciliationKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";

const routeOptions = (): SelectOption<UnclassifiedRoutePolicyValue>[] => [
  { value: "skip", label: t("file.unclassified.skip") },
  { value: "route", label: t("file.unclassified.route") },
];

// The file-effect policy card: unknown-episode handling (unclassified_route) is an
// instance-level snapshot configuration fixed at subscribe time — changing the global default
// never touches a subscribed instance, and the instance adjusts it here independently. The
// handling and the automatic-alignment switch are both the rules tab's draft, submitted one by
// one by the dialog footer's single save. Scanning and scan results (alignment state, problem
// files) are not configuration; they live in the anime's "files" dialog (opened from the card's
// ⋯ menu).
export function FileEffectPolicySection(props: { animeID: string; onChanged?: () => void; active?: boolean; readOnly?: boolean }) {
  const { animeID, onChanged, active = true, readOnly = false } = props;
  const queryClient = useQueryClient();
  const policy = useQuery({
    queryKey: fileMaintenanceKeys.fileEffect(animeID),
    queryFn: () => getInstanceFileEffectPolicy(animeID),
  });
  const [draft, setDraft] = useState<UnclassifiedRoutePolicyValue>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [alignmentDraft, setAlignmentDraft] = useState<boolean>();
  const [alignmentSaving, setAlignmentSaving] = useState(false);
  const [alignmentError, setAlignmentError] = useState<string>();
  const [alignmentConflict, setAlignmentConflict] = useState(false);
  const alignment = useQuery({
    queryKey: [...fileMaintenanceKeys.policy, animeID],
    queryFn: () => getFileMaintenanceAnimePolicy(animeID),
    enabled: active,
  });
  const diffs = useQuery({
    queryKey: reconciliationKeys.pathDiffs(animeID, 0),
    queryFn: () => listReconciliationPathDiffs(animeID),
    enabled: confirmOpen,
  });
  const dirty = !readOnly && policy.data !== undefined && draft !== undefined && draft !== policy.data.unclassified_route;
  const savedAlignment = alignment.data?.alignment_enabled ?? false;
  const alignmentDirty = !readOnly && alignment.data !== undefined && alignmentDraft !== undefined && alignmentDraft !== savedAlignment;

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: fileMaintenanceKeys.fileEffect(animeID) });
    onChanged?.();
  };

  async function handleSave() {
    const data = policy.data;
    if (!data || draft === undefined) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const stored = await saveInstanceFileEffectPolicy(animeID, {
        expected_version: data.version,
        unclassified_route: draft,
      });
      queryClient.setQueryData(fileMaintenanceKeys.fileEffect(animeID), stored);
      setDraft(stored.unclassified_route);
      refresh();
      return { ok: true as const };
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("anime.fileEffect.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }

  async function saveAlignment() {
    const data = alignment.data;
    if (!data || alignmentDraft === undefined) return { ok: true as const };
    setAlignmentSaving(true);
    setAlignmentError(undefined);
    setAlignmentConflict(false);
    try {
      const stored = await saveFileMaintenanceAnimePolicy(animeID, { enabled: data.enabled, alignment_enabled: alignmentDraft, expected_version: data.version });
      queryClient.setQueryData([...fileMaintenanceKeys.policy, animeID], stored);
      void queryClient.invalidateQueries({ queryKey: [...fileMaintenanceKeys.policy, animeID] });
      onChanged?.();
      return { ok: true as const };
    } catch (cause) {
      if (isVersionConflict(cause)) {
        // The server's value changed: after the refresh the user confirms again (the local choice is kept; counts recompute against the new baseline).
        setAlignmentConflict(true);
        await queryClient.invalidateQueries({ queryKey: [...fileMaintenanceKeys.policy, animeID] });
      }
      setAlignmentError(toUserMessage(cause, t("policy.fileEffect.alignmentSaveFailed")));
      return { ok: false as const };
    } finally {
      setAlignmentSaving(false);
    }
  }

  useSavePart("fileEffect", { count: dirty ? 1 : 0, label: t("file.unclassifiedTitle"), busy: saving, save: handleSave });
  useSavePart("alignment", { count: alignmentDirty ? 1 : 0, label: t("policy.fileEffect.alignmentTitle"), busy: alignmentSaving, save: saveAlignment });

  const data = policy.data;
  return (
    <section className="policy-card">
      <h3 className="policy-card-title text-body">{t("file.unclassifiedTitle")}</h3>
      {policy.isLoading && <StatePill busy>{t("act.reading")}</StatePill>}
      {policy.error && <StatePill tone="offline">{toUserMessage(policy.error, t("anime.fileEffect.loadFailed"))}</StatePill>}
      {data && (
        <div className="policy-card-body">
          {/* The title is already in the card header: the select shows only the option name and reuses the title as its accessible name. */}
          <Select<UnclassifiedRoutePolicyValue>
            label={t("file.unclassifiedTitle")}
            value={draft ?? data.unclassified_route}
            options={routeOptions()}
            onChange={setDraft}
            disabled={readOnly || saving}
            className="tw:w-full tw:max-w-xs"
          />
          <p className="policy-card-note text-caption">
            {t("anime.unclassifiedHint")}
            <HelpTip label={t("anime.unclassifiedHelpLabel")} className="tw:ml-1 tw:align-middle">{t("anime.unclassifiedHelp")}</HelpTip>
          </p>
          {error && <p className="policy-card-error">{error}</p>}
          {conflict !== undefined && (
            <p className="policy-card-conflict">{t("common.versionConflict")}</p>
          )}
        </div>
      )}
      <div className="policy-card-body">
        {alignment.isLoading ? <StatePill busy>{t("act.reading")}</StatePill> : alignment.error ? <StatePill tone="offline">{toUserMessage(alignment.error, t("policy.fileEffect.alignmentLoadFailed"))}</StatePill> : alignment.data && (
          <SwitchRow
            label={t("policy.fileEffect.alignmentTitle")}
            description={t("policy.fileEffect.alignmentNote")}
            checked={alignmentDraft ?? savedAlignment}
            disabled={readOnly || alignmentSaving}
            onCheckedChange={(checked) => {
              // The path differences are reviewed before opening: confirming only writes the switch into the draft; saving still goes through the footer.
              if (checked && !savedAlignment) setConfirmOpen(true);
              else setAlignmentDraft(checked);
            }}
          />
        )}
        {alignmentConflict && <p className="policy-card-conflict">{t("policy.versionConflictRefreshed")}</p>}
        {alignmentError && !alignmentConflict && <p className="policy-card-error">{alignmentError}</p>}
      </div>
      <Dialog
        open={confirmOpen}
        tier="S"
        title={t("policy.fileEffect.confirmTitle")}
        onRequestClose={() => setConfirmOpen(false)}
        footer={
          <>
            <Button onClick={() => setConfirmOpen(false)}>{t("policy.fileEffect.later")}</Button>
            <Button
              variant="primary"
              disabled={diffs.isLoading || Boolean(diffs.error)}
              onClick={() => {
                setAlignmentDraft(true);
                setConfirmOpen(false);
              }}
            >
              {t("policy.fileEffect.confirm")}
            </Button>
          </>
        }
      >
        <div className="tw:grid tw:gap-2 tw:text-sm">
          <p className="tw:m-0">{t("policy.fileEffect.confirmBody")}</p>
          {diffs.isLoading && <StatePill busy>{t("policy.fileEffect.diffLoading")}</StatePill>}
          {diffs.error && <StatePill tone="offline">{toUserMessage(diffs.error, t("policy.fileEffect.diffFailed"))}</StatePill>}
          {diffs.data?.flatMap((diff: ReconciliationPathDiffView) => [diff, ...(diff.companions ?? [])]).map((diff) => <p key={`${diff.file_path}:${diff.expected_path ?? ""}`} className="tw:m-0 tw:wrap-anywhere">{diff.differs ? t("policy.fileEffect.willChange") : t("policy.fileEffect.keep")}{diff.file_path} → {diff.expected_path ?? t("policy.fileEffect.unresolved")}</p>)}
          {diffs.data?.length === 0 && <p className="tw:m-0">{t("policy.fileEffect.noDiff")}</p>}
        </div>
      </Dialog>
    </section>
  );
}
