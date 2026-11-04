import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Alert, SpinnerIcon } from "../../ui/base";
import { listGlobalSections, putGlobalSection, toUserMessage, type GlobalSectionView } from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { useSavePart } from "../../lib/saveParts";
import { SwitchRow } from "../../ui/SaveFooter";
import { t } from "../../lib/i18n";

// "Release without metadata" under settings' subscription policy: a Global switch only, with no
//   per-anime setting. On, a subscription with no Bangumi or TMDB link is evaluated and admitted
//   against its manual values; the missing link stays a fact in readers and records. The server
//   re-enqueues the releases already waiting for metadata when the switch is turned on.

const SECTION = "metadata_hold";

export function MetadataHoldSettings() {
  const queryClient = useQueryClient();
  const sections = useQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
  const view = sections.data?.find((section) => section.section_name === SECTION);
  const [draft, setDraft] = useState<boolean>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const saved = allowMissingLinkageFromSection(view);
  const dirty = view !== undefined && draft !== undefined && draft !== saved;

  async function save() {
    if (!view || draft === undefined) return { ok: true as const };
    setSaving(true);
    setError(undefined);
    try {
      const stored = await putGlobalSection(SECTION, { schema_version: view.schema_version, value: { allow_missing_linkage: draft } }, view.version);
      queryClient.setQueryData<GlobalSectionView[]>(configKeys.globalSections, (current) =>
        current?.map((section) => (section.section_name === SECTION ? stored : section)),
      );
      void queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
      return { ok: true as const, message: t("metadataHold.saved") };
    } catch (cause) {
      setError(toUserMessage(cause, t("metadataHold.saveFailed")));
      return { ok: false as const };
    } finally {
      setSaving(false);
    }
  }
  useSavePart("metadata-hold", { count: dirty ? 1 : 0, label: t("metadataHold.title"), busy: saving, save });

  return (
    <div className="settings-policy-card">
      {sections.isLoading && <SpinnerIcon />}
      {sections.error && <Alert tone="error" title={toUserMessage(sections.error, t("metadataHold.loadFailed"))} />}
      {error && <Alert tone="error" title={error} />}
      {view && (
        <SwitchRow
          label={t("metadataHold.title")}
          description={t("metadataHold.description")}
          checked={draft ?? saved}
          disabled={saving}
          onCheckedChange={setDraft}
        />
      )}
    </div>
  );
}

function allowMissingLinkageFromSection(view?: GlobalSectionView): boolean {
  const allowed = view?.value.allow_missing_linkage;
  return typeof allowed === "boolean" ? allowed : false;
}
