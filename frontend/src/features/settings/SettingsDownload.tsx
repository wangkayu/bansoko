import { Alert, Button, Checkbox, HelpTip, Input, Select, StateBlock } from "../../ui/base";
import { reasonLabel } from "../../lib/i18n/reasons";
import { DownloadTimeoutsSettings } from "./DownloadTimeoutsSettings";
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus } from "lucide-react";
import { toast } from "../../ui/toast";
import { valuesEqual } from "../../lib/draft";
import {
  isVersionConflict,
  listConnections,
  listNetworkRoutes,
  newUUID,
  putConnection,
  testConnection,
  toUserMessage,
  type Connection,
  type GlobalSectionView,
} from "../../lib/api/client";
import { testHealthLabel, testHealthTone } from "../../lib/labels";
import { configKeys } from "../../lib/queryKeys";
import { useSettingsSection } from "../../lib/settings";
import { Chip, DescriptionList, StatePill, ConflictBlock, SecretField, TagInput, type SecretMode } from "../../ui";
import { sectionSourceLabel } from "./sectionBadge";
import { DialogFeedback, SettingsEditDialog } from "./SettingsEditDialog";
import { DownloadCompletionSettings } from "./DownloadCompletionSettings";
import { t } from "../../lib/i18n";

// Acquisition: the qBittorrent connection plus the global file-effect defaults
//   (operation / collision_policy) plus "after download completes" (a global value only). All write
//   into global_config_sections; already-submitted downloads run on a frozen snapshot, so edits
//   affect only later tasks.
//   The qB connection fields follow the QBittorrentConnectionInput contract exactly
//   (name/enabled/base_url/username/secret/route_id/download_root/downloader_root); no extra fields
//   are invented.

const OPERATION_LABELS = (): Record<string, string> => ({
  copy: t("file.operation.copy"),
  move: t("file.operation.move"),
  hardlink: t("file.operation.hardlink"),
});

const COLLISION_LABELS = (): Record<string, string> => ({
  fail: t("file.collision.fail"),
  versioned: t("file.collision.versioned"),
  overwrite: t("file.collision.overwrite"),
});

type FileEffectDraft = Record<string, unknown> & {
  operation: string;
  collision_policy: string;
};

const emptyFileEffectDraft = (): FileEffectDraft => ({
  operation: "copy",
  collision_policy: "fail",
});

function fileEffectDraftFromSection(section?: GlobalSectionView): FileEffectDraft {
  const value = section?.value ?? {};
  return {
    operation: typeof value.operation === "string" ? value.operation : "copy",
    collision_policy: typeof value.collision_policy === "string" ? value.collision_policy : "fail",
  };
}

