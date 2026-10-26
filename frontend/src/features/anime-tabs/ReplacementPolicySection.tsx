import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  getInstanceReplacementPolicy,
  isVersionConflict,
  saveInstanceReplacementPolicy,
  toUserMessage,
} from "../../lib/api/client";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { useSavePart } from "../../lib/saveParts";
import { t } from "../../lib/i18n";
import { StatePill } from "../../ui";
import { Select } from "../../ui/base";
import { QualityCompareEditor } from "../replacement/QualityCompareEditor";
import { admissionOptions, draftFromView, draftsEqual, inputFromDraft, type ReplacementDraft } from "../settings/replacementDraft";

// The subscription's own copy of the episode-versions policy (keep mode + version priority). It
//   was copied from the global default when the draft was created and never follows it again.
//   Edits are part of the rules tab's single save (save part "replacement"); saving decides nothing
//   at once, existing files are judged at the next cycle.
export function ReplacementPolicySection({
  animeID,
  onChanged,
  active = true,
  readOnly = false,
}: {
  animeID: string;
  onChanged?: () => void;
  active?: boolean;
  readOnly?: boolean;
}) {
  const queryClient = useQueryClient();
  const queryKey = fileMaintenanceKeys.replacement(animeID);
  const policy = useQuery({ queryKey, queryFn: () => getInstanceReplacementPolicy(animeID), enabled: active });
  const data = policy.data;
  const [draft, setDraft] = useState<ReplacementDraft>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState(false);
  const baseline = data ? draftFromView(data) : undefined;
  const dirty = !readOnly && !!draft && !!baseline && !draftsEqual(draft, baseline);

  // Adopt the server value whenever it changes and there is no local edit to lose.
  useEffect(() => {
    if (data && !dirty) setDraft(draftFromView(data));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- version identifies the server value
  }, [data?.version]);

  async function save() {
    if (!data || !draft) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    setConflict(false);
    try {
      const saved = await saveInstanceReplacementPolicy(animeID, inputFromDraft(data, draft));
      queryClient.setQueryData(queryKey, saved);
      setDraft(draftFromView(saved));
      onChanged?.();
      return { ok: true as const };
    } catch (cause) {
      if (isVersionConflict(cause)) {
        // The server's value changed: after the refresh the user confirms again (the local edit is kept).
        setConflict(true);
        await queryClient.invalidateQueries({ queryKey });
      }
      setError(toUserMessage(cause, t("policy.versions.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }

  useSavePart("replacement", { count: dirty ? 1 : 0, label: t("config.snapshot.legend"), busy: saving, save });

  const mode = draft?.admission_mode;
  return (
    <section className="policy-card" aria-label={t("config.snapshot.legend")}>
      <h3 className="policy-card-title text-body">{t("config.snapshot.legend")}</h3>
      {policy.isLoading && <StatePill busy>{t("act.reading")}</StatePill>}
      {policy.error && <StatePill tone="offline">{toUserMessage(policy.error, t("policy.versions.loadFailed"))}</StatePill>}
      {draft && mode && (
        <div className="policy-card-body">
          <Select
            label={t("replacement.admissionTitle")}
            value={mode}
            options={admissionOptions().map((option) => ({ value: option.value, label: option.label }))}
            onChange={(value) => setDraft({ ...draft, admission_mode: value })}
            disabled={readOnly || saving}
            className="tw:w-full tw:max-w-xs"
          />
          <p className="policy-card-note text-caption">{admissionOptions().find((option) => option.value === mode)?.hint}</p>
          {mode !== "all" && !readOnly && <p className="policy-card-note text-caption">{t("policy.versions.timingNote")}</p>}
          <QualityCompareEditor
            value={draft.quality_profile}
            admissionMode={mode}
            managedAnimeID={animeID}
            disabled={readOnly || saving}
            onChange={(quality_profile) => setDraft({ ...draft, quality_profile })}
          />
          {error && !conflict && <p className="policy-card-error">{error}</p>}
          {conflict && <p className="policy-card-conflict">{t("policy.versionConflictRefreshed")}</p>}
        </div>
      )}
    </section>
  );
}
