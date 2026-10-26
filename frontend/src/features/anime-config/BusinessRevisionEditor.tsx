import { useEffect, useState } from "react";
import { confirmBusinessReplace } from "./confirmBusinessReplace";
import { applyAnimeConfigCandidate, getAnimeBusinessEdit, getBusinessPreset, newUUID, toUserMessage, type AnimeConfigCandidate, type BusinessEditView } from "../../lib/api/client";
import { getCandidateStore } from "../../lib/candidate/serverCandidateStore";
import { valuesEqual } from "../../lib/draft";
import { Button } from "../../ui";
import { freezeBusinessDraft, loadBusinessPreset } from "./businessDraft";
import { RulesSectionsFields } from "../anime-forms/RulesSectionsFields";
import { BusinessSnapshotFields } from "./BusinessSnapshotFields";
import { t } from "../../lib/i18n";

/**
 * Existing-instance command path: save and apply are deliberately separate.
 */
export function BusinessRevisionEditor({ animeID, onApplied, onDirtyChange }: { animeID: string; onApplied: () => void; onDirtyChange?: (dirty: boolean) => void }) {
  const [baseline, setBaseline] = useState<BusinessEditView>();
  const [edit, setEdit] = useState<BusinessEditView>();
  const [candidate, setCandidate] = useState<AnimeConfigCandidate>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const dirty = !!edit && !valuesEqual(edit.draft, candidate?.draft ?? baseline?.draft);
  useEffect(() => { onDirtyChange?.(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => () => { onDirtyChange?.(false); }, [onDirtyChange]);
  async function run(action: () => Promise<void>) {
    setBusy(true); setError(undefined);
    try { await action(); } catch (cause) { setError(toUserMessage(cause, t("config.revision.failed"))); }
    finally { setBusy(false); }
  }
  return <section aria-label={t("anime.revision.aria")}>
    <Button disabled={busy} onClick={() => void run(async () => {
      if (dirty && !await confirmBusinessReplace(t("config.revision.reload"))) return;
      const value = await getAnimeBusinessEdit(animeID); setBaseline(value); setEdit(structuredClone(value)); setCandidate(undefined);
    })}>{t("config.revision.edit")}</Button>
    {edit && <>
      <Button disabled={busy} onClick={() => void run(async () => {
        if (dirty && !await confirmBusinessReplace(t("config.useDefaultsBody"), { title: t("config.useDefaultsTitle"), confirmLabel: t("config.useDefaultsConfirm") })) return;
        const preset = await getBusinessPreset();
        // Each section is a complete value: the default settings are copied afresh here.
        setEdit((current) => current ? { ...current, draft: loadBusinessPreset(current.draft, preset) } : current);
      })}>{t("config.useDefaults")}</Button>
      {(dirty || candidate) && <p>{dirty ? t("config.revision.dirty") : t("config.revision.saved")}</p>}
      {edit.draft.business && <BusinessSnapshotFields value={edit.draft.business} managedAnimeID={animeID} onChange={(business) => setEdit({ ...edit, draft: { ...edit.draft, business } })} />}
      <RulesSectionsFields sections={edit.draft.rules.sections} onChange={(sections) => setEdit({ ...edit, draft: { ...edit.draft, rules: { sections } } })} />
      <Button disabled={busy || !dirty} onClick={() => void run(async () => {
        const draft = await freezeBusinessDraft(edit.draft);
        const store = getCandidateStore();
        const saved = candidate ? await store.patch(candidate.id, draft, candidate.version) : await store.save(draft);
        setCandidate(saved); setEdit({ ...edit, draft: structuredClone(saved.draft) });
      })}>{t("config.revision.save")}</Button>
      <Button disabled={busy || dirty || !candidate?.current_revision} onClick={() => void run(async () => {
        if (!candidate?.current_revision) return;
        await applyAnimeConfigCandidate(candidate.id, {
          target_managed_anime_id: animeID, revision: candidate.current_revision,
          expected_candidate_version: candidate.version, expected_owner_versions: edit.expected_owner_versions,
        }, newUUID());
        const value = await getAnimeBusinessEdit(animeID); setBaseline(value); setEdit(structuredClone(value)); setCandidate(undefined); onApplied();
      })}>{t("config.revision.apply")}</Button>
    </>}
    {error && <p role="alert">{error}</p>}
  </section>;
}