export function SettingsDownload() {
  const {
    section: fileEffect,
    sectionsQuery: sections,
    draft: fileEffectDraft,
    dirty: fileEffectDirty,
    saving: fileEffectSaving,
    error: fileEffectError,
    conflict: fileEffectConflict,
    updateDraft: updateFileEffectDraft,
    save: saveFileEffect,
    refreshAfterConflict: refreshFileEffectAfterConflict,
    resetDraft: resetFileEffectDraft,
  } = useSettingsSection<FileEffectDraft>({
    sectionName: "file_effect",
    initialDraft: emptyFileEffectDraft,
    draftFromSection: fileEffectDraftFromSection,
    successMessage: t("settings.download.saved"),
    failureFallback: t("settings.download.saveFailed"),
  });
  const [editing, setEditing] = useState(false);
  const closeAfterSave = useRef(false);

  useEffect(() => {
    if (closeAfterSave.current && !fileEffectSaving && !fileEffectDirty) {
      closeAfterSave.current = false;
      setEditing(false);
    }
  }, [fileEffectDirty, fileEffectSaving]);

  function openEditor() {
    resetFileEffectDraft();
    setEditing(true);
  }

  function closeEditor() {
    closeAfterSave.current = false;
    resetFileEffectDraft();
    setEditing(false);
  }

  function submitFileEffect() {
    closeAfterSave.current = true;
    void saveFileEffect();
  }

  return (
    <div className="settings-shared-stack">
      <QBittorrentCard />
      <section className="settings-shared-grid">
        <div className="settings-shared-header">
          <h2 className="settings-shared-title text-title">{t("settings.download.sectionTitle")}</h2>
          <span className="settings-shared-muted text-caption">{t("settings.download.sectionHint")}</span>
        </div>

        {sections.isLoading && (
          <StateBlock kind="busy" title={t("settings.download.loading")} />
        )}
        {sections.error && <Alert tone="error" title={toUserMessage(sections.error, t("settings.download.loadFailed"))} />}
        <DownloadTimeoutsSettings />

        {fileEffect && (
          <div className="settings-shared-card">
            <div className="settings-shared-header">
              <h3 className="settings-shared-card-title text-body">{t("settings.download.title")}</h3>
              <div className="settings-shared-inline">
                <span className="settings-shared-muted text-badge">
                  {sectionSourceLabel(fileEffect)}
                </span>
                <Button variant="ghost" size="sm" onClick={openEditor}>
                  <Pencil size={14} />
                  {t("settings.download.edit")}
                </Button>
              </div>
            </div>
            <div className="settings-shared-offset">
              <DescriptionList
                items={[
                  { label: t("file.operationTitle"), value: OPERATION_LABELS()[String(fileEffect.value.operation)] ?? String(fileEffect.value.operation) },
                  { label: t("file.collisionTitle"), value: COLLISION_LABELS()[String(fileEffect.value.collision_policy)] ?? String(fileEffect.value.collision_policy) },
                ]}
              />
            </div>
          </div>
        )}
        {fileEffect && <DownloadCompletionSettings importMethod={String(fileEffect.value.operation)} />}
      </section>

      {editing && (
        <SettingsEditDialog
          open
          onOpenChange={(open) => { if (!open) closeEditor(); }}
          title={t("settings.download.editTitle")}
          description={t("settings.download.editHint")}
          dirty={fileEffectDirty}
          footer={(requestClose) => (
            <>
              <Button variant="ghost" disabled={fileEffectSaving} onClick={requestClose}>{t("settings.download.cancel")}</Button>
              <Button variant="primary" aria-label={fileEffectSaving ? t("settings.download.saving") : t("settings.download.save")} disabled={fileEffectSaving} form="file-effect-form" type="submit">
                {fileEffectSaving ? t("settings.download.saving") : t("settings.download.save")}
              </Button>
            </>
          )}
        >
          {fileEffectConflict !== undefined ? (
            <ConflictBlock
              currentVersion={fileEffectConflict}
              message={fileEffectError}
              onRefresh={() => {
                closeAfterSave.current = false;
                void refreshFileEffectAfterConflict();
              }}
            />
          ) : (
            fileEffectError && <Alert tone="error" title={fileEffectError} />
          )}
          <FileEffectForm
            value={fileEffectDraft}
            onChange={(update) => updateFileEffectDraft((current) => ({ ...current, ...update }))}
            onSubmit={submitFileEffect}
          />
        </SettingsEditDialog>
      )}
    </div>
  );
}

function FileEffectForm({
  value,
  onChange,
  onSubmit,
}: {
  value: FileEffectDraft;
  onChange: (update: Pick<FileEffectDraft, "operation" | "collision_policy">) => void;
  onSubmit: () => void;
}) {
  return (
    <form id="file-effect-form" onSubmit={(event) => { event.preventDefault(); onSubmit(); }} className="settings-shared-grid">
      <Field label={t("file.operationTitle")} hint={t("settings.download.operationHint")}>
        <Select label={t("file.operationTitle")} value={value.operation} options={Object.entries(OPERATION_LABELS()).map(([optionValue, label]) => ({ value: optionValue, label }))} onChange={(operation) => onChange({ operation, collision_policy: value.collision_policy })} />
      </Field>
      <Field label={t("file.collisionTitle")} hint={value.collision_policy === "overwrite" ? t("file.collisionOverwriteNote") : t("settings.download.collisionHint")}>
        <Select label={t("file.collisionTitle")} value={value.collision_policy} options={Object.entries(COLLISION_LABELS()).map(([optionValue, label]) => ({ value: optionValue, label }))} onChange={(collision_policy) => onChange({ operation: value.operation, collision_policy })} />
      </Field>
    </form>
  );
}

