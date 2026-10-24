import { Alert, Button, Checkbox, Input, PasswordInput, Popconfirm, Popover, Select, SpinnerIcon, StatePill, Chip, Textarea, StateBlock } from "../../ui/base";
import { useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { valuesEqual } from "../../lib/draft";
import { notificationTriggerKindLabel } from "../../lib/labels";
import { configKeys, templateCatalogKeys } from "../../lib/queryKeys";
import { useBoundedRefresh } from "@/lib/events/useBoundedRefresh";
import { Bell, FlaskConical, History, KeyRound, Pencil, Plus, Send, Trash2, Webhook } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  ApiError,
  createPluginTarget,
  deletePluginTarget,
  getNotificationTemplateCatalog,
  getPluginDelivery,
  isVersionConflict,
  newUUID,
  listNotificationTriggerKinds,
  listPluginTargets,
  previewNotificationTemplate,
  testPluginTarget,
  toUserMessage,
  updatePluginTarget,
  type ErrorDetail,
  type NotificationCatalogVersion,
  type NotificationTemplatePreview,
  type NotificationTriggerKind,
  type PluginAdapterConfig,
  type PluginAdapterSecretValue,
  type PluginDeliveryDetail,
  type PluginTargetConfig,
  type PluginTargetCreate,
  type PluginTargetUpdate,
} from "../../lib/api/client";
import { SecretField, type SecretMode } from "../../ui";
import { DeliveryHistoryDialog } from "./DeliveryHistoryDialog";
import { deliveryStateLabel } from "./DeliveryDetailDialog";
import { SettingsEditDialog } from "./SettingsEditDialog";
import { getLocale, t } from "../../lib/i18n";
import { FactCatalog } from "../templateFacts/FactCatalog";

// Notification channels: the target list and the create/edit dialogs for the telegram / feishu /
//   webhook plugin targets. Secret-typed fields are never read back — editing expresses them as the
//   keep / replace / remove three states, and saving goes through Idempotency-Key / If-Match for
//   idempotency and optimistic locking. A test delivery is an async work item: after acceptance the
//   delivery detail is polled until the final result.

// The closed trigger-event registry is no longer hardcoded in the frontend: the editable events,
//   checkboxes and test events all come from /config/notifications/trigger-kinds (the backend's
//   plugin-target registry). NO_TRIGGER_KINDS is only a placeholder reference for when the query
//   returns nothing; rendering never expands from it.
const NO_TRIGGER_KINDS: NotificationTriggerKind[] = [];

// The events a new channel enables by default. These are product defaults, not a registry copy: the
//   registry comes from the backend and may grow, so this picks only the subset worth enabling by
//   default, while the actually enabled list and its order come from the returned registry (see
//   defaultTriggerKinds).
const DEFAULT_TRIGGER_KINDS: ReadonlySet<NotificationTriggerKind> = new Set([
  "download_submitted",
  "download_failed",
  "file_operation_completed",
  "unknown_files_imported",
]);

function defaultTriggerKinds(kinds: NotificationTriggerKind[]): NotificationTriggerKind[] {
  return kinds.filter((kind) => DEFAULT_TRIGGER_KINDS.has(kind));
}

const WEBHOOK_SIGNING_SECRET_MIN = 16;

const adapterLabel = (): Record<PluginTargetConfig["adapter_kind"], string> => ({
  telegram: t("settings.notifications.adapter.telegram"),
  feishu: t("settings.notifications.adapter.feishu"),
  webhook: t("settings.notifications.adapter.webhook"),
});

const adapterIcon: Record<PluginTargetConfig["adapter_kind"], React.ComponentType<{ className?: string }>> = {
  telegram: Send,
  feishu: Bell,
  webhook: Webhook,
};

// The fixed version number agreed with the backend's validateTargetCommon / validateTemplateSet.
const CONFIG_SCHEMA_VERSION = 1;
const TEMPLATE_LANGUAGE_VERSION = "bansoko-template-1";
const TEMPLATE_CATALOG_VERSION: NotificationCatalogVersion = "notification-catalog-v1";
const TERMINAL_DELIVERY_STATES = new Set(["delivered", "failed"]);

// The secret input stays in the form during edit keep/remove — its placeholder guides the user to
//   fill it only when replacing with a new value, guarding against accidentally wiping stored
//   credentials.
const secretInputPlaceholder = (): Record<PluginTargetConfig["adapter_kind"], string> => ({
  telegram: t("settings.notifications.field.botTokenReplace"),
  feishu: t("settings.notifications.field.webhookUrlReplace"),
  webhook: t("settings.notifications.field.endpointUrlReplace"),
});

type TemplateMode = "system_default" | "custom";

type EventTemplateDraft = {
  mode: TemplateMode;
  title_template: string;
  body_template: string;
  preview?: NotificationTemplatePreview;
  previewing?: boolean;
};

type NotificationDraft = {
  id?: string;
  version: number;
  name: string;
  adapter_kind: PluginTargetConfig["adapter_kind"];
  enabled: boolean;
  trigger_kinds: NotificationTriggerKind[];
  // telegram
  chat_id: string;
  bot_token: string;
  // feishu
  webhook_url: string;
  signing_secret: string;
  // webhook
  endpoint_url: string;
  auth_kind: "none" | "bearer" | "header";
  auth_header_name: string;
  auth_value: string;
  secret_mode: SecretMode;
  catalog_version: NotificationCatalogVersion;
  // One editing state per registry trigger event, built in the order /config/notifications/trigger-kinds
//   returns; on save everything is written back explicitly (the backend accepts the full registry's
//   events).
  events: Record<NotificationTriggerKind, EventTemplateDraft>;
};

