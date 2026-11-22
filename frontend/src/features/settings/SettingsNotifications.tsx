import {
  Alert,
  Button,
  Checkbox,
  Chip,
  DropdownMenu,
  Input,
  PasswordInput,
  Select,
  SpinnerIcon,
  StateBlock,
  StatePill,
  Textarea,
  type MenuEntry,
} from "../../ui/base";
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { valuesEqual } from "../../lib/draft";
import { notificationTriggerKindLabel } from "../../lib/labels";
import { configKeys, templateCatalogKeys } from "../../lib/queryKeys";
import { cn } from "../../lib/utils";
import { PHONE_MEDIA_QUERY, useMediaQuery } from "../../lib/useMediaQuery";
import { useBoundedRefresh } from "@/lib/events/useBoundedRefresh";
import {
  Bell,
  Check,
  ChevronLeft,
  ChevronRight,
  Ellipsis,
  FlaskConical,
  History,
  KeyRound,
  Pencil,
  Plus,
  RotateCcw,
  Send,
  ShieldCheck,
  Trash2,
  Webhook,
} from "lucide-react";
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
import { ConfirmDialog, SecretField, SegmentedControl, type SecretMode } from "../../ui";
import { DeliveryHistoryDialog } from "./DeliveryHistoryDialog";
import { deliveryStateLabel } from "@/lib/labels";
import { SettingsEditDialog } from "./SettingsEditDialog";
import { SettingsEventScripts } from "./SettingsEventScripts";
import { getLocale, t, tryT } from "../../lib/i18n";
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

type NotificationLocale = NonNullable<PluginTargetConfig["template_set"]["locale"]>;

