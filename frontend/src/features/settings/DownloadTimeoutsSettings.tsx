import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { Alert, Button, HelpTip, Input, Select } from "../../ui/base";
import { ConflictBlock, DescriptionList } from "../../ui";
import { toast } from "../../ui/toast";
import {
  isVersionConflict,
  listGlobalSections,
  putGlobalSection,
  toUserMessage,
  type GlobalSectionView,
} from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { valuesEqual } from "../../lib/draft";
import {
  downloadTimeoutsForm,
  downloadTimeoutsFromForm,
  downloadTimeoutsValue,
  formatDuration,
  timeoutUnitOptions,
  type DownloadTimeoutsForm,
  type DurationField,
  type TimeoutUnit,
} from "../../lib/downloadTimeouts";
import { t } from "../../lib/i18n";
import { sectionSourceLabel } from "./sectionBadge";
import { SettingsEditDialog } from "./SettingsEditDialog";

// "Download timeouts" under settings' acquisition section: this global value
//   (download_timeouts) is the only one. It is frozen into a download when the download starts;
//   later edits affect only newly started downloads.

const SECTION = "download_timeouts";

export function DownloadTimeoutsSettings() {
  const queryClient = useQueryClient();
  const sections = useQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
  const view = sections.data?.find((section) => section.section_name === SECTION);
  const [editing, setEditing] = useState<GlobalSectionView>();

  if (!view) return null;
  const value = downloadTimeoutsValue(view.value);

  return (
    <div className="settings-shared-card">
      <div className="settings-shared-header">
        <h3 className="settings-shared-card-title text-body">
          <span className="tw:inline-flex tw:items-center tw:gap-1">
            {t("downloadTimeouts.title")}
            <HelpTip label={t("downloadTimeouts.helpLabel")}>{t("downloadTimeouts.help")}</HelpTip>
          </span>
        </h3>
        <div className="settings-shared-inline">
          <span className="settings-shared-muted text-badge">{sectionSourceLabel(view)}</span>
          <Button variant="ghost" size="sm" aria-label={t("downloadTimeouts.editTitle")} onClick={() => setEditing(view)}>
            <Pencil size={14} />
            {t("downloadTimeouts.edit")}
          </Button>
        </div>
      </div>
      <div className="settings-shared-offset">
        <DescriptionList
          items={[
            { label: t("downloadTimeouts.stallLabel"), value: formatDuration(value.stall_seconds) },
            { label: t("downloadTimeouts.waitLabel"), value: formatDuration(value.download_wait_seconds) },
          ]}
        />
        <p className="settings-shared-description text-caption">{t("downloadTimeouts.hint")}</p>
      </div>
      {editing && (
        <DownloadTimeoutsDialog
          view={editing}
          onClose={() => setEditing(undefined)}
          onSaved={async () => {
            setEditing(undefined);
            await queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
          }}
          onRefresh={async () => {
            const fresh = await queryClient.fetchQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
            setEditing(fresh.find((section) => section.section_name === SECTION));
          }}
        />
      )}
    </div>
  );
}

function DownloadTimeoutsDialog({
  view,
  onClose,
  onSaved,
  onRefresh,
}: {
  view: GlobalSectionView;
  onClose: () => void;
  onSaved: () => Promise<void>;
  onRefresh: () => Promise<void>;
}) {
  const initial = downloadTimeoutsForm(downloadTimeoutsValue(view.value));
  const [form, setForm] = useState<DownloadTimeoutsForm>(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [invalid, setInvalid] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  function update(change: Partial<DownloadTimeoutsForm>) {
    setForm((current) => ({ ...current, ...change }));
    setInvalid(undefined);
  }

  async function submit() {
    const result = downloadTimeoutsFromForm(form);
    if ("error" in result) {
      setInvalid(result.error);
      return;
    }
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      await putGlobalSection(SECTION, { schema_version: view.schema_version, value: result.value }, view.version);
      toast.success(t("downloadTimeouts.saved"));
      await onSaved();
    } catch (cause) {
      if (isVersionConflict(cause)) setConflict(cause.currentVersion);
      setError(toUserMessage(cause, t("downloadTimeouts.saveFailed")));
    } finally {
      setBusy(false);
    }
  }

  return (
    <SettingsEditDialog
      open
      onOpenChange={(open) => { if (!open) onClose(); }}
      title={t("downloadTimeouts.editTitle")}
      description={t("downloadTimeouts.hint")}
      dirty={() => !valuesEqual(form, initial)}
      footer={(requestClose) => (
        <>
          <Button variant="ghost" disabled={busy} onClick={requestClose}>{t("downloadTimeouts.cancel")}</Button>
          <Button variant="primary" aria-label={busy ? t("downloadTimeouts.saving") : t("downloadTimeouts.save")} disabled={busy} form="download-timeouts-form" type="submit">
            {busy ? t("downloadTimeouts.saving") : t("downloadTimeouts.save")}
          </Button>
        </>
      )}
    >
      {conflict !== undefined ? (
        <ConflictBlock
          currentVersion={conflict}
          message={error}
          onRefresh={() => {
            setConflict(undefined);
            setError(undefined);
            void onRefresh();
          }}
        />
      ) : (
        error && <Alert tone="error" title={error} />
      )}
      <form id="download-timeouts-form" onSubmit={(event) => { event.preventDefault(); void submit(); }} className="settings-shared-grid">
        <DurationInput
          label={t("downloadTimeouts.stallLabel")}
          hint={t("downloadTimeouts.stallHint")}
          value={form.stall}
          onChange={(stall) => update({ stall })}
        />
        <DurationInput
          label={t("downloadTimeouts.waitLabel")}
          hint={t("downloadTimeouts.waitHint")}
          value={form.wait}
          onChange={(wait) => update({ wait })}
        />
        {invalid && <p role="alert" className="tw:text-xs tw:text-destructive">{invalid}</p>}
      </form>
    </SettingsEditDialog>
  );
}

function DurationInput({
  label,
  hint,
  value,
  onChange,
}: {
  label: string;
  hint: string;
  value: DurationField;
  onChange: (value: DurationField) => void;
}) {
  return (
    <div className="settings-shared-field text-caption">
      <span>{label}</span>
      <div className="settings-shared-responsive-grid">
        <Input
          aria-label={t("downloadTimeouts.amountAria", { field: label })}
          inputMode="numeric"
          value={value.amount}
          onChange={(event) => onChange({ ...value, amount: event.target.value })}
        />
        <Select<TimeoutUnit>
          label={t("downloadTimeouts.unitAria", { field: label })}
          value={value.unit}
          options={timeoutUnitOptions()}
          onChange={(unit) => onChange({ ...value, unit })}
        />
      </div>
      <span className="settings-shared-field-hint text-caption">{hint}</span>
    </div>
  );
}