const secretFieldsLabel = (): Record<PluginTargetConfig["adapter_kind"], string> => ({
  telegram: t("settings.notifications.secretSubject.telegram"),
  feishu: t("settings.notifications.secretSubject.feishu"),
  webhook: t("settings.notifications.secretSubject.webhook"),
});

// System default templates are no longer hardcoded in the frontend: the placeholder draft carries
//   only the shape, while default copy and available variables come from the backend's
//   getNotificationTemplateCatalog.
function defaultEventTemplate(): EventTemplateDraft {
  return { mode: "system_default", title_template: "", body_template: "" };
}

// The events record is built item by item from the returned registry; the trigger events are closed
//   and share their source with the generated union type, so every returned key appears in the
//   record.
function emptyEvents(kinds: NotificationTriggerKind[]): Record<NotificationTriggerKind, EventTemplateDraft> {
  return Object.fromEntries(kinds.map((kind) => [kind, defaultEventTemplate()])) as Record<
    NotificationTriggerKind,
    EventTemplateDraft
  >;
}

function emptyDraft(kinds: NotificationTriggerKind[]): NotificationDraft {
  return {
    version: 0,
    name: "",
    adapter_kind: "telegram",
    enabled: true,
    trigger_kinds: defaultTriggerKinds(kinds),
    chat_id: "",
    bot_token: "",
    webhook_url: "",
    signing_secret: "",
    endpoint_url: "",
    auth_kind: "none",
    auth_header_name: "",
    auth_value: "",
    secret_mode: "keep",
    catalog_version: TEMPLATE_CATALOG_VERSION,
    events: emptyEvents(kinds),
  };
}

function draftFromTarget(target: PluginTargetConfig, kinds: NotificationTriggerKind[]): NotificationDraft {
  const base = emptyDraft(kinds);
  base.id = target.id;
  base.version = target.version;
  base.name = target.name;
  base.adapter_kind = target.adapter_kind;
  base.enabled = target.enabled;
  base.trigger_kinds = [...target.trigger_kinds];
  base.secret_mode = target.has_secret ? "keep" : "replace";
  base.catalog_version = target.template_set.catalog_version;
  // A target whose stored config is not provably canonical reports
  // config_status=invalid with a null config, and the editor starts from an
  // empty draft so saving re-establishes a canonical document. The generated
  // nullable wrapper hides the union discriminator, so re-assert the contract
  // union that client.ts exports.
  const config = target.config as PluginAdapterConfig | null;
  if (config && config.adapter_kind === "telegram") {
    base.chat_id = config.chat_id;
  } else if (config && config.adapter_kind === "webhook") {
    base.auth_kind = config.auth_kind;
    base.auth_header_name = config.auth_header_name ?? "";
  }
  // A stored target may have written only part of the events in an older version; unwritten events
//   enter editing under the default template and are written back explicitly on save together with
//   the rest.
  for (const kind of kinds) {
    const event = target.template_set.events[kind];
    base.events[kind] =
      event && event.mode === "custom"
        ? { mode: "custom", title_template: event.title_template, body_template: event.body_template }
        : defaultEventTemplate();
  }
  return base;
}

function buildAdapterConfig(draft: NotificationDraft): PluginAdapterConfig {
  switch (draft.adapter_kind) {
    case "telegram":
      return { adapter_kind: "telegram", chat_id: draft.chat_id.trim() };
    case "feishu":
      return { adapter_kind: "feishu" };
    case "webhook":
      return {
        adapter_kind: "webhook",
        auth_kind: draft.auth_kind,
        auth_header_name:
          draft.auth_kind === "header" && draft.auth_header_name.trim() !== ""
            ? draft.auth_header_name.trim()
            : null,
      };
  }
}

function buildSecretValue(draft: NotificationDraft): PluginAdapterSecretValue {
  switch (draft.adapter_kind) {
    case "telegram":
      return { adapter_kind: "telegram", bot_token: draft.bot_token };
    case "feishu":
      return {
        adapter_kind: "feishu",
        webhook_url: draft.webhook_url.trim(),
        signing_secret: draft.signing_secret === "" ? undefined : draft.signing_secret,
      };
    case "webhook":
      return {
        adapter_kind: "webhook",
        endpoint_url: draft.endpoint_url.trim(),
        auth_value: draft.auth_value === "" ? undefined : draft.auth_value,
        signing_secret: draft.signing_secret === "" ? undefined : draft.signing_secret,
      };
  }
}

// Required-field validation for the adapters' non-secret fields: a telegram chat_id is the delivery destination, and empty means the backend cannot deliver.
function adapterConfigIsMissing(draft: NotificationDraft): boolean {
  switch (draft.adapter_kind) {
    case "telegram":
      return draft.chat_id.trim() === "";
    default:
      return false;
  }
}