// The per-event editing state. Preview output is deliberately NOT part of the draft: it is a
//   read-only rendering of the current text and must never move the dirty comparison.
type EventTemplateDraft = {
  mode: TemplateMode;
  title_template: string;
  body_template: string;
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
  // Language of the system default templates. An existing channel keeps the language its stored
  //   template set was accepted in; a new channel starts in the current UI language.
  locale: NotificationLocale;
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
    locale: getLocale(),
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
  base.locale = target.template_set.locale ?? "zh-CN";
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

function templateSaveErrorKind(item: ErrorDetail): NotificationTriggerKind | undefined {
  return (item.field ?? "").split(".")[2] as NotificationTriggerKind | undefined;
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

/** How many events of this draft (or saved target) use a custom template. */
function customTemplateCount(events: Record<string, { mode: string } | undefined>): number {
  return Object.values(events).filter((event) => event?.mode === "custom").length;
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
  const isPhone = useMediaQuery(PHONE_MEDIA_QUERY);
  const [draft, setDraft] = useState<NotificationDraft>();
  const [savedDraft, setSavedDraft] = useState<NotificationDraft>();
  // The edit dialog has two pages: the channel page (connection, credentials, trigger events) and
  //   the template page (per-event copy). On phones the template page drills in: the event list
  //   shows first and tapping an event opens its editor (mobileEditing).
  const [view, setView] = useState<"channel" | "template">("channel");
  const [tplKind, setTplKind] = useState<NotificationTriggerKind>();
  const [mobileEditing, setMobileEditing] = useState(false);
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

  function openDialog(next: NotificationDraft) {
    setError(undefined);
    setConflict(undefined);
    setTemplateErrors([]);
    setDraft(next);
    setSavedDraft(next);
    setView("channel");
    setMobileEditing(false);
    setTplKind(triggerKinds[0]);
  }

  function closeDialog() {
    setDraft(undefined);
    setSavedDraft(undefined);
  }

  // A template save error names its event; clicking it jumps straight to that event's editor.
  function jumpToTemplateError(item: ErrorDetail) {
    const kind = templateSaveErrorKind(item);
    setView("template");
    if (kind && triggerKinds.includes(kind)) setTplKind(kind);
    setMobileEditing(true);
  }

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
        locale: draft.locale,
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
      closeDialog();
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

  const saveDisabled =
    busy ||
    !draft ||
    draft.name.trim() === "" ||
    adapterConfigIsMissing(draft) ||
    webhookSigningSecretTooShort(draft) ||
    (!draft.id && secretIsMissing(draft)) ||
    (draft?.id !== undefined && draft.secret_mode === "replace" && secretIsMissing(draft));

  return (
    <section className="settings-notifications">
      <div className="settings-notifications-header">
        <h2 className="settings-notifications-title text-title">{t("settings.notifications.title")}</h2>
        <Button variant="primary" size="sm" disabled={!triggerVocabulary.data} onClick={() => openDialog(emptyDraft(triggerKinds))}>
          <Plus size={14} />
          {t("settings.notifications.new")}
        </Button>
      </div>

      {targets.isLoading && (
        <StateBlock kind="busy" skeleton="settings" title={t("settings.notifications.loading")} />
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
        {(targets.data?.targets ?? []).map((item) => (
          <TargetRow
            key={item.id}
            item={item}
            result={testResults[item.id]}
            testing={testing === item.id}
            deleting={deleting === item.id}
            editDisabled={!triggerVocabulary.data}
            onEdit={() => openDialog(draftFromTarget(item, triggerKinds))}
            onTest={(kind) => void test(item, kind)}
            onHistory={() => setShowHistory(true)}
            onDelete={() => setPendingDelete(item)}
          />
        ))}
      </ul>

      {draft && (
        <SettingsEditDialog
          open
          tier={view === "template" ? "L" : "M"}
          onOpenChange={(o) => {
            if (!o) closeDialog();
          }}
          title={view === "channel"
            ? (draft.id ? t("settings.notifications.editTitle") : t("settings.notifications.newTitle"))
            : t("settings.notifications.template.pageTitle")}
          description={view === "channel"
            ? t("settings.notifications.editDescription")
            : t("settings.notifications.template.pageDescription")}
          dirty={() => !!draft && !valuesEqual(draft, savedDraft)}
          footer={(requestClose) => view === "channel" ? (
            <>
              <Checkbox checked={draft.enabled} onCheckedChange={(checked) => setDraft({ ...draft, enabled: checked })}>
                {t("settings.notifications.enableChannel")}
              </Checkbox>
              <span className="settings-notifications-footer-grow" />
              <Button variant="ghost" onClick={requestClose}>
                {t("settings.notifications.cancel")}
              </Button>
              <Button variant="primary" aria-label={t("settings.notifications.save")} disabled={saveDisabled} onClick={() => void save()}>
                {busy ? t("settings.notifications.saving") : t("settings.notifications.save")}
              </Button>
            </>
          ) : (
            <>
              <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.template.disabledKeepHint")}</span>
              <span className="settings-notifications-footer-grow" />
              <Button variant="ghost" onClick={() => setView("channel")}>
                {t("settings.notifications.template.done")}
              </Button>
              <Button variant="primary" aria-label={t("settings.notifications.save")} disabled={saveDisabled} onClick={() => void save()}>
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
                      <li key={index} className="text-caption">
                        <button type="button" className="settings-notifications-error-link" onClick={() => jumpToTemplateError(item)}>
                          {templateSaveErrorText(item)}
                        </button>
                      </li>
                    ))}
                  </ul>
                ) : undefined
              } />
            )
          )}

          {view === "channel" ? (
            <ChannelPage
              draft={draft}
              isNew={!draft.id}
              triggerKinds={triggerKinds}
              triggerVocabulary={triggerVocabulary}
              onChange={setDraft}
              onOpenTemplates={() => {
                setView("template");
                setMobileEditing(false);
              }}
              unsignedWarning={signingSecretMissing(draft, targets.data?.targets)}
            />
          ) : (
            <div className="settings-notifications-template-page">
              <div>
                <Button variant="ghost" size="compact" onClick={() => setView("channel")}>
                  <ChevronLeft size={14} aria-hidden />
                  {t("settings.notifications.template.backToChannel")}
                </Button>
              </div>
              <div className="settings-notifications-split">
                {(!isPhone || !mobileEditing) && (
                  <TemplateEventList
                    kinds={triggerKinds}
                    draft={draft}
                    current={tplKind}
                    isPhone={isPhone}
                    onPick={(kind) => {
                      setTplKind(kind);
                      setMobileEditing(true);
                    }}
                  />
                )}
                {(!isPhone || mobileEditing) && tplKind && (
                  <TemplatePane
                    key={tplKind}
                    kind={tplKind}
                    adapter={draft.adapter_kind}
                    catalogVersion={draft.catalog_version}
                    locale={draft.locale}
                    event={draft.events[tplKind] ?? defaultEventTemplate()}
                    isPhone={isPhone}
                    onBack={() => setMobileEditing(false)}
                    onChange={(event) => setDraft({ ...draft, events: { ...draft.events, [tplKind]: event } })}
                  />
                )}
              </div>
            </div>
          )}
        </SettingsEditDialog>
      )}

      <ConfirmDialog
        open={!!pendingDelete}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(undefined);
        }}
        title={pendingDelete ? t("settings.notifications.deleteTitle", { name: pendingDelete.name }) : ""}
        description={t("settings.notifications.deleteHint")}
        confirmLabel={deleting ? t("settings.notifications.deleting") : t("settings.notifications.delete")}
        danger
        onConfirm={() => void confirmDelete()}
      />

      {showHistory && <DeliveryHistoryDialog onClose={() => setShowHistory(false)} />}
    </section>
  );
}

