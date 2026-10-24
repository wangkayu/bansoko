import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil } from "lucide-react";
import { Alert, Button, Input, Select, StateBlock } from "../../ui/base";
import { Chip, ConflictBlock, DescriptionList } from "../../ui";
import { toast } from "../../ui/toast";
import {
  getNamingTemplateLibrary,
  isVersionConflict,
  putNamingTemplateLibrary,
  toUserMessage,
  type NamingTemplate,
  type NamingTemplateLibraryView,
} from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";
import { NamingTemplateFields } from "../naming/NamingTemplateFields";
import { namingTemplateBody } from "../naming/templateBody";
import { SettingsEditDialog } from "./SettingsEditDialog";

// The naming template library (the only edit surface inside global settings): a template list plus
//   one default template. The default template is the global naming that every anime without its
//   own naming choice uses; a per-anime template pick is simply a copy. At least one template must
//   survive; the default cannot be deleted — set another template as default first.

/** A summary's template always renders as plain body text: the outer backticks are the server's to add, never written or seen here. */
function namingSummaryItems(rules: NamingTemplateLibraryView["templates"][number]["rules"]) {
  return [
    { label: t("settings.rules.namingTv"), value: rules.tv.directory_template },
    { label: t("settings.rules.namingTvVideo"), value: rules.tv.video_filename_template },
    { label: t("settings.rules.namingMovie"), value: rules.movie.directory_template },
    { label: t("settings.rules.namingMovieVideo"), value: rules.movie.video_filename_template },
    { label: t("settings.rules.namingSubtitle"), value: rules.subtitle_suffix_template },
  ];
}

export function NamingTemplatesCard() {
  const queryClient = useQueryClient();
  const library = useQuery({ queryKey: configKeys.namingTemplates, queryFn: getNamingTemplateLibrary });
  const [editing, setEditing] = useState<NamingTemplateLibraryView>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  const data = library.data;
  const defaultTemplate = data?.templates.find((item) => item.key === data.default_key);

  function open(view: NamingTemplateLibraryView) {
    setEditing(view);
    setError(undefined);
    setConflict(undefined);
  }

  async function save(base: NamingTemplateLibraryView, defaultKey: string, templates: NamingTemplate[]) {
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      const saved = await putNamingTemplateLibrary({ default_key: defaultKey, templates }, base.version);
      queryClient.setQueryData(configKeys.namingTemplates, saved);
      toast.success(t("settings.rules.saved", { title: t("settings.rules.namingRules") }));
      setEditing(undefined);
      await queryClient.invalidateQueries({ queryKey: configKeys.globalSections });
    } catch (cause) {
      if (isVersionConflict(cause)) setConflict(cause.currentVersion);
      setError(toUserMessage(cause, t("settings.rules.saveFailed")));
    } finally {
      setBusy(false);
    }
  }

  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await queryClient.fetchQuery({ queryKey: configKeys.namingTemplates, queryFn: getNamingTemplateLibrary });
    setEditing(fresh);
  }

  return (
    <li className="settings-shared-card">
      <div className="settings-shared-header">
        <h3 className="settings-shared-card-title text-body">{t("settings.rules.namingRules")}</h3>
        {data && (
          <Button variant="ghost" size="sm" onClick={() => open(data)}>
            <Pencil size={14} />
            {t("settings.rules.edit")}
          </Button>
        )}
      </div>
      <div className="settings-shared-offset">
        {library.isLoading && <StateBlock kind="busy" title={t("settings.rules.loading")} />}
        {library.error && <Alert tone="error" title={toUserMessage(library.error, t("settings.rules.templates.loadFailed"))} />}
        {data && defaultTemplate && (
          <DescriptionList
            items={[
              { label: t("settings.rules.templates.defaultTemplate"), value: defaultTemplate.name },
              { label: t("settings.rules.templates.all"), value: data.templates.map((item) => item.name).join(" · ") },
              ...namingSummaryItems(defaultTemplate.rules),
            ]}
          />
        )}
      </div>
      {editing && (
        <NamingTemplatesDialog
          key={editing.version}
          view={editing}
          busy={busy}
          error={error}
          conflict={conflict}
          onRefreshConflict={() => void refreshAfterConflict()}
          onClose={() => setEditing(undefined)}
          onSave={(defaultKey, templates) => void save(editing, defaultKey, templates)}
        />
      )}
    </li>
  );
}