/** The webhook signing key is optional; when filled, at least 16 characters. */
function webhookSigningSecretTooShort(draft: NotificationDraft): boolean {
  return (
    draft.adapter_kind === "webhook" &&
    draft.signing_secret !== "" &&
    draft.signing_secret.length < WEBHOOK_SIGNING_SECRET_MIN
  );
}

/** A secret is required on create; on edit only replace carries a new value. */
function secretIsMissing(draft: NotificationDraft): boolean {
  switch (draft.adapter_kind) {
    case "telegram":
      return draft.bot_token.trim() === "";
    case "feishu":
      return draft.webhook_url.trim() === "";
    case "webhook":
      return (
        draft.endpoint_url.trim() === "" ||
        (draft.auth_kind === "bearer" && draft.auth_value.trim() === "")
      );
  }
}

// The server compiles the templates on save; TEMPLATE_INVALID's details carry the failing event, field and position.
function templateSaveErrors(cause: unknown): ErrorDetail[] {
  if (!(cause instanceof ApiError) || cause.code !== "TEMPLATE_INVALID") return [];
  return (cause.details ?? []).filter((item) => item.field?.startsWith("template_set.events."));
}

function templateSaveErrorText(item: ErrorDetail): string {
  const [, , trigger = "", part = ""] = (item.field ?? "").split(".");
  const fieldLabel =
    part === "title_template"
      ? t("settings.notifications.template.titleField")
      : part === "body_template"
        ? t("settings.notifications.template.bodyField")
        : "";
  const where = [notificationTriggerKindLabel(trigger as NotificationTriggerKind), fieldLabel].filter(Boolean).join(" · ");
  const position = item.start !== undefined ? t("settings.notifications.template.errorAt", { n: item.start }) : "";
  return t("settings.notifications.template.errorLine", { where, position, message: item.message });
}

/**
 * webhook 渠道保存后将不带签名：新建或替换时未填签名密钥，或编辑时保留的
 * 密钥本身没有签名密钥。
 */
function signingSecretMissing(draft: NotificationDraft, targets: PluginTargetConfig[] | undefined): boolean {
  if (draft.adapter_kind !== "webhook") return false;
  if (!draft.id || draft.secret_mode === "replace") return draft.signing_secret === "";
  if (draft.secret_mode === "remove") return false;
  return targets?.find((item) => item.id === draft.id)?.has_signing_secret === false;
}