/** The channel list row: badges first, the enabled events on the second line, actions at the right. */
function TargetRow({
  item,
  result,
  testing,
  deleting,
  editDisabled,
  onEdit,
  onTest,
  onHistory,
  onDelete,
}: {
  item: PluginTargetConfig;
  result?: { state: "delivered" | "failed" | "accepted"; text?: string };
  testing: boolean;
  deleting: boolean;
  editDisabled: boolean;
  onEdit: () => void;
  onTest: (kind: NotificationTriggerKind) => void;
  onHistory: () => void;
  onDelete: () => void;
}) {
  const AdapterIcon = adapterIcon[item.adapter_kind];
  const menuItems: MenuEntry[] = [];
  // The test submenu lists only the events this channel actually enabled.
  if (item.trigger_kinds.length > 0) {
    menuItems.push({
      type: "submenu",
      key: "test",
      label: testing ? t("settings.notifications.testing") : t("settings.notifications.test"),
      icon: <FlaskConical size={14} />,
      items: item.trigger_kinds.map((kind) => ({
        key: kind,
        label: notificationTriggerKindLabel(kind),
        onSelect: () => onTest(kind),
      })),
    });
  }
  menuItems.push(
    { key: "history", label: t("settings.delivery.history"), icon: <History size={14} />, onSelect: onHistory },
    { type: "divider", key: "divider" },
    { key: "delete", label: t("settings.notifications.delete"), icon: <Trash2 size={14} />, danger: true, onSelect: onDelete },
  );
  return (
    <li className="settings-notifications-item">
      <div className="settings-notifications-row">
        <AdapterIcon className="settings-notifications-icon" />
        <span className="settings-notifications-title-text text-body">{item.name}</span>
        <Chip>{adapterLabel()[item.adapter_kind]}</Chip>
        <StatePill tone={item.enabled ? "online" : "muted"}>{item.enabled ? t("common.enabled") : t("common.disabled")}</StatePill>
        <StatePill tone={item.has_secret ? "online" : "warning"} icon={KeyRound}>
          {item.has_secret ? t("common.credentialSet") : t("common.credentialUnset")}
        </StatePill>
        {item.adapter_kind === "webhook" && item.has_signing_secret === true && (
          <StatePill tone="online" icon={ShieldCheck}>
            {t("settings.notifications.signed")}
          </StatePill>
        )}
        {result && (
          <StatePill tone={result.state === "delivered" ? "online" : result.state === "failed" ? "offline" : "warning"}>
            {t("settings.notifications.testResult", { state: deliveryStateLabel(result.state === "accepted" ? "pending" : result.state) })}
          </StatePill>
        )}
        <span className="settings-notifications-spacer" />
        <div className="tw:flex tw:flex-none tw:items-center tw:gap-1">
          <Button variant="ghost" size="sm" disabled={editDisabled} onClick={onEdit}>
            <Pencil size={14} />
            {t("settings.notifications.edit")}
          </Button>
          <DropdownMenu
            trigger={
              <Button variant="ghost" size="sm" disabled={deleting} aria-label={t("settings.notifications.moreActions")}>
                <Ellipsis size={14} />
              </Button>
            }
            items={menuItems}
          />
        </div>
      </div>
      <div className="settings-notifications-events">
        {item.trigger_kinds.length === 0 ? (
          <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.noTrigger")}</span>
        ) : (
          <>
            <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.enabledEvents")}</span>
            {item.trigger_kinds.map((kind) => (
              <Chip key={kind}>{notificationTriggerKindLabel(kind)}</Chip>
            ))}
          </>
        )}
        {result?.text && (
          <span className="settings-notifications-muted-break text-caption tw:text-muted-foreground">· {result.text}</span>
        )}
      </div>
    </li>
  );
}