// --- qBittorrent downloader connection (split out from service connections) ---

type ConnectionSecretInput = { mode: "keep" } | { mode: "replace"; value: string } | { mode: "remove" };

type QBDraft = {
  id?: string;
  version: number;
  name: string;
  enabled: boolean;
  base_url: string;
  username: string;
  password: string;
  secret_mode: SecretMode;
  // Whether the stored downloader already holds a password; without one the dialog works as on create.
  credential_configured: boolean;
  download_root: string;
  downloader_root: string;
  route_id: string;
  category: string;
  user_tags: string[];
};

function qbDraftFrom(item?: Connection): QBDraft {
  if (!item || item.kind !== "qbittorrent") {
    return {
      version: 0,
      name: "",
      enabled: true,
      base_url: "",
      username: "",
      password: "",
      secret_mode: "replace",
      credential_configured: false,
      download_root: "",
      downloader_root: "",
      route_id: "",
      category: "",
      user_tags: [],
    };
  }
  return {
    id: item.id,
    version: item.version,
    name: item.name,
    enabled: item.enabled,
    base_url: item.base_url,
    username: item.username,
    password: "",
    // Configured credentials are kept by default; an unconfigured one goes straight into the replace state, avoiding keep on empty credentials.
    secret_mode: item.credential_configured ? "keep" : "replace",
    credential_configured: item.credential_configured,
    download_root: item.download_root ?? "",
    downloader_root: item.downloader_root ?? "",
    route_id: item.route_id ?? "",
    category: item.category ?? "",
    user_tags: item.user_tags ?? [],
  };
}

// Field limits shared with the OpenAPI contract and the backend normalization.
const QB_NAME_MAX = 128;
const QB_USERNAME_MAX = 256;
const QB_SECRET_MAX = 1024;
const QB_CATEGORY_MAX = 256;
const QB_PATH_MAX = 1024;
const QB_TAG_MAX = 256;

function hasParentSegment(value: string): boolean {
  return value.replace(/\\/g, "/").split("/").includes("..");
}

// The address must be an http(s) URL; the server also refuses credentials, query and fragment in it.
function qbAddressValid(value: string): boolean {
  const trimmed = value.trim();
  if (trimmed === "") return false;
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    return false;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return false;
  return url.hostname !== "" && url.username === "" && url.password === "" && url.search === "" && url.hash === "";
}

type QBErrors = { address?: string; username?: string; downloadDir?: string; localDir?: string; blocked: boolean };

// A downloader cannot run without a username and a password, so those two are required whenever the
//   password is being entered (create, or replacing the stored one); keep needs no input.
function qbErrors(draft: QBDraft): QBErrors {
  const passwordEntered = !draft.credential_configured || draft.secret_mode === "replace";
  const address = draft.base_url.trim() !== "" && !qbAddressValid(draft.base_url) ? t("settings.download.addressInvalid") : undefined;
  const username = draft.username.trim() === "" && (draft.base_url.trim() !== "" || draft.password !== "") ? t("settings.download.usernameRequired") : undefined;
  const downloadDir = draft.download_root.trim() === ""
    ? (draft.base_url.trim() !== "" || draft.username.trim() !== "" || draft.password !== "" ? t("settings.download.downloadDirRequired") : undefined)
    : hasParentSegment(draft.download_root) ? t("settings.download.pathInvalid") : undefined;
  const localDir = hasParentSegment(draft.downloader_root) ? t("settings.download.pathInvalid") : undefined;
  const blocked =
    draft.name.trim() === "" ||
    !qbAddressValid(draft.base_url) ||
    draft.username.trim() === "" ||
    (passwordEntered && draft.password === "") ||
    draft.download_root.trim() === "" ||
    downloadDir !== undefined ||
    localDir !== undefined;
  return { address, username, downloadDir, localDir, blocked };
}