export function SettingsNotifications() {
  useBoundedRefresh([configKeys.pluginTargets]);
  const queryClient = useQueryClient();
  const targets = useQuery({ queryKey: configKeys.pluginTargets, queryFn: listPluginTargets });
  // The trigger-event registry is basically stable and long-cached once fetched; on failure the UI
//   enters an explicit loading/error state and never silently renders the editor over an empty
//   list.
  const triggerVocabulary = useQuery({
    queryKey: configKeys.notificationTriggerKinds,
    queryFn: listNotificationTriggerKinds,
    staleTime: Infinity,
  });
  const triggerKinds = triggerVocabulary.data?.trigger_kinds ?? NO_TRIGGER_KINDS;
  const [draft, setDraft] = useState<NotificationDraft>();
  const [savedDraft, setSavedDraft] = useState<NotificationDraft>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [templateErrors, setTemplateErrors] = useState<ErrorDetail[]>([]);
  const [conflict, setConflict] = useState<number>();
  const [testing, setTesting] = useState<string>();
  const [deleting, setDeleting] = useState<string>();
  const [pendingDelete, setPendingDelete] = useState<PluginTargetConfig>();
  const [showHistory, setShowHistory] = useState(false);
  const [testResults, setTestResults] = useState<
    Record<string, { state: "delivered" | "failed" | "accepted"; text?: string }>
  >({});

  async function save() {
    if (!draft) return;
    setBusy(true);
    setError(undefined);
    setTemplateErrors([]);
    setConflict(undefined);
    try {
      const templateSet = {
        language_version: TEMPLATE_LANGUAGE_VERSION,
        catalog_version: draft.catalog_version,
        locale: getLocale(),
        events: Object.fromEntries(
          Object.entries(draft.events).map(([kind, event]) => [
            kind,
            event.mode === "custom"
              ? {
                  mode: "custom",
                  title_template: event.title_template,
                  body_template: event.body_template,
                }
              : { mode: "system_default" },
          ]),
        ),
      } as PluginTargetCreate["template_set"];

      if (draft.id) {
        const input: PluginTargetUpdate = {
          expected_version: draft.version,
          name: draft.name.trim(),
          plugin_kind: "notification",
          adapter_kind: draft.adapter_kind,
          enabled: draft.enabled,
          trigger_kinds: draft.trigger_kinds,
          config_schema_version: CONFIG_SCHEMA_VERSION,
          config: buildAdapterConfig(draft),
          template_set: templateSet,
          secret:
            draft.secret_mode === "replace"
              ? { mode: "replace", value: buildSecretValue(draft) }
              : draft.secret_mode === "remove"
                ? { mode: "remove" }
                : { mode: "keep" },
        };
        await updatePluginTarget(draft.id, draft.version, input);
      } else {
        const input: PluginTargetCreate = {
          name: draft.name.trim(),
          plugin_kind: "notification",
          adapter_kind: draft.adapter_kind,
          enabled: draft.enabled,
          trigger_kinds: draft.trigger_kinds,
          config_schema_version: CONFIG_SCHEMA_VERSION,
          config: buildAdapterConfig(draft),
          template_set: templateSet,
          secret: { mode: "set", value: buildSecretValue(draft) },
        };
        await createPluginTarget(input, newUUID());
      }
      toast.success(draft.id ? t("settings.notifications.saved") : t("settings.notifications.created"));
      setDraft(undefined);
      setSavedDraft(undefined);
      await queryClient.invalidateQueries({ queryKey: configKeys.pluginTargets });
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setTemplateErrors(templateSaveErrors(cause));
      setError(toUserMessage(cause, t("settings.notifications.saveFailed")));
    } finally {
      setBusy(false);
    }
  }

  // On a 409 refresh: load the server's latest targets, discard the stale local draft, and the user re-edits.
  async function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    const fresh = await queryClient.fetchQuery({ queryKey: configKeys.pluginTargets, queryFn: listPluginTargets });
    setDraft((current) => {
      if (!current?.id) return current;
      const target = fresh.targets.find((item) => item.id === current.id);
      if (!target) {
        setSavedDraft(undefined);
        return undefined;
      }
      const latest = draftFromTarget(target, triggerKinds);
      setSavedDraft(latest);
      return latest;
    });
  }

  async function confirmDelete() {
    if (!pendingDelete) return;
    setDeleting(pendingDelete.id);
    try {
      await deletePluginTarget(pendingDelete.id);
      toast.success(t("settings.notifications.deleted"));
      await queryClient.invalidateQueries({ queryKey: configKeys.pluginTargets });
    } catch (cause) {
      toast.error(toUserMessage(cause, t("settings.notifications.deleteFailed")));
    } finally {
      setDeleting(undefined);
      setPendingDelete(undefined);
    }
  }

  async function test(item: PluginTargetConfig, triggerKind: NotificationTriggerKind) {
    setTesting(item.id);
    try {
      const accepted = await testPluginTarget(
        item.id,
        { expected_version: item.version, trigger_kind: triggerKind },
        newUUID(),
      );
      const detail = await pollDelivery(accepted.delivery_id);
      const delivery = detail.delivery;
      setTestResults((prev) => ({
        ...prev,
        [item.id]: {
          state: delivery.state === "delivered" ? "delivered" : delivery.state === "failed" ? "failed" : "accepted",
          text: delivery.last_error ?? undefined,
        },
      }));
      if (delivery.state === "delivered") {
        toast.success(t("settings.notifications.testDelivered"));
      } else if (delivery.state === "failed") {
        toast.error(delivery.last_error ?? t("settings.notifications.testFailed"));
      } else {
        toast.info(t("settings.notifications.testAccepted"));
      }
    } catch (cause) {
      setTestResults((prev) => ({
        ...prev,
        [item.id]: { state: "failed", text: toUserMessage(cause, t("settings.notifications.testFailedShort")) },
      }));
    } finally {
      setTesting(undefined);
    }
  }

  return (
    <section className="settings-notifications">
      <div className="settings-notifications-header">
        <h2 className="settings-notifications-title text-title">{t("settings.notifications.title")}</h2>
        <Button variant="primary" size="sm" disabled={!triggerVocabulary.data} onClick={() => {
            setError(undefined);
            setConflict(undefined);
            const next = emptyDraft(triggerKinds);
            setDraft(next);
            setSavedDraft(next);
          }}>
          <Plus size={14} />
          {t("settings.notifications.new")}
        </Button>
      </div>

      {targets.isLoading && (
        <StateBlock kind="busy" title={t("settings.notifications.loading")} />
      )}
      {targets.error && <Alert tone="error" title={toUserMessage(targets.error, t("settings.notifications.loadFailed"))} />}
      {triggerVocabulary.isError && !draft && (
        <Alert
          tone="error"
          title={t("settings.notifications.triggerKindsFailed")}
          description={<Button size="sm" onClick={() => void triggerVocabulary.refetch()}>{t("ui.errorBoundary.retry")}</Button>}
        />
      )}
      {targets.data && targets.data.targets.length === 0 && !draft && (
        <StateBlock kind="empty" title={t("settings.notifications.empty")} />
      )}

      <ul className="settings-notifications-list">
        {(targets.data?.targets ?? []).map((item) => {
          const AdapterIcon = adapterIcon[item.adapter_kind];
          const result = testResults[item.id];
          return (
            <li key={item.id} className="settings-notifications-item">
              <div className="settings-notifications-row">
                <AdapterIcon className="settings-notifications-icon" />
                <span className="settings-notifications-title-text text-body">{item.name}</span>
                <Chip>{adapterLabel()[item.adapter_kind]}</Chip>
                <StatePill tone={item.enabled ? "online" : "muted"}>{item.enabled ? t("common.enabled") : t("common.disabled")}</StatePill>
                <StatePill tone={item.has_secret ? "online" : "warning"} icon={KeyRound}>
                  {item.has_secret ? t("common.credentialSet") : t("common.credentialUnset")}
                </StatePill>
                {item.adapter_kind === "webhook" && item.has_secret && item.has_signing_secret === false && (
                  <StatePill tone="warning" title={t("settings.notifications.unsignedHint")}>
                    {t("settings.notifications.unsigned")}
                  </StatePill>
                )}
                {result && (
                  <StatePill tone={result.state === "delivered" ? "online" : result.state === "failed" ? "offline" : "warning"}>
                    {t("settings.notifications.testResult", { state: deliveryStateLabel(result.state === "accepted" ? "pending" : result.state) })}
                  </StatePill>
                )}
                <span className="settings-notifications-spacer" />
                <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-1">
                  <TestButton
                    triggerKinds={triggerKinds}
                    disabled={testing === item.id || !triggerVocabulary.data}
                    label={testing === item.id ? t("settings.notifications.testing") : t("settings.notifications.test")}
                    onTest={(kind) => void test(item, kind)}
                  />
                  <Button variant="ghost" size="sm" onClick={() => setShowHistory(true)}>
                    <History size={14} />
                    {t("settings.delivery.history")}
                  </Button>
                  <Button variant="ghost" size="sm" disabled={!triggerVocabulary.data} onClick={() => {
                      setError(undefined);
                      setConflict(undefined);
                      const next = draftFromTarget(item, triggerKinds);
                      setDraft(next);
                      setSavedDraft(next);
                    }}>
                    <Pencil size={14} />
                    {t("settings.notifications.edit")}
                  </Button>
                  <Popconfirm
                    title={t("settings.notifications.deleteTitle", { name: item.name })}
                    description={t("settings.notifications.deleteHint")}
                    confirmLabel={t("settings.notifications.delete")}
                    danger
                    confirmLoading={deleting === item.id}
                    open={pendingDelete?.id === item.id}
                    onOpenChange={(open) => setPendingDelete(open ? item : undefined)}
                    onConfirm={() => void confirmDelete()}
                    trigger={
                      <Button variant="dangerGhost" size="sm" disabled={deleting === item.id}>
                        <Trash2 size={14} />
                        {deleting === item.id ? t("settings.notifications.deleting") : t("settings.notifications.delete")}
                      </Button>
                    }
                  />
                </div>
              </div>
              <div className="settings-notifications-events">
                {item.trigger_kinds.length === 0 && (
                  <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.noTrigger")}</span>
                )}
                {item.trigger_kinds.map((kind) => (
                  <Chip key={kind}>{notificationTriggerKindLabel(kind)}</Chip>
                ))}
                {result?.text && (
                  <span className="settings-notifications-muted-break text-caption tw:text-muted-foreground">· {result.text}</span>
                )}
              </div>
            </li>
          );
        })}
      </ul>

      {draft && (
        <SettingsEditDialog
          open
          onOpenChange={(o) => {
            if (!o) {
              setDraft(undefined);
              setSavedDraft(undefined);
            }
          }}
          title={draft.id ? t("settings.notifications.editTitle") : t("settings.notifications.newTitle")}
          description={t("settings.notifications.editDescription")}
          dirty={() => !!draft && !valuesEqual(draft, savedDraft)}
          footer={(requestClose) => (
            <>
              <Button variant="ghost" onClick={requestClose}>
                {t("settings.notifications.cancel")}
              </Button>
              <Button variant="primary" aria-label={t("settings.notifications.save")} disabled={
                  busy ||
                  draft.name.trim() === "" ||
                  adapterConfigIsMissing(draft) ||
                  webhookSigningSecretTooShort(draft) ||
                  (!draft.id && secretIsMissing(draft)) ||
                  (draft.id !== undefined && draft.secret_mode === "replace" && secretIsMissing(draft))
                } onClick={() => void save()}>
                {busy ? t("settings.notifications.saving") : t("settings.notifications.save")}
              </Button>
            </>
          )}
        >
          {conflict !== undefined ? (
            <Alert tone="error" title={t("common.versionConflict")} description={
                <div className="tw:grid tw:gap-2">
                  {error && <span>{error}</span>}
                  <span>{t("common.conflictHint")}</span>
                  <Button size="sm" onClick={() => void refreshAfterConflict()}>{t("common.refreshLatest")}</Button>
                </div>
              } />
          ) : (
            error && (
              <Alert tone="error" title={error} description={
                templateErrors.length > 0 ? (
                  <ul className="tw:grid tw:gap-1">
                    {templateErrors.map((item, index) => (
                      <li key={index} className="text-caption">{templateSaveErrorText(item)}</li>
                    ))}
                  </ul>
                ) : undefined
              } />
            )
          )}
          <div className="settings-notifications-responsive">
            <Field label={t("settings.notifications.field.type")}>
              <Select label={t("settings.notifications.field.type")} value={draft.adapter_kind} disabled={!!draft.id} options={[
                  { value: "telegram", label: adapterLabel().telegram },
                  { value: "feishu", label: adapterLabel().feishu },
                  { value: "webhook", label: adapterLabel().webhook },
                ]} onChange={(value) => setDraft({ ...draft, adapter_kind: value as PluginTargetConfig["adapter_kind"] })} />
            </Field>
            <Field label={t("settings.notifications.field.name")}>
              <Input aria-label={t("settings.notifications.field.name")} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </Field>
          </div>

          {draft.adapter_kind === "telegram" && (
            <div className="settings-notifications-field-gap settings-notifications-responsive">
              <Field label={t("settings.notifications.field.chatId")}>
                <Input aria-label={t("settings.notifications.field.chatId")} value={draft.chat_id} placeholder={t("settings.notifications.field.chatIdPlaceholder")} onChange={(e) => setDraft({ ...draft, chat_id: e.target.value })} />
              </Field>
              <Field label={t("settings.notifications.field.botToken")}>
                <PasswordInput aria-label={t("settings.notifications.field.botToken")} value={draft.bot_token} placeholder={draft.id ? secretInputPlaceholder().telegram : t("settings.notifications.field.botTokenPlaceholder")} onChange={(e) => setDraft({ ...draft, bot_token: e.target.value })} />
              </Field>
            </div>
          )}

          {draft.adapter_kind === "feishu" && (
            <div className="settings-notifications-field-gap settings-notifications-responsive">
              <Field label={t("settings.notifications.field.webhookUrl")} className="settings-notifications-field-wide">
                <PasswordInput aria-label={t("settings.notifications.field.webhookUrl")} value={draft.webhook_url} placeholder={draft.id ? secretInputPlaceholder().feishu : t("settings.notifications.field.webhookUrlPlaceholder")} onChange={(e) => setDraft({ ...draft, webhook_url: e.target.value })} />
              </Field>
              <Field label={t("settings.notifications.field.signingSecret")}>
                <PasswordInput aria-label={t("settings.notifications.field.signingSecret")} value={draft.signing_secret} onChange={(e) => setDraft({ ...draft, signing_secret: e.target.value })} />
              </Field>
            </div>
          )}

          {draft.adapter_kind === "webhook" && (
            <div className="settings-notifications-field-gap settings-notifications-responsive">
              <Field label={t("settings.notifications.field.endpointUrl")} className="settings-notifications-field-wide">
                <PasswordInput aria-label={t("settings.notifications.field.endpointUrl")} value={draft.endpoint_url} placeholder={draft.id ? secretInputPlaceholder().webhook : t("settings.notifications.field.endpointUrlPlaceholder")} onChange={(e) => setDraft({ ...draft, endpoint_url: e.target.value })} />
              </Field>
              <Field label={t("settings.notifications.field.authKind")}>
                <Select label={t("settings.notifications.field.authKind")} value={draft.auth_kind} options={[
                    { value: "none", label: t("settings.notifications.field.authNone") },
                    { value: "bearer", label: t("settings.notifications.field.authBearer") },
                    { value: "header", label: t("settings.notifications.field.authHeader") },
                  ]} onChange={(value) => setDraft({ ...draft, auth_kind: value as "none" | "bearer" | "header" })} />
              </Field>
              {draft.auth_kind === "header" && (
                <Field label={t("settings.notifications.field.headerName")}>
                  <Input aria-label={t("settings.notifications.field.headerName")} value={draft.auth_header_name} placeholder="X-Api-Key" onChange={(e) => setDraft({ ...draft, auth_header_name: e.target.value })} />
                </Field>
              )}
              {draft.auth_kind !== "none" && (
                <Field label={draft.auth_kind === "bearer" ? t("settings.notifications.field.bearerValue") : t("settings.notifications.field.headerValue")}>
                  <PasswordInput aria-label={draft.auth_kind === "bearer" ? t("settings.notifications.field.bearerValue") : t("settings.notifications.field.headerValue")} value={draft.auth_value} onChange={(e) => setDraft({ ...draft, auth_value: e.target.value })} />
                </Field>
              )}
              <Field label={t("settings.notifications.field.webhookSigningSecret")}>
                <PasswordInput aria-label={t("settings.notifications.field.webhookSigningSecret")} value={draft.signing_secret} placeholder={t("settings.notifications.field.webhookSigningSecretPlaceholder")} onChange={(e) => setDraft({ ...draft, signing_secret: e.target.value })} />
              </Field>
              {webhookSigningSecretTooShort(draft) && (
                <span role="alert" className="settings-notifications-field-wide text-caption tw:text-destructive">
                  {t("settings.notifications.field.webhookSigningSecretTooShort")}
                </span>
              )}
              {signingSecretMissing(draft, targets.data?.targets) && (
                <Alert className="settings-notifications-field-wide" tone="warning" title={t("settings.notifications.unsignedHint")} />
              )}
            </div>
          )}

          {triggerVocabulary.isPending && (
            <StateBlock kind="busy" title={t("settings.notifications.triggerKindsLoading")} />
          )}
          {triggerVocabulary.isError && (
            <Alert
              className="settings-notifications-section-gap"
              tone="error"
              title={t("settings.notifications.triggerKindsFailed")}
              description={<Button size="sm" onClick={() => void triggerVocabulary.refetch()}>{t("ui.errorBoundary.retry")}</Button>}
            />
          )}
          {triggerVocabulary.data && (
            <div className="settings-notifications-field-gap settings-notifications-responsive">
              <Field label={t("settings.notifications.field.triggers")} className="settings-notifications-field-wide">
                <div className="settings-notifications-events-input">
                  {triggerKinds.map((kind) => (
                    <Checkbox key={kind} checked={draft.trigger_kinds.includes(kind)} onCheckedChange={(checked) => {
                        const next = checked
                          ? [...draft.trigger_kinds.filter((k) => k !== kind), kind]
                          : draft.trigger_kinds.filter((k) => k !== kind);
                        next.sort((a, b) => triggerKinds.indexOf(a) - triggerKinds.indexOf(b));
                        setDraft({ ...draft, trigger_kinds: next });
                      }}>
                      {notificationTriggerKindLabel(kind)}
                    </Checkbox>
                  ))}
                </div>
              </Field>
              <Checkbox checked={draft.enabled} onCheckedChange={(checked) => setDraft({ ...draft, enabled: checked })}>
                {t("settings.notifications.enable")}
              </Checkbox>
            </div>
          )}

          {draft.id && (
            <SecretField
              editing
              className="settings-notifications-section-gap"
              mode={draft.secret_mode}
              onModeChange={(secret_mode) => setDraft({ ...draft, secret_mode })}
              subject={secretFieldsLabel()[draft.adapter_kind]}
              removeHint={t("settings.notifications.removeSecretHint")}
            />
          )}

          {triggerVocabulary.data && (
            <div className="settings-notifications-section-gap settings-notifications-form">
              <span className="settings-notifications-template-label text-caption">{t("settings.notifications.template.title")}</span>
              {triggerKinds.map((kind) => (
                <EventTemplateEditor
                  key={kind}
                  kind={kind}
                  catalogVersion={draft.catalog_version}
                  draft={draft.events[kind]}
                  onChange={(event) => setDraft({ ...draft, events: { ...draft.events, [kind]: event } })}
                />
              ))}
              <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.template.defaultHint")}</span>
            </div>
          )}
        </SettingsEditDialog>
      )}

      {showHistory && <DeliveryHistoryDialog onClose={() => setShowHistory(false)} />}
    </section>
  );
}