/** The dialog's first page: connection, credentials and the trigger-event grid. */
function ChannelPage({
  draft,
  isNew,
  triggerKinds,
  triggerVocabulary,
  onChange,
  onOpenTemplates,
  unsignedWarning,
}: {
  draft: NotificationDraft;
  isNew: boolean;
  triggerKinds: NotificationTriggerKind[];
  triggerVocabulary: { isPending: boolean; isError: boolean; data: unknown; refetch: () => Promise<unknown> };
  onChange: (draft: NotificationDraft) => void;
  onOpenTemplates: () => void;
  unsignedWarning: boolean;
}) {
  const customCount = customTemplateCount(draft.events);
  return (
    <>
      <div className="settings-notifications-responsive">
        <Field label={t("settings.notifications.field.type")}>
          <Select label={t("settings.notifications.field.type")} value={draft.adapter_kind} disabled={!isNew} options={[
              { value: "telegram", label: adapterLabel().telegram },
              { value: "feishu", label: adapterLabel().feishu },
              { value: "webhook", label: adapterLabel().webhook },
            ]} onChange={(value) => onChange({ ...draft, adapter_kind: value as PluginTargetConfig["adapter_kind"] })} />
        </Field>
        <Field label={t("settings.notifications.field.name")}>
          <Input aria-label={t("settings.notifications.field.name")} value={draft.name} onChange={(e) => onChange({ ...draft, name: e.target.value })} />
        </Field>
      </div>

      {draft.adapter_kind === "telegram" && (
        <div className="settings-notifications-field-gap settings-notifications-responsive">
          <Field label={t("settings.notifications.field.chatId")}>
            <Input aria-label={t("settings.notifications.field.chatId")} value={draft.chat_id} placeholder={t("settings.notifications.field.chatIdPlaceholder")} onChange={(e) => onChange({ ...draft, chat_id: e.target.value })} />
          </Field>
          <Field label={t("settings.notifications.field.botToken")}>
            <PasswordInput aria-label={t("settings.notifications.field.botToken")} value={draft.bot_token} placeholder={isNew ? t("settings.notifications.field.botTokenPlaceholder") : secretInputPlaceholder().telegram} onChange={(e) => onChange({ ...draft, bot_token: e.target.value })} />
          </Field>
        </div>
      )}

      {draft.adapter_kind === "feishu" && (
        <div className="settings-notifications-field-gap settings-notifications-responsive">
          <Field label={t("settings.notifications.field.webhookUrl")} className="settings-notifications-field-wide">
            <PasswordInput aria-label={t("settings.notifications.field.webhookUrl")} value={draft.webhook_url} placeholder={isNew ? t("settings.notifications.field.webhookUrlPlaceholder") : secretInputPlaceholder().feishu} onChange={(e) => onChange({ ...draft, webhook_url: e.target.value })} />
          </Field>
          <Field label={t("settings.notifications.field.signingSecret")}>
            <PasswordInput aria-label={t("settings.notifications.field.signingSecret")} value={draft.signing_secret} onChange={(e) => onChange({ ...draft, signing_secret: e.target.value })} />
          </Field>
        </div>
      )}

      {draft.adapter_kind === "webhook" && (
        <div className="settings-notifications-field-gap settings-notifications-responsive">
          <Field label={t("settings.notifications.field.endpointUrl")} className="settings-notifications-field-wide">
            <PasswordInput aria-label={t("settings.notifications.field.endpointUrl")} value={draft.endpoint_url} placeholder={isNew ? t("settings.notifications.field.endpointUrlPlaceholder") : secretInputPlaceholder().webhook} onChange={(e) => onChange({ ...draft, endpoint_url: e.target.value })} />
          </Field>
          <Field label={t("settings.notifications.field.authKind")}>
            <Select label={t("settings.notifications.field.authKind")} value={draft.auth_kind} options={[
                { value: "none", label: t("settings.notifications.field.authNone") },
                { value: "bearer", label: t("settings.notifications.field.authBearer") },
                { value: "header", label: t("settings.notifications.field.authHeader") },
              ]} onChange={(value) => onChange({ ...draft, auth_kind: value as "none" | "bearer" | "header" })} />
          </Field>
          {draft.auth_kind === "header" && (
            <Field label={t("settings.notifications.field.headerName")}>
              <Input aria-label={t("settings.notifications.field.headerName")} value={draft.auth_header_name} placeholder="X-Api-Key" onChange={(e) => onChange({ ...draft, auth_header_name: e.target.value })} />
            </Field>
          )}
          {draft.auth_kind !== "none" && (
            <Field label={draft.auth_kind === "bearer" ? t("settings.notifications.field.bearerValue") : t("settings.notifications.field.headerValue")}>
              <PasswordInput aria-label={draft.auth_kind === "bearer" ? t("settings.notifications.field.bearerValue") : t("settings.notifications.field.headerValue")} value={draft.auth_value} onChange={(e) => onChange({ ...draft, auth_value: e.target.value })} />
            </Field>
          )}
          <Field label={t("settings.notifications.field.webhookSigningSecret")}>
            <PasswordInput aria-label={t("settings.notifications.field.webhookSigningSecret")} value={draft.signing_secret} placeholder={t("settings.notifications.field.webhookSigningSecretPlaceholder")} onChange={(e) => onChange({ ...draft, signing_secret: e.target.value })} />
          </Field>
          {webhookSigningSecretTooShort(draft) && (
            <span role="alert" className="settings-notifications-field-wide text-caption tw:text-destructive">
              {t("settings.notifications.field.webhookSigningSecretTooShort")}
            </span>
          )}
          {unsignedWarning && (
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
        <div className="settings-notifications-field-gap">
          <div className={cn("settings-notifications-sec-head", !isNew && "tw:flex-wrap")}>
            <span className="text-caption">{t("settings.notifications.field.triggers")}</span>
            <span className="settings-notifications-footer-grow" />
            {customCount > 0 && (
              <span className="text-caption tw:text-muted-foreground">
                {t("settings.notifications.customTemplateCount", { n: customCount })}
              </span>
            )}
            <div className="tw:flex tw:items-center tw:gap-2">
              <Button size="sm" onClick={onOpenTemplates}>
                <Pencil size={14} aria-hidden="true" />{t("settings.notifications.template.editEntry")}
              </Button>
              {!isNew && <SettingsEventScripts />}
            </div>
          </div>
          <div className="settings-notifications-events-input">
            {triggerKinds.map((kind) => {
              const custom = draft.events[kind]?.mode === "custom";
              return (
                <Checkbox key={kind} checked={draft.trigger_kinds.includes(kind)} onCheckedChange={(checked) => {
                    const next = checked
                      ? [...draft.trigger_kinds.filter((k) => k !== kind), kind]
                      : draft.trigger_kinds.filter((k) => k !== kind);
                    next.sort((a, b) => triggerKinds.indexOf(a) - triggerKinds.indexOf(b));
                    onChange({ ...draft, trigger_kinds: next });
                  }}>
                  {notificationTriggerKindLabel(kind)}
                  {custom && <span className="settings-notifications-custom-dot" title={t("settings.notifications.template.badgeCustom")} />}
                </Checkbox>
              );
            })}
          </div>
        </div>
      )}

      {draft.id && (
        <SecretField
          editing
          className="settings-notifications-section-gap"
          mode={draft.secret_mode}
          onModeChange={(secret_mode) => onChange({ ...draft, secret_mode })}
          subject={secretFieldsLabel()[draft.adapter_kind]}
          removeHint={t("settings.notifications.removeSecretHint")}
        />
      )}
    </>
  );
}

/** The template page's event list: enabled events first, then the rest, each with its mode badge. */
function TemplateEventList({
  kinds,
  draft,
  current,
  isPhone,
  onPick,
}: {
  kinds: NotificationTriggerKind[];
  draft: NotificationDraft;
  current: NotificationTriggerKind | undefined;
  isPhone: boolean;
  onPick: (kind: NotificationTriggerKind) => void;
}) {
  const row = (kind: NotificationTriggerKind, enabled: boolean) => {
    const custom = draft.events[kind]?.mode === "custom";
    return (
      <button
        key={kind}
        type="button"
        className={cn("settings-notifications-tpl-row text-body", kind === current && "settings-notifications-tpl-row-on", !enabled && "settings-notifications-tpl-row-off")}
        onClick={() => onPick(kind)}
      >
        <span className="settings-notifications-tpl-tick">{enabled && <Check size={10} aria-hidden />}</span>
        <span className="settings-notifications-tpl-name">{notificationTriggerKindLabel(kind)}</span>
        {custom ? (
          <Chip tone="accent" className="settings-notifications-tpl-badge-custom">
            {t("settings.notifications.template.badgeCustom")}
          </Chip>
        ) : (
          <span className="settings-notifications-tpl-badge text-caption">{t("settings.notifications.template.badgeDefault")}</span>
        )}
        {isPhone && <ChevronRight size={14} aria-hidden className="settings-notifications-tpl-go" />}
      </button>
    );
  };
  const enabled = kinds.filter((kind) => draft.trigger_kinds.includes(kind));
  const rest = kinds.filter((kind) => !draft.trigger_kinds.includes(kind));
  return (
    <div className="settings-notifications-tpl-list">
      <div className="settings-notifications-tpl-group text-caption">{t("settings.notifications.template.enabledGroup")} {enabled.length}</div>
      {enabled.map((kind) => row(kind, true))}
      <div className="settings-notifications-tpl-group text-caption">{t("settings.notifications.template.disabledGroup")} {rest.length}</div>
      {rest.map((kind) => row(kind, false))}
    </div>
  );
}

/** One event's editor plus the live per-adapter preview. */
function TemplatePane({
  kind,
  adapter,
  catalogVersion,
  locale,
  event,
  isPhone,
  onBack,
  onChange,
}: {
  kind: NotificationTriggerKind;
  adapter: PluginTargetConfig["adapter_kind"];
  catalogVersion: NotificationCatalogVersion;
  locale: NotificationLocale;
  event: EventTemplateDraft;
  isPhone: boolean;
  onBack: () => void;
  onChange: (event: EventTemplateDraft) => void;
}) {
  const label = notificationTriggerKindLabel(kind);
  const titleRef = useRef<HTMLInputElement>(null);
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  // The insertion point follows the last focused input, defaulting to the body.
  const lastFocused = useRef<"title" | "body">("body");
  const catalog = useQuery({
    queryKey: templateCatalogKeys.notification(kind, catalogVersion, locale),
    queryFn: () => getNotificationTemplateCatalog(kind, catalogVersion, locale),
    staleTime: Infinity,
  });
  const defaults = catalog.data?.default_template;
  const mode = event.mode;

  function switchMode(next: TemplateMode) {
    // Switching to custom starts from the default template rather than a blank page.
    if (next === "custom" && defaults && event.title_template === "" && event.body_template === "") {
      onChange({ mode: next, title_template: defaults.title_template, body_template: defaults.body_template });
    } else {
      onChange({ ...event, mode: next });
    }
  }

  // Image variables are message-level attachments and can only be written in the body, on a line
  //   of their own. Provider images use the call form image(path); the plain image variable stays
  //   the bare reference.
  function catalogToken(path: string): string {
    if (path === "image") return "{{image}}";
    if (path.endsWith(".image")) return `{{image(${path})}}`;
    return `{{${path}}}`;
  }

  function insertToken(token: string) {
    const isImage = token === "{{image}}" || token.startsWith("{{image(");
    const field = isImage ? "body" : lastFocused.current;
    const element = field === "title" ? titleRef.current : bodyRef.current;
    const current = field === "title" ? event.title_template : event.body_template;
    const start = element?.selectionStart ?? current.length;
    const end = element?.selectionEnd ?? current.length;
    let before = current.slice(0, start);
    let after = current.slice(end);
    if (isImage) {
      if (before !== "" && !before.endsWith("\n")) before += "\n";
      if (after !== "" && !after.startsWith("\n")) after = "\n" + after;
    }
    const next = before + token + after;
    onChange(field === "title" ? { ...event, title_template: next } : { ...event, body_template: next });
    if (!element) return;
    requestAnimationFrame(() => {
      element.focus();
      const caret = before.length + token.length;
      element.setSelectionRange(caret, caret);
    });
  }

  const previewTitle = mode === "custom" ? event.title_template : (defaults?.title_template ?? "");
  const previewBody = mode === "custom" ? event.body_template : (defaults?.body_template ?? "");
  const previewReady = mode === "custom" || defaults !== undefined;

  return (
    <div className="settings-notifications-tpl-pane">
      <div className="settings-notifications-editor-head">
        {isPhone && (
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ChevronLeft size={14} aria-hidden />
            {t("settings.notifications.template.backToEvents")}
          </Button>
        )}
        <h4 className="settings-notifications-editor-title">{label}</h4>
        <span className="settings-notifications-footer-grow" />
        <SegmentedControl
          compact
          ariaLabel={t("settings.notifications.template.modeLabel", { event: label })}
          value={mode}
          options={[
            { value: "system_default", label: t("settings.notifications.template.modeDefault") },
            { value: "custom", label: t("settings.notifications.template.modeCustom") },
          ]}
          onChange={(next) => switchMode(next)}
        />
      </div>
      <div className="settings-notifications-editor-cols">
        <div className="settings-notifications-editor-main">
          {mode === "custom" ? (
            <>
              <Field label={t("settings.notifications.template.titleField")}>
                <Input ref={titleRef} aria-label={t("settings.notifications.template.titleField")} value={event.title_template} onFocus={() => { lastFocused.current = "title"; }} onChange={(e) => onChange({ ...event, title_template: e.target.value })} />
              </Field>
              <Field label={t("settings.notifications.template.bodyField")}>
                <Textarea ref={bodyRef} aria-label={t("settings.notifications.template.bodyField")} rows={6} value={event.body_template} onFocus={() => { lastFocused.current = "body"; }} onChange={(e) => onChange({ ...event, body_template: e.target.value })} />
              </Field>
              {catalog.data ? (
                <details className="settings-notifications-template-catalog">
                  <summary className="text-caption">
                    {t("settings.notifications.template.variables", { n: catalog.data.variables.length })}
                  </summary>
                  <FactCatalog
                    facts={catalog.data.variables.map((variable) => ({ ...variable, path: variable.name }))}
                    token={catalogToken}
                    onInsert={insertToken}
                  />
                  <div className="settings-notifications-template-functions">
                    <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.template.syntaxTitle")}</span>
                    <ul className="settings-notifications-template-syntax">
                      <li className="text-caption">{t("settings.notifications.template.syntaxLines")}</li>
                      <li className="text-caption">{t("settings.notifications.template.syntaxLinkInline")}</li>
                      <li className="text-caption">{t("settings.notifications.template.syntaxImage")}</li>
                      <li className="text-caption">{t("settings.notifications.template.syntaxTitlePlain")}</li>
                    </ul>
                    <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.template.functionsTitle")}</span>
                    <ul className="settings-notifications-template-syntax">
                      {catalog.data.functions.map((fn) => (
                        <li key={fn.name} className="text-caption">
                          <code>{fn.name}({fn.arguments.join(", ")})</code>{" — "}
                          {tryT(`templateFacts.functions.${fn.name}`) ?? fn.description}
                        </li>
                      ))}
                    </ul>
                  </div>
                </details>
              ) : (
                <span className="tw:text-muted-foreground">
                  {catalog.isError ? t("settings.notifications.template.catalogFailed") : t("settings.notifications.template.catalogLoading")}
                </span>
              )}
              <div>
                <Button variant="ghost" size="sm" onClick={() => onChange({ mode: "system_default", title_template: "", body_template: "" })}>
                  <RotateCcw size={14} aria-hidden />
                  {t("settings.notifications.template.resetDefault")}
                </Button>
              </div>
            </>
          ) : defaults ? (
            <>
              <div className="settings-notifications-template-defaults">
                <div className="settings-notifications-template-ro">
                  <span className="settings-notifications-template-ro-label text-caption">{t("settings.notifications.template.defaultTitle")}</span>
                  <p className="settings-notifications-template-ro-box text-body">{defaults.title_template}</p>
                </div>
                <div className="settings-notifications-template-ro">
                  <span className="settings-notifications-template-ro-label text-caption">{t("settings.notifications.template.defaultBody")}</span>
                  <p className="settings-notifications-template-ro-box settings-notifications-template-ro-pre text-body">{defaults.body_template}</p>
                </div>
              </div>
              <p className="text-caption tw:text-muted-foreground">{t("settings.notifications.template.followDefault")}</p>
            </>
          ) : (
            <div className="settings-notifications-template-fallback">
              <span className="tw:text-muted-foreground">
                {catalog.isError ? t("settings.notifications.template.defaultFailed") : t("settings.notifications.template.defaultLoading")}
              </span>
            </div>
          )}
        </div>
        {previewReady ? (
          <TemplatePreview
            kind={kind}
            adapter={adapter}
            catalogVersion={catalogVersion}
            title={previewTitle}
            body={previewBody}
          />
        ) : (
          <div className="settings-notifications-preview text-body">
            <span className="text-caption tw:text-muted-foreground">{t("settings.notifications.template.defaultLoading")}</span>
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * The live preview: edits are debounced, then the backend renders the template with its fixed
 * sample facts and the result is drawn in the adapter's own shape (Telegram photo caption, Feishu
 * post card, webhook message envelope).
 */
function TemplatePreview({
  kind,
  adapter,
  catalogVersion,
  title,
  body,
}: {
  kind: NotificationTriggerKind;
  adapter: PluginTargetConfig["adapter_kind"];
  catalogVersion: NotificationCatalogVersion;
  title: string;
  body: string;
}) {
  const [result, setResult] = useState<NotificationTemplatePreview>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const seq = useRef(0);

  useEffect(() => {
    const id = ++seq.current;
    setLoading(true);
    const timer = setTimeout(() => {
      previewNotificationTemplate({
        trigger_kind: kind,
        language_version: TEMPLATE_LANGUAGE_VERSION,
        catalog_version: catalogVersion,
        title_template: title,
        body_template: body,
      })
        .then((next) => {
          if (seq.current !== id) return;
          setResult(next);
          setError(undefined);
          setLoading(false);
        })
        .catch((cause: unknown) => {
          if (seq.current !== id) return;
          setError(toUserMessage(cause, t("settings.notifications.template.previewFailed")));
          setLoading(false);
        });
    }, 350);
    return () => clearTimeout(timer);
  }, [kind, catalogVersion, title, body]);

  return (
    <div className="settings-notifications-preview text-body">
      <div className="settings-notifications-preview-label text-caption">
        {t("settings.notifications.template.previewAs", { adapter: adapterLabel()[adapter] })}
        {loading && <SpinnerIcon />}
      </div>
      {error && <Alert tone="error" title={error} />}
      {result && !result.valid && (
        <StatePill tone="offline">{t("settings.notifications.template.invalid")}</StatePill>
      )}
      {result && result.valid && (
        <AdapterMessage adapter={adapter} kind={kind} presentation={result.presentation} />
      )}
      {result?.errors.map((item, index) => (
        <div key={`e${index}`}>
          <span className="text-caption tw:text-destructive">
            {item.field ? `${item.field}: ` : ""}{item.message}
          </span>
        </div>
      ))}
      {result?.warnings.map((item, index) => (
        <div key={`w${index}`}>
          <span className="text-caption tw:text-warning">
            {item.variable ? `${item.variable}: ` : ""}{item.message}
          </span>
        </div>
      ))}
    </div>
  );
}

/**
 * The rendered message in the adapter's own shape. Channel limits and shapes
 * come from the backend presentation, which the delivery formatters also use,
 * so the preview cannot drift from what is actually sent.
 */
function AdapterMessage({
  adapter,
  kind,
  presentation,
}: {
  adapter: PluginTargetConfig["adapter_kind"];
  kind: NotificationTriggerKind;
  presentation: NotificationTemplatePreview["presentation"];
}) {
  if (adapter === "telegram") {
    const telegram = presentation.telegram;
    const asPhoto = telegram.mode === "sendPhoto";
    return (
      <div className="settings-notifications-preview-tg">
        {asPhoto && (
          <div className="settings-notifications-preview-image text-caption">{t("settings.notifications.template.imageNote")}</div>
        )}
        {telegram.image_omitted && (
          <p className="text-caption tw:text-warning">{t("settings.notifications.template.telegramImageOmitted")}</p>
        )}
        {telegram.truncated && (
          <p className="text-caption tw:text-warning">{t("settings.notifications.template.telegramTruncated")}</p>
        )}
        <div className="settings-notifications-preview-tg-title">{telegram.title}</div>
        {telegram.body_parts.length > 0 && (
          <div className="settings-notifications-preview-body">{renderBodyInline(telegram.body_parts)}</div>
        )}
        {telegram.buttons.length > 0 && (
          <div className="settings-notifications-preview-url-buttons">
            {telegram.buttons.map((button, index) => (
              <span key={index}>{button.label}</span>
            ))}
          </div>
        )}
      </div>
    );
  }
  if (adapter === "feishu") {
    const feishu = presentation.feishu;
    // Feishu cannot upload images: an available image is the last link
    // paragraph in content, and no URL means nothing is shown.
    return (
      <div className="settings-notifications-preview-fs">
        <div className="settings-notifications-preview-fs-head">{feishu.title}</div>
        <div className="settings-notifications-preview-body settings-notifications-preview-fs-body">{renderFeishuContent(feishu.content)}</div>
        {feishu.image_omitted && (
          <div className="settings-notifications-preview-fs-foot text-caption tw:text-warning">{t("settings.notifications.template.feishuImageNoUrl")}</div>
        )}
      </div>
    );
  }
  const message = presentation.webhook.message;
  return (
    <>
      <pre className="settings-notifications-preview-wh">{JSON.stringify({ event: kind, message }, null, 2)}</pre>
      <p className="text-caption tw:text-muted-foreground">{t("settings.notifications.template.webhookEnvelopeNote")}</p>
    </>
  );
}

/** Feishu post paragraphs, one line each; link elements stay inline. */
function renderFeishuContent(
  content: NotificationTemplatePreview["presentation"]["feishu"]["content"],
): React.ReactNode {
  if (content.length === 0) return null;
  const nodes: React.ReactNode[] = [];
  let key = 0;
  for (const paragraph of content) {
    if (nodes.length > 0) nodes.push(<br key={key++} />);
    for (const element of paragraph) {
      if (element.tag === "a") {
        nodes.push(
          <span key={key++} className="settings-notifications-preview-anchor">
            {element.text}
          </span>,
        );
      } else if (element.text !== " ") {
        nodes.push(<span key={key++}>{element.text}</span>);
      }
    }
  }
  return nodes;
}

/** Body parts in template order: links stay inline where the template wrote them. */
function renderBodyInline(
  bodyParts: NotificationTemplatePreview["body_parts"],
): React.ReactNode {
  const nodes: React.ReactNode[] = [];
  let key = 0;
  for (const part of bodyParts) {
    if (part.link) {
      nodes.push(
        <span key={key++} className="settings-notifications-preview-anchor">
          {part.text}
        </span>,
      );
      continue;
    }
    part.text.split("\n").forEach((chunk, index) => {
      if (index > 0) nodes.push(<br key={key++} />);
      if (chunk) nodes.push(chunk);
    });
  }
  return nodes;
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