function QBittorrentCard() {
  const queryClient = useQueryClient();
  const connections = useQuery({ queryKey: configKeys.connections, queryFn: listConnections });
  const routes = useQuery({ queryKey: configKeys.networkRoutes, queryFn: listNetworkRoutes });
  const [draft, setDraft] = useState<QBDraft>();
  const [savedDraft, setSavedDraft] = useState<QBDraft>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();
  const [testing, setTesting] = useState<string>();
  const [testResults, setTestResults] = useState<Record<string, { health: "ready" | "degraded" | "failed"; text?: string }>>({});

  const downloaders = (connections.data ?? []).filter((item) => item.kind === "qbittorrent");

  // The connection secret's states (the ConnectionSecret contract): a downloader cannot run without
  //   its password, so create and replace write replace and editing a stored one may only keep or replace.
  function buildSecret(value: string): ConnectionSecretInput {
    if (!draft || !draft.credential_configured || draft.secret_mode === "replace") return { mode: "replace", value };
    return { mode: "keep" };
  }

  async function save() {
    if (!draft) return;
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      await putConnection(draft.id ?? newUUID(), {
        kind: "qbittorrent",
        name: draft.name.trim(),
        enabled: draft.enabled,
        base_url: draft.base_url.trim(),
        username: draft.username.trim(),
        secret: buildSecret(draft.password),
        route_id: draft.route_id === "" ? undefined : draft.route_id,
        download_root: draft.download_root.trim(),
        downloader_root: draft.downloader_root.trim() === "" ? undefined : draft.downloader_root.trim(),
        category: draft.category.trim() === "" ? undefined : draft.category.trim(),
        user_tags: draft.user_tags.length === 0 ? undefined : draft.user_tags,
      }, draft.version);
      toast.success(t("settings.download.downloaderSaved"));
      setDraft(undefined);
      setSavedDraft(undefined);
      await queryClient.invalidateQueries({ queryKey: configKeys.connections });
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, t("settings.download.downloaderSaveFailed")));
    } finally {
      setBusy(false);
    }
  }

  // On a 409 refresh: load the server's latest downloader, discard the stale local draft, and the user re-edits.
  function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    setDraft(undefined);
    setSavedDraft(undefined);
    void queryClient.invalidateQueries({ queryKey: configKeys.connections });
  }

  async function test(item: Connection) {
    setTesting(item.id);
    try {
      const result = await testConnection(item.id);
      setTestResults((prev) => ({
        ...prev,
        [item.id]: { health: result.health, text: reasonLabel(result.reason_code) },
      }));
    } catch (cause) {
      setTestResults((prev) => ({
        ...prev,
        [item.id]: { health: "failed", text: toUserMessage(cause, t("settings.download.testFailed")) },
      }));
    } finally {
      setTesting(undefined);
    }
  }

  const errors = draft ? qbErrors(draft) : { blocked: true };

  return (
    <section className="settings-shared-grid">
      <div className="settings-shared-header">
        <h2 className="settings-shared-title text-title">{t("settings.download.downloaders")}</h2>
        {connections.data && downloaders.length === 0 && (
          <Button variant="primary" size="sm" onClick={() => { const next = qbDraftFrom(); setError(undefined); setConflict(undefined); setDraft(next); setSavedDraft(next); }}>
            <Plus size={14} />
            {t("settings.download.newDownloader")}
          </Button>
        )}
      </div>

      {connections.isLoading && (
        <StateBlock kind="busy" title={t("settings.download.loadingDownloaders")} />
      )}
      {connections.error && <Alert tone="error" title={toUserMessage(connections.error, t("settings.connections.loadFailed"))} />}
      {connections.data && downloaders.length === 0 && !draft && (
        <StateBlock kind="empty" title={t("settings.download.empty")} />
      )}

      <ul className="settings-shared-list">
        {downloaders.map((item) => (
          <li key={item.id} className="settings-shared-card">
            <div className="settings-shared-item-content">
              <span className="settings-shared-card-title text-body">{item.name}</span>
              {/* Skip the type badge when the name is exactly "qBittorrent" (the default name) */}
              {item.name.trim().toLowerCase() !== "qbittorrent" && <Chip>qBittorrent</Chip>}
              <StatePill tone={item.enabled ? "online" : "muted"}>{item.enabled ? t("common.enabled") : t("common.disabled")}</StatePill>
              {testResults[item.id] && (
                <StatePill tone={testHealthTone(testResults[item.id]!.health)}>
                  {testHealthLabel(testResults[item.id]!.health)}
                </StatePill>
              )}
              <span className="settings-shared-spacer" />
              <Button variant="ghost" aria-label={t("settings.download.edit")} size="sm" onClick={() => { const next = qbDraftFrom(item); setError(undefined); setConflict(undefined); setDraft(next); setSavedDraft(next); }}>
                <Pencil size={14} />
                {t("settings.download.edit")}
              </Button>
              <Button aria-label={testing === item.id ? t("settings.download.testing") : t("settings.download.test")} size="sm" loading={testing === item.id} disabled={testing === item.id} onClick={() => void test(item)}>
                {testing === item.id ? t("settings.download.testing") : t("settings.download.test")}
              </Button>
            </div>
            <p className="settings-shared-description text-caption">
              {item.kind === "qbittorrent" ? item.base_url : ""}
              {testResults[item.id]?.text && ` · ${testResults[item.id]!.text}`}
            </p>
          </li>
        ))}
      </ul>

      {draft && (
        <SettingsEditDialog
          open
          onOpenChange={(o) => { if (!o) { setDraft(undefined); setSavedDraft(undefined); } }}
          busy={busy}
          title={draft.id ? t("settings.download.editDownloader") : t("settings.download.newDownloaderTitle")}
          dirty={() => !!draft && !valuesEqual(draft, savedDraft)}
          footer={(requestClose) => (
            <>
              <Button variant="ghost" disabled={busy} onClick={requestClose}>{t("settings.download.cancel")}</Button>
              <Button variant="primary" type="submit" form="downloader-form" aria-label={busy ? t("settings.download.saving") : t("settings.download.save")} disabled={busy || errors.blocked}>
                {busy ? t("settings.download.saving") : t("settings.download.save")}
              </Button>
            </>
          )}
        >
          {conflict !== undefined ? (
            <DialogFeedback>
              <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
            </DialogFeedback>
          ) : (
            error && <DialogFeedback><Alert tone="error" title={error} /></DialogFeedback>
          )}
          <form
            id="downloader-form"
            className="tw:grid tw:grid-cols-2 tw:items-start tw:gap-3 tw:phone:grid-cols-1"
            onSubmit={(event) => {
              event.preventDefault();
              if (!busy && !errors.blocked) void save();
            }}
          >
            <Field label={t("settings.download.name")} className="tw:col-span-2 tw:phone:col-span-1">
              <Input aria-label={t("settings.download.name")} maxLength={QB_NAME_MAX} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </Field>
            <Field
              className="tw:col-span-2 tw:phone:col-span-1"
              label={t("settings.download.address")}
              hint={errors.address && <span className="settings-connections-error">{errors.address}</span>}
            >
              <Input aria-label={t("settings.download.address")} value={draft.base_url} placeholder="http://qbittorrent.local:8080" onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} />
            </Field>
            {draft.credential_configured && (
              <SecretField
                className="tw:col-span-2 tw:phone:col-span-1"
                editing
                allowRemove={false}
                mode={draft.secret_mode}
                onModeChange={(mode) => setDraft({ ...draft, secret_mode: mode })}
                subject={t("settings.download.password")}
                replaceHint={t("settings.download.replaceHint")}
              />
            )}
            <Field label={t("settings.download.username")} hint={errors.username && <span className="settings-connections-error">{errors.username}</span>}>
              <Input aria-label={t("settings.download.username")} maxLength={QB_USERNAME_MAX} value={draft.username} onChange={(e) => setDraft({ ...draft, username: e.target.value })} />
            </Field>
            {(!draft.credential_configured || draft.secret_mode === "replace") && (
              <Field label={t("settings.download.password")}>
                <Input type="password" aria-label={t("settings.download.password")} maxLength={QB_SECRET_MAX} value={draft.password} onChange={(e) => setDraft({ ...draft, password: e.target.value })} />
              </Field>
            )}
            <Field className="tw:col-span-2 tw:phone:col-span-1" label={t("settings.download.proxyLabel")}>
              <Select label={t("settings.download.proxyLabel")} value={draft.route_id} options={[
                  { value: "", label: t("settings.download.proxyDirect") },
                  ...(routes.data ?? []).map((route) => ({ value: route.id, label: route.name })),
                ]} onChange={(routeID) => setDraft({ ...draft, route_id: routeID })} />
            </Field>
            <Field label={t("settings.download.downloadDir")} hint={errors.downloadDir ? <span className="settings-connections-error">{errors.downloadDir}</span> : t("settings.download.downloadDirHint")}>
              <Input aria-label={t("settings.download.downloadDir")} maxLength={QB_PATH_MAX} value={draft.download_root} onChange={(e) => setDraft({ ...draft, download_root: e.target.value })} />
            </Field>
            <Field
              label={t("settings.download.localDir")}
              hint={
                errors.localDir ? (
                  <span className="settings-connections-error">{errors.localDir}</span>
                ) : (
                  <>
                    {t("settings.download.localDirHint")}
                    <HelpTip label={t("settings.download.localDirHelpLabel")}>{t("settings.download.localDirHelp")}</HelpTip>
                  </>
                )
              }
            >
              <Input aria-label={t("settings.download.localDir")} maxLength={QB_PATH_MAX} value={draft.downloader_root} placeholder="/user/downloads" onChange={(e) => setDraft({ ...draft, downloader_root: e.target.value })} />
            </Field>
            <Field className="tw:col-span-2 tw:phone:col-span-1" label={t("settings.download.category")} hint={t("settings.download.categoryHint")}>
              <Input aria-label={t("settings.download.category")} maxLength={QB_CATEGORY_MAX} value={draft.category} onChange={(e) => setDraft({ ...draft, category: e.target.value })} />
            </Field>
            <Field className="tw:col-span-2 tw:phone:col-span-1" label={t("settings.download.tags")} hint={t("settings.download.tagsHint")}>
              <TagInput
                value={draft.user_tags}
                onChange={(next) => setDraft({ ...draft, user_tags: next })}
                placeholder={t("settings.download.tagsPlaceholder")}
                ariaLabel={t("settings.download.tagsAria")}
                validateItem={(tag) => {
                  if (tag.toLowerCase().startsWith("bansoko-")) return t("settings.download.tagReserved");
                  if (/[,\r\n]/.test(tag)) return t("settings.download.tagInvalid");
                  if (tag.length > QB_TAG_MAX) return t("settings.download.tooLong", { max: QB_TAG_MAX });
                  return null;
                }}
              />
            </Field>
            <Checkbox className="tw:col-span-2 tw:phone:col-span-1" checked={draft.enabled} onCheckedChange={(checked) => setDraft({ ...draft, enabled: checked })}>
              {t("settings.download.enable")}
            </Checkbox>
          </form>
        </SettingsEditDialog>
      )}
    </section>
  );
}

function Field({
  label,
  hint,
  children,
  className,
}: {
  label: React.ReactNode;
  hint?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <label className={`settings-shared-field${className ? ` ${className}` : ""} text-caption`}>
      <span>{label}</span>
      {children}
      {hint && <span className="settings-shared-field-hint text-caption">{hint}</span>}
    </label>
  );
}