function Field({
  label,
  children,
  className,
}: {
  label: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <label className={`settings-notifications-field${className ? ` ${className}` : ""} text-caption`}>
      <span>{label}</span>
      {children}
    </label>
  );
}

function TestButton({
  triggerKinds,
  disabled,
  label,
  onTest,
}: {
  triggerKinds: NotificationTriggerKind[];
  disabled: boolean;
  label: string;
  onTest: (kind: NotificationTriggerKind) => void;
}) {
  const [open, setOpen] = useState(false);
  const content = (
    <div className="settings-notifications-test-menu">
      <span className="settings-notifications-test-menu-label text-caption tw:text-muted-foreground">{t("settings.notifications.testPickEvent")}</span>
      {triggerKinds.map((kind) => (
        <Button variant="ghost" key={kind} className="settings-notifications-test-menu-action" onClick={() => {
            setOpen(false);
            onTest(kind);
          }}>
          {notificationTriggerKindLabel(kind)}
        </Button>
      ))}
    </div>
  );
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      side="bottom"
      align="end"
      trigger={
        <Button size="sm" disabled={disabled}>
          <FlaskConical size={14} />
          {label}
        </Button>
      }
    >
      {content}
    </Popover>
  );
}

function EventTemplateEditor({
  kind,
  catalogVersion,
  draft,
  onChange,
}: {
  kind: NotificationTriggerKind;
  catalogVersion: NotificationCatalogVersion;
  draft: EventTemplateDraft;
  onChange: (draft: EventTemplateDraft) => void;
}) {
  const [previewOpen, setPreviewOpen] = useState(false);
  const [error, setError] = useState<string>();
  const titleRef = useRef<HTMLInputElement>(null);
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  // The insertion point follows the last focused input, defaulting to the body.
  const lastFocused = useRef<"title" | "body">("body");
  const catalog = useQuery({
    queryKey: templateCatalogKeys.notification(kind, catalogVersion),
    queryFn: () => getNotificationTemplateCatalog(kind, catalogVersion),
    staleTime: Infinity,
  });
  const defaults = catalog.data?.default_template;

  function insertToken(token: string) {
    const field = lastFocused.current;
    const element = field === "title" ? titleRef.current : bodyRef.current;
    const current = field === "title" ? draft.title_template : draft.body_template;
    const start = element?.selectionStart ?? current.length;
    const end = element?.selectionEnd ?? current.length;
    const next = current.slice(0, start) + token + current.slice(end);
    onChange(field === "title" ? { ...draft, title_template: next } : { ...draft, body_template: next });
    if (!element) return;
    requestAnimationFrame(() => {
      element.focus();
      const caret = start + token.length;
      element.setSelectionRange(caret, caret);
    });
  }

  async function preview() {
    setPreviewOpen(true);
    setError(undefined);
    onChange({ ...draft, previewing: true, preview: undefined });
    try {
      const result = await previewNotificationTemplate({
        trigger_kind: kind,
        language_version: TEMPLATE_LANGUAGE_VERSION,
        catalog_version: catalogVersion,
        title_template: draft.title_template,
        body_template: draft.body_template,
      });
      onChange({ ...draft, previewing: false, preview: result });
    } catch (cause) {
      onChange({ ...draft, previewing: false, preview: undefined });
      setError(toUserMessage(cause, t("settings.notifications.template.previewFailed")));
    }
  }

  return (
    <div className="settings-notifications-template-card">
      <div className="settings-notifications-template-header">
        <span className="settings-notifications-title-text text-body">{notificationTriggerKindLabel(kind)}</span>
        <div className="tw:flex tw:items-center tw:gap-1">
          <Select label={t("settings.notifications.template.modeLabel", { event: notificationTriggerKindLabel(kind) })} value={draft.mode} options={[
              { value: "system_default", label: t("settings.notifications.template.modeDefault") },
              { value: "custom", label: t("settings.notifications.template.modeCustom") },
            ]} onChange={(mode) => {
              // Switching to custom starts from the default template rather than a blank page.
              if (mode === "custom" && defaults && draft.title_template === "" && draft.body_template === "") {
                onChange({ ...draft, mode, title_template: defaults.title_template, body_template: defaults.body_template });
              } else {
                onChange({ ...draft, mode: mode as TemplateMode });
              }
            }} />
          {draft.mode === "custom" && (
            <Button variant="ghost" size="sm" loading={draft.previewing} disabled={draft.previewing} onClick={() => void preview()}>
              {draft.previewing ? t("settings.notifications.template.previewing") : t("settings.notifications.template.preview")}
            </Button>
          )}
        </div>
      </div>

      {draft.mode === "custom" ? (
        <div className="settings-notifications-template-fields">
          <Field label={t("settings.notifications.template.titleField")}>
            <Input ref={titleRef} aria-label={t("settings.notifications.template.titleField")} value={draft.title_template} onFocus={() => { lastFocused.current = "title"; }} onChange={(e) => onChange({ ...draft, title_template: e.target.value })} />
          </Field>
          <Field label={t("settings.notifications.template.bodyField")}>
            <Textarea ref={bodyRef} aria-label={t("settings.notifications.template.bodyField")} rows={2} value={draft.body_template} onFocus={() => { lastFocused.current = "body"; }} onChange={(e) => onChange({ ...draft, body_template: e.target.value })} />
          </Field>
          {catalog.data ? (
            <details className="settings-notifications-template-catalog">
              <summary className="text-caption">
                {t("settings.notifications.template.variables", { n: catalog.data.variables.length })}
              </summary>
              <FactCatalog
                facts={catalog.data.variables.map((variable) => ({ ...variable, path: variable.name }))}
                token={(path) => `{{${path}}}`}
                onInsert={insertToken}
              />
              {/* Block-level layout is the plain div's job; the Typography.Text root carries no structural class. */}
              <div className="settings-notifications-template-functions">
                <span className="text-caption tw:text-muted-foreground">
                  {t("settings.notifications.template.functions", { names: catalog.data.functions.map((fn) => fn.name).join(" / ") })}
                </span>
              </div>
            </details>
          ) : (
            <span className="tw:text-muted-foreground">
              {catalog.isError ? t("settings.notifications.template.catalogFailed") : t("settings.notifications.template.catalogLoading")}
            </span>
          )}

          {previewOpen && (
            <div className="settings-notifications-template-preview">
              {error && <Alert tone="error" title={error} />}
              {draft.previewing && (
                <div role="status" className="settings-notifications-template-preview-status text-caption">
                  <SpinnerIcon />
                  {t("settings.notifications.template.previewing")}
                </div>
              )}
              {draft.preview && (
                <div className="settings-notifications-template-preview-content">
                  <StatePill tone={draft.preview.valid ? "online" : "offline"}>{draft.preview.valid ? t("settings.notifications.template.valid") : t("settings.notifications.template.invalid")}</StatePill>
                  <p className="settings-notifications-template-copy text-caption">
                    <span className="tw:text-muted-foreground">{t("settings.notifications.template.previewTitle")}</span>{draft.preview.title}
                  </p>
                  <p className="settings-notifications-template-copy text-caption">
                    <span className="tw:text-muted-foreground">{t("settings.notifications.template.previewBody")}</span>{draft.preview.body}
                  </p>
                  {draft.preview.errors.map((item, index) => (
                    <div key={index}>
                      <span className="text-caption tw:text-destructive">
                        {item.field ? `${item.field}: ` : ""}{item.message}
                      </span>
                    </div>
                  ))}
                  {draft.preview.warnings.map((item, index) => (
                    <div key={index}>
                      <span className="text-caption tw:text-warning">
                        {item.variable ? `${item.variable}: ` : ""}{item.message}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      ) : defaults ? (
        <div className="settings-notifications-template-defaults">
          <p className="settings-notifications-template-copy">
            {t("settings.notifications.template.defaultTitle")}<span>{defaults.title_template}</span>
          </p>
          <p className="settings-notifications-template-copy-pre">
            {t("settings.notifications.template.defaultBody")}<span>{defaults.body_template}</span>
          </p>
        </div>
      ) : (
        <div className="settings-notifications-template-fallback">
          <span className="tw:text-muted-foreground">
            {catalog.isError ? t("settings.notifications.template.defaultFailed") : t("settings.notifications.template.defaultLoading")}
          </span>
        </div>
      )}
    </div>
  );
}

// A test delivery is an async work item: the POST returns only accepted + delivery_id, then a short
//   poll on the delivery detail runs until a terminal state (delivered / failed) or timeout.
async function pollDelivery(deliveryID: string): Promise<PluginDeliveryDetail> {
  const deadline = Date.now() + 15_000;
  for (;;) {
    const detail = await getPluginDelivery(deliveryID);
    if (TERMINAL_DELIVERY_STATES.has(detail.delivery.state) || Date.now() >= deadline) {
      return detail;
    }
    await new Promise((resolve) => setTimeout(resolve, 1_200));
  }
}
