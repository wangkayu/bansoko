import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Alert, SpinnerIcon } from "../../ui/base";
import { listGlobalSections, putGlobalSection, toUserMessage, type GlobalSectionView } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { useSavePart } from "../../lib/saveParts";
import { SwitchRow } from "../../ui/SaveFooter";
import { t } from "../../lib/i18n";

// "Periodic metadata refresh" under settings' subscription policy: a Global switch only, with no
//   per-anime setting. On, the server re-pulls Bangumi / TMDB data for active subscriptions' bound
//   works once a day; a failed re-pull keeps the cached rows and waits for the next cycle.

const SECTION = "metadata_refresh";

export function MetadataRefreshSettings() {
  const queryClient = useQueryClient();
  const sections = useQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
  const view = sections.data?.find((section) => section.section_name === SECTION);
  const [draft, setDraft] = useState<boolean>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const saved = enabledFromSection(view);
  const dirty = view !== undefined && draft !== undefined && draft !== saved;

  async function save() {
    if (!view || draft === undefined) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    try {
      const stored = await putGlobalSection(SECTION, { schema_version: view.schema_version, value: { enabled: draft } }, view.version);
      queryClient.setQueryData<GlobalSectionView[]>(configKeys.globalSections, (current) =>
        current?.map((section) => (section.section_name === SECTION ? stored : section)),
      );
      void queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
      return { ok: true as const, message: t("metadataRefresh.saved") };
    } catch (cause) {
      setError(toUserMessage(cause, t("metadataRefresh.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }
  useSavePart("metadata-refresh", { count: dirty ? 1 : 0, label: t("metadataRefresh.title"), busy: saving, save });

  return (
    <div className="settings-policy-card">
      {sections.isLoading && <SpinnerIcon />}
      {sections.error && <Alert tone="error" title={toUserMessage(sections.error, t("metadataRefresh.loadFailed"))} />}
      {error && <Alert tone="error" title={error} />}
      {view && (
        <SwitchRow
          label={t("metadataRefresh.title")}
          description={t("metadataRefresh.description")}
          checked={draft ?? saved}
          disabled={saving}
          onCheckedChange={setDraft}
        />
      )}
    </div>
  );
}

function enabledFromSection(view?: GlobalSectionView): boolean {
  const enabled = view?.value.enabled;
  return typeof enabled === "boolean" ? enabled : false;
}
