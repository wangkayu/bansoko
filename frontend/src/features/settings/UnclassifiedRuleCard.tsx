import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { getFileEffectPolicyDefault, isVersionConflict, saveFileEffectPolicyDefault, toUserMessage, type FileEffectPolicyDefaultView } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { ConflictBlock, DescriptionList } from "../../ui";
import { Alert, Button, Select, StateBlock } from "../../ui/base";
import { toast } from "../../ui/toast";
import { SettingsEditDialog } from "./SettingsEditDialog";

type Route = FileEffectPolicyDefaultView["unclassified_route"];

export function UnclassifiedRuleCard() {
  const queryClient = useQueryClient();
  const queryKey = fileMaintenanceKeys.fileEffectDefault;
  const query = useQuery({ queryKey, queryFn: getFileEffectPolicyDefault });
  const [editing, setEditing] = useState<FileEffectPolicyDefaultView>();
  const [route, setRoute] = useState<Route>("skip");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  const title = t("file.unclassifiedTitle");
  const field = t("settings.replacement.unclassified.selectLabel");
  const dirty = editing !== undefined && route !== editing.unclassified_route;

  function edit(view: FileEffectPolicyDefaultView) {
    setEditing(view);
    setRoute(view.unclassified_route);
    setError(undefined);
    setConflict(undefined);
  }

  async function save() {
    if (!editing || !dirty || busy) return;
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const saved = await saveFileEffectPolicyDefault({ expected_version: editing.version, unclassified_route: route });
      queryClient.setQueryData(queryKey, saved);
      setEditing(undefined);
      toast.success(t("settings.replacement.unclassified.saved"));
    } catch (cause) {
      if (isVersionConflict(cause)) setConflict(cause.currentVersion ?? 0);
      setError(toUserMessage(cause, t("settings.replacement.unclassified.saveFailed")));
    } finally {
      setBusy(false);
    }
  }

  async function refresh() {
    const fresh = await query.refetch();
    if (fresh.isSuccess) edit(fresh.data);
    else setError(toUserMessage(fresh.error, t("settings.replacement.unclassified.loadFailed")));
  }

  return (
    <li className="settings-shared-card">
      <div className="settings-shared-header">
        <h3 className="settings-shared-card-title text-body">{title}</h3>
        <Button variant="ghost" size="sm" disabled={!query.data} onClick={() => { if (query.data) edit(query.data); }}>
          <Pencil size={14} />{t("settings.rules.edit")}
        </Button>
      </div>
      <div className="settings-shared-offset">
        {query.data && <DescriptionList items={[{ label: field, value: t(`file.unclassified.${query.data.unclassified_route}`) }]} />}
        {query.isLoading && <StateBlock kind="busy" title={t("settings.replacement.unclassified.loading")} />}
        {query.error && <Alert tone="error" title={toUserMessage(query.error, t("settings.replacement.unclassified.loadFailed"))} />}
      </div>
      {editing && (
        <SettingsEditDialog open onOpenChange={(open) => { if (!open) setEditing(undefined); }} title={t("settings.rules.editTitle", { title })} dirty={dirty} busy={busy}
          footer={(requestClose) => <>
            <Button variant="ghost" disabled={busy} onClick={requestClose}>{t("settings.rules.cancel")}</Button>
            <Button variant="primary" disabled={!dirty || busy || conflict !== undefined} onClick={() => void save()}>{t(busy ? "settings.rules.saving" : "settings.rules.save")}</Button>
          </>}
        >
          {conflict !== undefined
            ? <ConflictBlock currentVersion={conflict} message={error} onRefresh={() => void refresh()} />
            : error && <Alert tone="error" title={error} />}
          <Select label={field} value={route} options={(["skip", "route"] as const).map((value) => ({ value, label: t(`file.unclassified.${value}`) }))} disabled={busy} onChange={setRoute} />
          <p className="text-caption text-muted">{t(route === "skip" ? "settings.replacement.unclassified.skipHint" : "settings.replacement.unclassified.routeHint")}</p>
        </SettingsEditDialog>
      )}
    </li>
  );
}
