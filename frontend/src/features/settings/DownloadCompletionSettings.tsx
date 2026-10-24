import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { Alert, Button, Checkbox, HelpTip, Input, Select } from "../../ui/base";
import { ConflictBlock, DescriptionList } from "../../ui";
import { toast } from "../../ui/toast";
import {
  isVersionConflict,
  listGlobalSections,
  putGlobalSection,
  toUserMessage,
  type DownloadCompletionAction,
  type GlobalSectionView,
} from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { valuesEqual } from "../../lib/draft";
import {
  downloadCompletionForm,
  downloadCompletionNote,
  downloadCompletionOptions,
  downloadCompletionRules,
  downloadCompletionValue,
  seedLimitActionOptions,
  seedLimitsSummary,
  seedTimeUnitOptions,
  type DownloadCompletionForm,
  type SeedLimitAction,
  type SeedTimeUnit,
} from "../../lib/downloadCompletion";
import { joinSentences } from "../../lib/i18n/format";
import { t } from "../../lib/i18n";
import { sectionSourceLabel } from "./sectionBadge";
import { SettingsEditDialog } from "./SettingsEditDialog";

// "After download completes" under settings' acquisition section: this global value is the only one
//   there, with no per-anime setting. Continued seeding may carry a retention cap; once the cap is
//   written to the downloader, the downloader enforces it.

export function DownloadCompletionSettings({ importMethod }: { importMethod?: string }) {
  const queryClient = useQueryClient();
  const sections = useQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
  const view = sections.data?.find((section) => section.section_name === "download_completion");
  const [editing, setEditing] = useState<GlobalSectionView>();

  if (!view) return null;
  const rules = downloadCompletionRules(view.value);
  const items = [{ label: t("downloadCompletion.field"), value: t(`downloadCompletion.action.${rules.action}`) }];
  if (rules.action === "seed") {
    items.push({ label: t("downloadCompletion.limitsLabel"), value: seedLimitsSummary(rules.seed_limits) });
    if (rules.seed_limits) items.push({ label: t("downloadCompletion.atLimitLabel"), value: t(`downloadCompletion.atLimit.${rules.seed_limits.at_limit}`) });
  }
  const note = downloadCompletionNote(rules.action, importMethod);

  return (
    <div className="settings-shared-card">
      <div className="settings-shared-header">
        <h3 className="settings-shared-card-title text-body">{t("downloadCompletion.title")}</h3>
        <div className="settings-shared-inline">
          <span className="settings-shared-muted text-badge">{sectionSourceLabel(view)}</span>
          <Button variant="ghost" size="sm" aria-label={t("downloadCompletion.editTitle")} onClick={() => setEditing(view)}>
            <Pencil size={14} />
            {t("downloadCompletion.edit")}
          </Button>
        </div>
      </div>
      <div className="settings-shared-offset">
        <DescriptionList items={items} />
        {note && <p className="settings-shared-description text-caption">{note}</p>}
      </div>
      {editing && (
        <DownloadCompletionDialog
          view={editing}
          importMethod={importMethod}
          onClose={() => setEditing(undefined)}
          onSaved={async () => {
            setEditing(undefined);
            await queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
          }}
          onRefresh={async () => {
            const fresh = await queryClient.fetchQuery({ queryKey: configKeys.globalSections, queryFn: listGlobalSections });
            setEditing(fresh.find((section) => section.section_name === "download_completion"));
          }}
        />
      )}
    </div>
  );
}