function newTemplateKey(taken: Set<string>): string {
  for (;;) {
    const key = `t-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
    if (!taken.has(key)) return key;
  }
}

function copyName(source: string, taken: Set<string>): string {
  let name = t("settings.rules.templates.copyName", { name: source });
  for (let n = 2; taken.has(name); n += 1) {
    name = `${t("settings.rules.templates.copyName", { name: source })} ${n}`;
  }
  return name;
}

function NamingTemplatesDialog({
  view,
  busy,
  error,
  conflict,
  onRefreshConflict,
  onClose,
  onSave,
}: {
  view: NamingTemplateLibraryView;
  busy: boolean;
  error?: string;
  conflict?: number;
  onRefreshConflict: () => void;
  onClose: () => void;
  onSave: (defaultKey: string, templates: NamingTemplate[]) => void;
}) {
  const [templates, setTemplates] = useState<NamingTemplate[]>(view.templates);
  const [defaultKey, setDefaultKey] = useState(view.default_key);
  const [selectedKey, setSelectedKey] = useState(view.default_key);
  const [dirty, setDirty] = useState(false);

  const selected = templates.find((item) => item.key === selectedKey) ?? templates[0]!;
  const isDefault = selected.key === defaultKey;

  function update(next: NamingTemplate[]) {
    setTemplates(next);
    setDirty(true);
  }

  function patchSelected(patch: Partial<NamingTemplate>) {
    update(templates.map((item) => (item.key === selected.key ? { ...item, ...patch } : item)));
  }

  function addCopy() {
    const key = newTemplateKey(new Set(templates.map((item) => item.key)));
    const name = copyName(selected.name.trim(), new Set(templates.map((item) => item.name.trim())));
    update([...templates, { key, name, rules: namingTemplateBody(selected.rules) }]);
    setSelectedKey(key);
  }

  function remove() {
    if (isDefault) return;
    const next = templates.filter((item) => item.key !== selected.key);
    update(next);
    setSelectedKey(defaultKey);
  }

  function submit(event: React.FormEvent) {
    event.preventDefault();
    onSave(
      defaultKey,
      templates.map((item) => ({ ...item, name: item.name.trim() })),
    );
  }

  return (
    <SettingsEditDialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title={t("settings.rules.editTitle", { title: t("settings.rules.namingRules") })}
      dirty={dirty}
      footer={(requestClose) => (
        <>
          <Button variant="ghost" disabled={busy} onClick={requestClose}>{t("settings.rules.cancel")}</Button>
          <Button variant="primary" disabled={busy} form="naming-templates-form" type="submit">
            {busy ? t("settings.rules.saving") : t("settings.rules.save")}
          </Button>
        </>
      )}
    >
      {conflict !== undefined ? (
        <ConflictBlock currentVersion={conflict} message={error} onRefresh={onRefreshConflict} />
      ) : (
        error && <Alert tone="error" title={error} />
      )}
      <form id="naming-templates-form" onSubmit={submit} className="settings-shared-grid">
        <div className="tw:flex tw:flex-wrap tw:items-end tw:gap-2">
          <label className="tw:grid tw:min-w-0 tw:flex-1 tw:basis-48 tw:gap-1 text-caption">
            {t("settings.rules.templates.select")}
            <Select
              label={t("settings.rules.templates.select")}
              value={selected.key}
              options={templates.map((item) => ({
                value: item.key,
                label: item.key === defaultKey ? t("settings.rules.templates.defaultOption", { name: item.name }) : item.name,
              }))}
              onChange={setSelectedKey}
            />
          </label>
          <Button variant="ghost" size="sm" disabled={busy} onClick={addCopy}>
            {t("settings.rules.templates.add")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={busy || isDefault}
            onClick={() => {
              setDefaultKey(selected.key);
              setDirty(true);
            }}
          >
            {t("settings.rules.templates.setDefault")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={busy || isDefault}
            title={isDefault ? t("settings.rules.templates.deleteDefaultHint") : undefined}
            onClick={remove}
          >
            {t("settings.rules.templates.delete")}
          </Button>
        </div>
        <p className="tw:m-0 tw:flex tw:items-center tw:gap-2 tw:text-xs tw:text-muted-foreground">
          {isDefault && <Chip tone="accent">{t("settings.rules.templates.defaultTemplate")}</Chip>}
          {isDefault ? t("settings.rules.templates.deleteDefaultHint") : t("settings.rules.templates.defaultNote")}
        </p>
        <label className="tw:grid tw:gap-1 text-caption">
          {t("settings.rules.templates.name")}
          <Input
            aria-label={t("settings.rules.templates.name")}
            value={selected.name}
            maxLength={64}
            required
            onChange={(event) => patchSelected({ name: event.target.value })}
          />
        </label>
        <NamingTemplateFields
          key={selected.key}
          value={selected.rules}
          samplePicker
          onChange={(rules) => patchSelected({ rules })}
        />
      </form>
    </SettingsEditDialog>
  );
}
