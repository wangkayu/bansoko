import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { toast } from "../ui/toast";
import {
  isVersionConflict,
  listGlobalSections,
  putGlobalSection,
  toUserMessage,
  type GlobalSectionName,
  type GlobalSectionView,
} from "./api/client";
import { configKeys } from "./queryKeys";
import { valuesEqual } from "./draft";
import type { SaveOutcome } from "./saveParts";

// Shared global-section draft lifecycle. Field conversion and validation stay in
// each consumer so independent nested forms/resources are never merged here.
export type SettingsSectionDraftAdapter<Draft extends Record<string, unknown>> = {
  initialDraft: () => Draft;
  draftFromSection: (section: GlobalSectionView | undefined) => Draft;
};

export type UseSettingsSectionOptions<Draft extends Record<string, unknown>> =
  SettingsSectionDraftAdapter<Draft> & {
    sectionName: GlobalSectionName;
    successMessage: string;
    failureFallback: string;
    onDirtyChange?: (dirty: boolean) => void;
  };

export type UseSettingsSectionResult<Draft extends Record<string, unknown>> = {
  section: GlobalSectionView | undefined;
  sectionsQuery: UseQueryResult<GlobalSectionView[]>;
  draft: Draft;
  dirty: boolean;
  /** The draft genuinely differs from the server value (dirty only means touched; reverting to the original is not unsaved). */
  changed: boolean;
  saving: boolean;
  error: string | undefined;
  conflict: number | undefined;
  updateDraft: (update: (current: Draft) => Draft) => void;
  save: () => Promise<void>;
  /** For surface-level save orchestration: submits without toasting, handing the success copy to the caller. */
  submit: () => Promise<SaveOutcome>;
  refreshAfterConflict: () => Promise<void>;
  resetDraft: () => void;
};

export function useSettingsSection<Draft extends Record<string, unknown>>(
  options: UseSettingsSectionOptions<Draft>,
): UseSettingsSectionResult<Draft> {
  const {
    sectionName,
    initialDraft,
    draftFromSection,
    successMessage,
    failureFallback,
    onDirtyChange,
  } = options;
  const queryClient = useQueryClient();
  const sectionsQuery = useQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
  const section = useMemo(
    () => (sectionsQuery.data ?? []).find((item) => item.section_name === sectionName),
    [sectionName, sectionsQuery.data],
  );
  const [draft, setDraft] = useState<Draft>(initialDraft);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  useEffect(() => {
    if (!dirty && section) setDraft(draftFromSection(section));
  }, [dirty, draftFromSection, section]);

  useEffect(() => {
    onDirtyChange?.(dirty);
    return () => onDirtyChange?.(false);
  }, [dirty, onDirtyChange]);

  function updateDraft(update: (current: Draft) => Draft) {
    setDraft((current) => update(current));
    setDirty(true);
    setError(undefined);
  }

  const changed = useMemo(
    () => dirty && section !== undefined && !valuesEqual(draft, draftFromSection(section)),
    [dirty, draft, draftFromSection, section],
  );

  async function submit(): Promise<SaveOutcome> {
    if (!section) return { ok: false };
    setSaving(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const stored = await putGlobalSection(
        sectionName,
        { schema_version: section.schema_version, value: draft },
        section.version,
      );
      // Write the response into the cache before clearing dirty: otherwise the draft gets clobbered by the old value until the re-read returns.
      queryClient.setQueryData<GlobalSectionView[]>(configKeys.globalSections, (current) =>
        current?.map((item) => (item.section_name === sectionName ? stored : item)),
      );
      setDirty(false);
      void queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
      return { ok: true, message: successMessage };
    } catch (cause) {
      if (isVersionConflict(cause)) setConflict(cause.currentVersion);
      setError(toUserMessage(cause, failureFallback));
      return { ok: false };
    } finally {
      setSaving(false);
    }
  }

  async function save() {
    if (!section) return;
    setSaving(true);
    setError(undefined);
    setConflict(undefined);
    try {
      await putGlobalSection(
        sectionName,
        { schema_version: section.schema_version, value: draft },
        section.version,
      );
      setDirty(false);
      toast.success(successMessage);
      await queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
    } catch (cause) {
      if (isVersionConflict(cause)) setConflict(cause.currentVersion);
      setError(toUserMessage(cause, failureFallback));
    } finally {
      setSaving(false);
    }
  }

  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await queryClient.fetchQuery({
      queryKey: configKeys.globalSections,
      queryFn: listGlobalSections,
    });
    const latest = fresh.find((item) => item.section_name === sectionName);
    if (latest) setDraft(draftFromSection(latest));
    setDirty(false);
  }

  function resetDraft() {
    setConflict(undefined);
    setError(undefined);
    if (section) setDraft(draftFromSection(section));
    setDirty(false);
  }

  return {
    section,
    sectionsQuery,
    draft,
    dirty,
    changed,
    saving,
    error,
    conflict,
    updateDraft,
    save,
    submit,
    refreshAfterConflict,
    resetDraft,
  };
}