function DownloadCompletionDialog({
  view,
  importMethod,
  onClose,
  onSaved,
  onRefresh,
}: {
  view: GlobalSectionView;
  importMethod?: string;
  onClose: () => void;
  onSaved: () => Promise<void>;
  onRefresh: () => Promise<void>;
}) {
  const initial = downloadCompletionForm(downloadCompletionRules(view.value));
  const [form, setForm] = useState<DownloadCompletionForm>(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [invalid, setInvalid] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  function update(change: Partial<DownloadCompletionForm>) {
    setForm((current) => ({ ...current, ...change }));
    setInvalid(undefined);
  }

  async function submit() {
    const result = downloadCompletionValue(form);
    if ("error" in result) {
      setInvalid(result.error);
      return;
    }
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      await putGlobalSection("download_completion", { schema_version: view.schema_version, value: result.value }, view.version);
      toast.success(t("downloadCompletion.saved"));
      await onSaved();
    } catch (cause) {
      if (isVersionConflict(cause)) setConflict(cause.currentVersion);
      setError(toUserMessage(cause, t("downloadCompletion.saveFailed")));
    } finally {
      setBusy(false);
    }
  }

  const note = downloadCompletionNote(form.action, importMethod);
  const seeding = form.action === "seed";
  return (
    <SettingsEditDialog
      open
      onOpenChange={(open) => { if (!open) onClose(); }}
      title={t("downloadCompletion.editTitle")}
      description={t("downloadCompletion.hint")}
      dirty={() => !valuesEqual(form, initial)}
      footer={(requestClose) => (
        <>
          <Button variant="ghost" disabled={busy} onClick={requestClose}>{t("downloadCompletion.cancel")}</Button>
          <Button variant="primary" aria-label={busy ? t("downloadCompletion.saving") : t("downloadCompletion.save")} disabled={busy} form="download-completion-form" type="submit">
            {busy ? t("downloadCompletion.saving") : t("downloadCompletion.save")}
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
      <form id="download-completion-form" onSubmit={(event) => { event.preventDefault(); void submit(); }} className="settings-shared-grid">
        <Field label={t("downloadCompletion.field")} help={joinSentences(t("downloadCompletion.help"), t("downloadCompletion.seedHelp"))}>
          <Select<DownloadCompletionAction>
            label={t("downloadCompletion.field")}
            value={form.action}
            options={downloadCompletionOptions()}
            onChange={(action) => update({ action })}
          />
        </Field>
        {note && (
          <p role={form.action === "remove_with_data" ? "alert" : undefined} className={form.action === "remove_with_data" ? "tw:text-xs tw:text-destructive" : "tw:text-xs tw:text-muted-foreground"}>
            {note}
          </p>
        )}
        {seeding && (
          <Checkbox checked={form.limited} onCheckedChange={(limited) => update({ limited })}>
            {t("downloadCompletion.limited")}
          </Checkbox>
        )}
        {seeding && form.limited && (
          <div className="settings-shared-grid">
            <p className="tw:text-xs tw:text-muted-foreground">{t("downloadCompletion.limitsHint")}</p>
            <div className="settings-shared-responsive-grid">
              <Field label={t("downloadCompletion.seedingTime")}>
                <Input aria-label={t("downloadCompletion.seedingTime")} inputMode="numeric" value={form.time} onChange={(event) => update({ time: event.target.value })} />
              </Field>
              <Field label={t("downloadCompletion.unitLabel")}>
                <Select<SeedTimeUnit> label={t("downloadCompletion.unitLabel")} value={form.unit} options={seedTimeUnitOptions()} onChange={(unit) => update({ unit })} />
              </Field>
              <Field label={t("downloadCompletion.ratio")}>
                <Input aria-label={t("downloadCompletion.ratio")} inputMode="decimal" value={form.ratio} onChange={(event) => update({ ratio: event.target.value })} />
              </Field>
              <Field label={t("downloadCompletion.atLimitLabel")}>
                <Select<SeedLimitAction> label={t("downloadCompletion.atLimitLabel")} value={form.atLimit} options={seedLimitActionOptions()} onChange={(atLimit) => update({ atLimit })} />
              </Field>
            </div>
            {form.atLimit === "remove_with_data" && (
              <p role="alert" className="tw:text-xs tw:text-destructive">{t("downloadCompletion.removeDataWarning")}</p>
            )}
          </div>
        )}
        {invalid && <p role="alert" className="tw:text-xs tw:text-destructive">{invalid}</p>}
      </form>
    </SettingsEditDialog>
  );
}

function Field({ label, help, children }: { label: string; help?: string; children: React.ReactNode }) {
  return (
    <div className="settings-shared-field text-caption">
      <span className="tw:inline-flex tw:items-center tw:gap-1">
        {label}
        {help && <HelpTip label={t("downloadCompletion.helpLabel")}>{help}</HelpTip>}
      </span>
      {children}
    </div>
  );
}
