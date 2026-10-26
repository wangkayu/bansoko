import { Alert, Button, Checkbox, Input, Select, StateBlock } from "../../ui/base";
import { reasonLabel } from "../../lib/i18n/reasons";
import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useQuery, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { Pencil, Plus, RotateCcw } from "lucide-react";
import { toast } from "../../ui/toast";
import {
  isVersionConflict,
  listConnections,
  listNetworkRoutes,
  newUUID,
  putConnection,
  putNetworkRoute,
  testConnection,
  testNetworkRoute,
  toUserMessage,
  type Connection,
  type ConnectionInput,
  type NetworkRoute,
  type NetworkRouteInput,
} from "../../lib/api/client";
import { testHealthLabel, testHealthTone } from "../../lib/labels";
import { valuesEqual } from "../../lib/draft";
import { configKeys } from "../../lib/queryKeys";
import { Chip, StatePill, ConflictBlock, SecretField, type SecretMode } from "../../ui";
import { DialogFeedback, SettingsEditDialog } from "./SettingsEditDialog";
import { t } from "../../lib/i18n";

// Service connections: AniBT / Mikan / BGM / RSS HTTP / TMDB plus network routes.
//   The qBittorrent downloader has moved to the acquisition settings section (SettingsDownload) and
//   is neither shown nor edited here. The list is read-only cards with editing in a fixed-size
//   dialog. The connection secret (the tmdb API Key) and route credentials (the http_proxy / socks5
//   passwords) both use SecretField's explicit three states (keep/replace/remove), matching the
//   ConnectionSecret contract; on a route replace the username is required and the server composes
//   it into one user:pass secret.
//
//   Built-in connections (mikan/anibt/bgm/tmdb) are seeded by instance initialization, exactly one
//   per kind: they cannot be deleted (the contract has no delete) or newly created twice (the
//   create dialog forbids an existing kind), while baseurl/routes are editable with a
//   "restore defaults" (official address + direct connection). RSS HTTP is a transport-policy
//   connection and may be created freely.
//
//   The route form takes a unified URL input: http_proxy fills http://host:port and socks5 fills
//   socks5://host:port, aligned with the backend's Endpoint scheme validation (http/https and
//   socks5/socks5h); on save a socks5 URL is parsed into the contract's host/port fields, and on
//   read composed back into a URL for display.

type ConnectionSecretInput = { mode: "keep" } | { mode: "replace"; value: string } | { mode: "remove" };

// A route draft's local validation failure (an unparseable SOCKS5 address): equivalent to the old
//   in-save `setError(...); return`, thrown through the shared save loop so the caller catches it,
//   sets error and never sends the request.
class RouteDraftError extends Error {}

// Each provider's real official address: prefilled on create/type switch, editable and clearable by
//   the user, with a save-time fallback for TMDB. rss_http has no address (the system's fixed
//   transport policy).
const PROVIDER_DEFAULT_URL: Partial<Record<ConnectionDraft["kind"], string>> = {
  mikan: "https://mikanani.me",
  anibt: "https://anibt.net",
  bgm: "https://api.bgm.tv",
  tmdb: "https://api.themoviedb.org/3",
};

// Built-in provider/metadata connections: seeded at fresh-instance initialization, one per kind.
const BUILTIN_CONNECTION_KINDS = ["mikan", "anibt", "bgm", "tmdb"] as const;
type BuiltinConnectionKind = (typeof BUILTIN_CONNECTION_KINDS)[number];

function isBuiltinConnection(item: Connection): boolean {
  return (BUILTIN_CONNECTION_KINDS as readonly string[]).includes(item.kind);
}

// Parses a socks5 URL into contract host/port fields; returns null when the
// input is not a well-formed socks5/socks5h URL with a valid port.
function parseSocks5URL(input: string): { host: string; port: number } | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    return null;
  }
  if (url.protocol !== "socks5:" && url.protocol !== "socks5h:") return null;
  if (url.hostname === "") return null;
  const port = url.port === "" ? 1080 : Number(url.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null;
  return { host: url.hostname, port };
}

// Validates the uniform proxy endpoint URL against the backend scheme rules:
// http_proxy accepts http/https, socks5 accepts socks5/socks5h.
function routeEndpointError(kind: "http_proxy" | "socks5", value: string): string | undefined {
  const trimmed = value.trim();
  if (trimmed === "") return t("settings.proxy.addressRequired");
  if (kind === "http_proxy") {
    if (!/^https?:\/\/.+/i.test(trimmed)) return t("settings.proxy.httpInvalid");
    return undefined;
  }
  return parseSocks5URL(trimmed) ? undefined : t("settings.proxy.socksInvalid");
}

type ConnectionDraft = {
  id?: string;
  version: number;
  kind: "rss_http" | "tmdb" | "anibt" | "mikan" | "bgm";
  name: string;
  enabled: boolean;
  base_url: string;
  api_key: string;
  secret_mode: SecretMode;
  route_id: string;
};

function connectionDraftFrom(item?: Connection): ConnectionDraft {
  if (!item) {
    // A first-time user most often creates a provider: default to Mikan and prefill its real address.
    return {
      version: 0,
      kind: "mikan",
      name: "",
      enabled: true,
      base_url: PROVIDER_DEFAULT_URL.mikan!,
      api_key: "",
      secret_mode: "replace",
      route_id: "",
    };
  }
  const base: ConnectionDraft = {
    id: item.id,
    version: item.version,
    kind: item.kind === "qbittorrent" ? "rss_http" : item.kind,
    name: item.name,
    enabled: item.enabled,
    base_url: "",
    api_key: "",
    secret_mode: "keep",
    route_id: item.route_id ?? "",
  };
  if (item.kind === "tmdb" || item.kind === "anibt" || item.kind === "mikan" || item.kind === "bgm") {
    base.base_url = item.base_url;
    if (item.kind === "tmdb") {
      base.secret_mode = item.credential_configured ? "keep" : "replace";
    }
  }
  return base;
}

type RouteDraft = {
  id?: string;
  version: number;
  kind: "direct" | "http_proxy" | "socks5";
  name: string;
  enabled: boolean;
  // Uniform proxy endpoint URL for both http_proxy (http://host:port) and
  // socks5 (socks5://host:port); split into contract fields on save.
  endpoint: string;
  username: string;
  password: string;
  secret_mode: SecretMode;
  // Whether the stored route already holds credentials; without them the dialog works as on create.
  credential_configured: boolean;
};

function routeDraftFrom(item?: NetworkRoute): RouteDraft {
  if (!item) {
    return {
      version: 0,
      kind: "direct",
      name: "",
      enabled: true,
      endpoint: "",
      username: "",
      password: "",
      secret_mode: "replace",
      credential_configured: false,
    };
  }
  const base: RouteDraft = {
    id: item.id,
    version: item.version,
    kind: item.kind,
    name: item.name,
    enabled: item.enabled,
    endpoint: "",
    username: "",
    password: "",
    secret_mode: "replace",
    credential_configured: false,
  };
  if (item.kind === "http_proxy") {
    base.endpoint = item.proxy_url;
    // Configured credentials are kept by default; an unconfigured route is edited like a new one (no keep/remove choice).
    base.credential_configured = item.credential_configured;
    base.secret_mode = item.credential_configured ? "keep" : "replace";
  } else if (item.kind === "socks5") {
    base.endpoint = `socks5://${item.host}:${item.port}`;
    base.credential_configured = item.credential_configured;
    base.secret_mode = item.credential_configured ? "keep" : "replace";
  }
  return base;
}

// The connection secret's three states (the ConnectionSecret contract): create and replace write replace; editing may keep/remove.
function connectionSecretFor(draft: ConnectionDraft, value: string): ConnectionSecretInput {
  if (!draft.id || draft.secret_mode === "replace") return { mode: "replace", value };
  if (draft.secret_mode === "remove") return { mode: "remove" };
  return { mode: "keep" };
}

// Field limits shared with the OpenAPI contract (name 128, secret value 1024, username 256).
const NAME_MAX = 128;
const SECRET_MAX = 1024;
const USERNAME_MAX = 256;

// The username and password inputs take part in a save when a route has no stored credentials
//   (create-like) or the stored ones are being replaced.
function routeCredentialInputsActive(draft: RouteDraft): boolean {
  return draft.kind !== "direct" && (!draft.credential_configured || draft.secret_mode === "replace");
}

// Username and password go together: the server composes them into one user:pass secret and splits
//   it at the first colon, so the username cannot contain one. `incomplete` marks replacing stored
//   credentials with nothing entered yet (blocks save without a message).
function routeCredentialCheck(draft: RouteDraft): { usernameError?: string; passwordError?: string; incomplete: boolean } {
  if (!routeCredentialInputsActive(draft)) return { incomplete: false };
  const username = draft.username.trim();
  const hasPassword = draft.password !== "";
  if (username.includes(":")) return { usernameError: t("settings.proxy.usernameColon"), incomplete: false };
  if (hasPassword && username === "") return { usernameError: t("settings.proxy.usernameRequired"), incomplete: false };
  if (!hasPassword && username !== "") return { passwordError: t("settings.proxy.passwordRequired"), incomplete: false };
  return { incomplete: draft.credential_configured && !hasPassword && username === "" };
}

function routeDraftInvalid(draft: RouteDraft): boolean {
  if (draft.name.trim() === "") return true;
  if (draft.kind !== "direct" && routeEndpointError(draft.kind, draft.endpoint) !== undefined) return true;
  const check = routeCredentialCheck(draft);
  return check.usernameError !== undefined || check.passwordError !== undefined || check.incomplete;
}

// The route credentials' three states (the ConnectionSecret contract): without stored credentials
//   an empty password sends no secret at all; with stored ones keep/remove/replace are explicit,
//   and on replace the server composes username + password into user:pass.
function routeSecretFor(draft: RouteDraft): ConnectionSecretInput | undefined {
  if (draft.kind === "direct") return undefined;
  if (!draft.credential_configured) {
    return draft.password === "" ? undefined : { mode: "replace", value: draft.password };
  }
  if (draft.secret_mode === "replace") return { mode: "replace", value: draft.password };
  if (draft.secret_mode === "remove") return { mode: "remove" };
  return { mode: "keep" };
}

// The two cards, connections and network routes, share one "list-row entity" draft editing loop
//   (draft + opened snapshot -> valuesEqual dirty -> expected_version/If-Match CAS save ->
//   error/409 conflict feedback -> close after a successful save or a conflict refresh, and
//   invalidate the list).
//   The shared lib's useSettingsSection targets only the global-sections single-section contract
//   (listGlobalSections/putGlobalSection, a section-wide schema_version, the draft following the
//   server while clean, and keeping the dialog open to refill the draft after a conflict refresh);
//   it cannot express this page's row-set semantics (row-level version, create via newUUID, dirty
//   from an opened snapshot, closing on a conflict refresh), so it is converged here into a local
//   hook and never pushed into the shared lib.
//
// The behavior contract (aligned item by item with this page's original handwritten loop):
//   - openEditor: clears error/conflict and puts the draft and the opened snapshot in place (the
//     dirty baseline).
//   - A successful save: toast.success -> close the editor -> invalidate the invalidateQueryKey.
//   - A 409 save: set conflict (the server's current version) plus the user-facing error copy; other
//     failures set error only. error/conflict clear on the next
//     openEditor/save/refreshAfterConflict.
//   - refreshAfterConflict: clear conflict/error -> close the editor (dropping the local draft) ->
//     invalidate, letting the user reopen editing on the latest row data.
//   - dirty is judged by the caller's valuesEqual(draft, savedDraft) snapshot comparison; the hook
//     takes no part, keeping the two edit dialogs' close confirmations independent.

type UseEntityDraftEditorOptions<Draft extends { id?: string; version: number }, Input> = {
  /** Submits the draft: assembles the contract input and PUTs with the draft's id (a newUUID when missing) and version. */
  submit: (draft: Draft) => Promise<unknown>;
  /** The save-success toast copy (e.g. "connection saved"). */
  successMessage: string;
  /** The save-failure user-copy prefix (the toUserMessage fallback). */
  failureFallback: string;
  /** The list query keys to refetch after a successful save or conflict refresh. */
  invalidateQueryKey: QueryKey;
};

function useEntityDraftEditor<Draft extends { id?: string; version: number }, Input>(
  options: UseEntityDraftEditorOptions<Draft, Input>,
) {
  const { submit, successMessage, failureFallback, invalidateQueryKey } = options;
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<Draft>();
  const [savedDraft, setSavedDraft] = useState<Draft>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [conflict, setConflict] = useState<number>();

  function openEditor(next: Draft) {
    setError(undefined);
    setConflict(undefined);
    setDraft(next);
    setSavedDraft(next);
  }

  function closeEditor() {
    setDraft(undefined);
    setSavedDraft(undefined);
  }

  async function save() {
    if (!draft) return;
    setBusy(true);
    setError(undefined);
    setConflict(undefined);
    try {
      await submit(draft);
      toast.success(successMessage);
      closeEditor();
      await queryClient.invalidateQueries({ queryKey: invalidateQueryKey });
    } catch (cause) {
      if (isVersionConflict(cause)) {
        setConflict(cause.currentVersion);
      }
      setError(toUserMessage(cause, failureFallback));
    } finally {
      setBusy(false);
    }
  }

  // On a 409 refresh: refetch the list and clear the editing state, letting the user re-edit on the latest version.
  function refreshAfterConflict() {
    setConflict(undefined);
    setError(undefined);
    closeEditor();
    void queryClient.invalidateQueries({ queryKey: invalidateQueryKey });
  }

  return {
    draft,
    setDraft,
    savedDraft,
    busy,
    setBusy,
    error,
    setError,
    conflict,
    openEditor,
    closeEditor,
    save,
    refreshAfterConflict,
  };
}

export function SettingsConnections() {
  return (
    <div className="settings-connections">
      <ConnectionsCard />
      <RoutesCard />
    </div>
  );
}

// The qBittorrent downloader belongs to the acquisition settings section and is no longer listed among service connections.
function visibleConnections(items: Connection[]): Connection[] {
  return items.filter((item) => item.kind !== "qbittorrent");
}

// Secrets: credentials are required in the create and replace states; the edit keep/remove states require no input.
function secretValueMissing(draft: ConnectionDraft): boolean {
  if (draft.kind !== "tmdb") return false;
  if (draft.id && draft.secret_mode !== "replace") return false;
  return draft.api_key.trim() === "";
}

function ConnectionsCard() {
  const queryClient = useQueryClient();
  const connections = useQuery({ queryKey: configKeys.connections, queryFn: listConnections });
  const routes = useQuery({ queryKey: configKeys.networkRoutes, queryFn: listNetworkRoutes });
  const {
    draft,
    setDraft,
    savedDraft,
    busy,
    error,
    conflict,
    openEditor,
    closeEditor,
    save,
    refreshAfterConflict,
  } = useEntityDraftEditor<ConnectionDraft, ConnectionInput>({
    submit: (current) => {
      const routeID = current.route_id === "" ? undefined : current.route_id;
      let input: ConnectionInput;
      if (current.kind === "tmdb") {
        input = {
          kind: "tmdb",
          name: current.name.trim(),
          enabled: current.enabled,
          base_url: current.base_url.trim() || "https://api.themoviedb.org/3",
          secret: connectionSecretFor(current, current.api_key),
          route_id: routeID,
        };
      } else if (current.kind === "anibt" || current.kind === "mikan" || current.kind === "bgm") {
        input = {
          kind: current.kind,
          name: current.name.trim(),
          enabled: current.enabled,
          base_url: current.base_url.trim(),
          route_id: routeID,
        };
      } else {
        input = {
          kind: "rss_http",
          name: current.name.trim(),
          enabled: current.enabled,
          route_id: routeID,
        };
      }
      return putConnection(current.id ?? newUUID(), input, current.version);
    },
    successMessage: t("settings.connections.saved"),
    failureFallback: t("settings.connections.saveFailed"),
    invalidateQueryKey: configKeys.connections,
  });
  const [testing, setTesting] = useState<string>();
  const [testResults, setTestResults] = useState<Record<string, { health: "ready" | "degraded" | "failed"; text?: string }>>({});

  // Deep link: `/settings/connections?connection=<id|kind>` opens that connection's edit dialog
//   (the landing target of each row in the state sheet). Matched once, consumed and cleared, so a
//   refresh does not reopen it repeatedly.
  const [searchParams, setSearchParams] = useSearchParams();
  const deepLink = searchParams.get("connection");
  const deepLinkHandled = useRef(false);
  useEffect(() => {
    if (deepLinkHandled.current || !deepLink || !connections.data) return;
    deepLinkHandled.current = true;
    const target = connections.data.find((item) => item.id === deepLink || item.kind === deepLink);
    if (target) openEditor(connectionDraftFrom(target));
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.delete("connection");
      return next;
    }, { replace: true });
  }, [deepLink, connections.data, openEditor, setSearchParams]);

  // Builtin kinds that already have a row cannot be created again; the user
  // edits the seeded row instead.
  const usedBuiltinKinds = new Set(
    (connections.data ?? []).filter(isBuiltinConnection).map((item) => item.kind as BuiltinConnectionKind),
  );

  // Restore defaults: a built-in connection returns to the official address plus a direct connection (route binding cleared), saved on the user's confirmation.
  function restoreDefaults() {
    if (!draft || !(BUILTIN_CONNECTION_KINDS as readonly string[]).includes(draft.kind)) return;
    setDraft({ ...draft, base_url: PROVIDER_DEFAULT_URL[draft.kind as BuiltinConnectionKind] ?? "", route_id: "" });
  }

  async function test(item: Connection) {
    setTesting(item.id);
    try {
      const result = await testConnection(item.id);
      setTestResults((prev) => ({
        ...prev,
        [item.id]: { health: result.health, text: reasonLabel(result.reason_code) },
      }));
      // Testing a connection advances its health state; refetch the cache so later edits see the latest row data.
      await queryClient.invalidateQueries({ queryKey: configKeys.connections });
    } catch (cause) {
      setTestResults((prev) => ({
        ...prev,
        [item.id]: { health: "failed", text: toUserMessage(cause, t("settings.connections.testFailed")) },
      }));
    } finally {
      setTesting(undefined);
    }
  }

  return (
    <section className="settings-connections-section">
      <div className="settings-connections-header">
        <h2 className="settings-connections-title text-title">{t("settings.connections.title")}</h2>
        <Button variant="primary" size="sm" onClick={() => openEditor(connectionDraftFrom())}>
          <Plus size={14} />
          {t("settings.connections.new")}
        </Button>
      </div>

      {connections.isLoading && (
        <StateBlock kind="busy" title={t("settings.connections.loading")} />
      )}
      {connections.error && <Alert tone="error" title={toUserMessage(connections.error, t("settings.connections.loadFailed"))} />}
      {connections.data && visibleConnections(connections.data).length === 0 && !draft && (
        <StateBlock kind="empty" title={t("settings.connections.empty")} />
      )}

      <ul className="settings-connections-list">
        {visibleConnections(connections.data ?? []).map((item) => {
          const kindLabel = item.kind === "tmdb" ? "TMDB" : item.kind === "anibt" ? "AniBT" : item.kind === "mikan" ? "Mikan" : item.kind === "bgm" ? "Bangumi" : t("settings.connections.rssKind");
          return (
          <li key={item.id} className="settings-connections-item">
            <div className="settings-connections-item-row">
              <span className="settings-connections-item-title text-body">{item.name}</span>
              {/* Skip the type badge when the name equals the type's display name (e.g. the default "Mikan") */}
              {item.name.trim().toLowerCase() !== kindLabel.toLowerCase() && <Chip>{kindLabel}</Chip>}
              {isBuiltinConnection(item) && <Chip>{t("settings.connections.builtin")}</Chip>}
              <StatePill tone={item.enabled ? "online" : "muted"}>{item.enabled ? t("common.enabled") : t("common.disabled")}</StatePill>
              {testResults[item.id] && (
                <StatePill tone={testHealthTone(testResults[item.id]!.health)}>
                  {testHealthLabel(testResults[item.id]!.health)}
                </StatePill>
              )}
              <span className="settings-connections-spacer" />
              <Button variant="ghost" aria-label={t("settings.connections.edit")} size="sm" onClick={() => openEditor(connectionDraftFrom(item))}>
                <Pencil size={14} />
                {t("settings.connections.edit")}
              </Button>
              {item.kind !== "rss_http" && (
                <Button aria-label={testing === item.id ? t("settings.connections.testing") : t("settings.connections.test")} size="sm" loading={testing === item.id} disabled={testing === item.id} onClick={() => void test(item)}>
                  {testing === item.id ? t("settings.connections.testing") : t("settings.connections.test")}
                </Button>
              )}
            </div>
            <p className="settings-connections-description text-caption">
              {item.kind === "rss_http" ? t("settings.connections.rssRowNote") : item.base_url}
              {testResults[item.id]?.text && ` · ${testResults[item.id]!.text}`}
            </p>
          </li>
          );
        })}
      </ul>

      {draft && (
        <SettingsEditDialog
          open
          onOpenChange={(o) => { if (!o) closeEditor(); }}
          title={draft.id ? t("settings.connections.editTitle") : t("settings.connections.newTitle")}
          description={t("settings.connections.dialogHint")}
          dirty={() => !!draft && !valuesEqual(draft, savedDraft)}
          footer={(requestClose) => (
            <>
              <Button variant="ghost" onClick={requestClose}>{t("settings.connections.cancel")}</Button>
              <Button variant="primary" aria-label={t("settings.connections.save")} disabled={busy || draft.name.trim() === "" || secretValueMissing(draft)} onClick={() => void save()}>
                {busy ? t("settings.connections.saving") : t("settings.connections.save")}
              </Button>
            </>
          )}
        >
          {conflict !== undefined ? (
            <ConflictBlock currentVersion={conflict} message={error} onRefresh={refreshAfterConflict} />
          ) : (
            error && <Alert tone="error" title={error} />
          )}
          <div className="settings-connections-form settings-connections-form-responsive">
            <Field
              label={t("settings.connections.typeLabel")}
              hint={!draft.id && usedBuiltinKinds.size > 0 ? t("settings.connections.typeHint") : undefined}
            >
              <Select label={t("settings.connections.typeLabel")} value={draft.kind} disabled={!!draft.id} options={[
                  { value: "mikan", label: `Mikan${usedBuiltinKinds.has("mikan") ? t("settings.connections.builtinExisting") : ""}`, disabled: usedBuiltinKinds.has("mikan") },
                  { value: "anibt", label: `AniBT${usedBuiltinKinds.has("anibt") ? t("settings.connections.builtinExisting") : ""}`, disabled: usedBuiltinKinds.has("anibt") },
                  { value: "bgm", label: `BGM${usedBuiltinKinds.has("bgm") ? t("settings.connections.builtinExisting") : ""}`, disabled: usedBuiltinKinds.has("bgm") },
                  { value: "rss_http", label: t("settings.connections.rssKind") },
                  { value: "tmdb", label: `TMDB${usedBuiltinKinds.has("tmdb") ? t("settings.connections.builtinExisting") : ""}`, disabled: usedBuiltinKinds.has("tmdb") },
                ]} onChange={(kind) => {
                  // On a type switch: an empty address or one of the official defaults becomes the new type's
//                   default address; a user-entered custom address (proxies included) is kept, never
//                   overwritten.
                  const knownDefaults = Object.values(PROVIDER_DEFAULT_URL);
                  const refill = draft.base_url.trim() === "" || knownDefaults.includes(draft.base_url.trim());
                  setDraft({ ...draft, kind: kind as ConnectionDraft["kind"], base_url: refill ? (PROVIDER_DEFAULT_URL[kind as ConnectionDraft["kind"]] ?? "") : draft.base_url });
                }} />
            </Field>
            <Field label={t("settings.connections.nameLabel")}>
              <Input aria-label={t("settings.connections.nameLabel")} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </Field>
          </div>

          {draft.id && (BUILTIN_CONNECTION_KINDS as readonly string[]).includes(draft.kind) && (
            <div className="settings-connections-field-gap">
              <Button variant="ghost" aria-label={t("settings.connections.restoreDefaults")} size="sm" onClick={restoreDefaults}>
                <RotateCcw size={14} />
                {t("settings.connections.restoreDefaults")}
              </Button>
            </div>
          )}

          {draft.kind === "tmdb" && (
            <div className="settings-connections-field-gap settings-connections-form">
              <Field label={t("settings.connections.tmdbAddress")}>
                <Input aria-label={t("settings.connections.tmdbAddress")} value={draft.base_url} placeholder="https://api.themoviedb.org/3" onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} />
              </Field>
              <SecretField
                className="settings-connections-field-wide"
                editing={!!draft.id}
                mode={draft.secret_mode}
                onModeChange={(mode) => setDraft({ ...draft, secret_mode: mode })}
                subject={t("settings.connections.tmdbKey")}
                inputLabel={t("settings.connections.tmdbKey")}
                value={draft.api_key}
                onChange={(next) => setDraft({ ...draft, api_key: next })}
              />
            </div>
          )}

          {(draft.kind === "anibt" || draft.kind === "mikan" || draft.kind === "bgm") && (
            <div className="settings-connections-field-gap settings-connections-form">
              <Field label={draft.kind === "bgm" ? t("settings.connections.bgmAddress") : t("settings.connections.siteAddress")}>
                <Input aria-label={draft.kind === "bgm" ? t("settings.connections.bgmAddress") : t("settings.connections.siteAddress")} value={draft.base_url} placeholder={PROVIDER_DEFAULT_URL[draft.kind]} onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} />
              </Field>
              <p className="settings-connections-description text-caption">
                {draft.kind === "bgm" ? t("settings.connections.bgmHint") : t("settings.connections.siteHint")}
              </p>
            </div>
          )}

          {draft.kind === "rss_http" && (
            <p className="settings-connections-field-gap-description text-caption">{t("settings.connections.rssFormNote")}</p>
          )}

          <div className="settings-connections-field-gap settings-connections-form">
            <Field label={t("settings.connections.proxyLabel")}>
              <Select label={t("settings.connections.proxyLabel")} value={draft.route_id} options={[
                  { value: "", label: t("settings.connections.proxyDirect") },
                  ...(routes.data ?? []).map((route) => ({ value: route.id, label: route.name })),
                ]} onChange={(routeID) => setDraft({ ...draft, route_id: routeID })} />
            </Field>
            <Checkbox className="tw:mt-5" checked={draft.enabled} onCheckedChange={(checked) => setDraft({ ...draft, enabled: checked })}>
              {t("settings.connections.enable")}
            </Checkbox>
          </div>
        </SettingsEditDialog>
      )}
    </section>
  );
}

function RoutesCard() {
  const routes = useQuery({ queryKey: configKeys.networkRoutes, queryFn: listNetworkRoutes });
  const {
    draft,
    setDraft,
    savedDraft,
    busy,
    error,
    setError,
    conflict,
    openEditor,
    closeEditor,
    save,
    refreshAfterConflict,
  } = useEntityDraftEditor<RouteDraft, NetworkRouteInput>({
    submit: (current) => {
      const secret = routeSecretFor(current);
      const username = secret?.mode === "replace" ? current.username.trim() : undefined;
      let input: NetworkRouteInput;
      if (current.kind === "direct") {
        input = { kind: "direct", name: current.name.trim(), enabled: current.enabled };
      } else if (current.kind === "http_proxy") {
        input = {
          kind: "http_proxy",
          name: current.name.trim(),
          enabled: current.enabled,
          proxy_url: current.endpoint.trim(),
          username,
          secret,
        };
      } else {
        const parsed = parseSocks5URL(current.endpoint);
        if (!parsed) throw new RouteDraftError(t("settings.proxy.socksInvalid"));
        input = {
          kind: "socks5",
          name: current.name.trim(),
          enabled: current.enabled,
          host: parsed.host,
          port: parsed.port,
          username,
          secret,
        };
      }
      return putNetworkRoute(current.id ?? newUUID(), input, current.version);
    },
    successMessage: t("settings.proxy.saved"),
    failureFallback: t("settings.proxy.saveFailed"),
    invalidateQueryKey: configKeys.networkRoutes,
  });
  const [testing, setTesting] = useState<string>();
  const [testResults, setTestResults] = useState<Record<string, { health: "ready" | "degraded" | "failed"; text?: string }>>({});

  // A legacy-path guard: the save button is already disabled by routeEndpointError for a bad SOCKS5
//   address, and the fallback here stays unchanged — a local validation failure only sets the error
//   hint, sends no request and never counts as a save failure.
  async function saveRoute() {
    try {
      await save();
    } catch (cause) {
      if (cause instanceof RouteDraftError) {
        setError(cause.message);
      } else {
        throw cause;
      }
    }
  }

  async function test(item: NetworkRoute) {
    setTesting(item.id);
    try {
      const result = await testNetworkRoute(item.id);
      setTestResults((prev) => ({
        ...prev,
        [item.id]: { health: result.health, text: reasonLabel(result.reason_code) },
      }));
    } catch (cause) {
      setTestResults((prev) => ({
        ...prev,
        [item.id]: { health: "failed", text: toUserMessage(cause, t("settings.connections.testFailed")) },
      }));
    } finally {
      setTesting(undefined);
    }
  }

  const credentialCheck = draft ? routeCredentialCheck(draft) : { incomplete: false };

  return (
    <section className="settings-connections-section">
      <div className="settings-connections-header">
        <h2 className="settings-connections-title text-title">{t("settings.proxy.title")}</h2>
        <Button variant="primary" size="sm" onClick={() => openEditor(routeDraftFrom())}>
          <Plus size={14} />
          {t("settings.proxy.new")}
        </Button>
      </div>

      {routes.isLoading && (
        <StateBlock kind="busy" title={t("settings.proxy.loading")} />
      )}
      {routes.error && <Alert tone="error" title={toUserMessage(routes.error, t("settings.proxy.loadFailed"))} />}
      {routes.data && routes.data.length === 0 && !draft && <StateBlock kind="empty" title={t("settings.proxy.empty")} />}

      <ul className="settings-connections-list">
        {(routes.data ?? []).map((item) => (
          <li key={item.id} className="settings-connections-item">
            <div className="settings-connections-item-row">
              <span className="settings-connections-item-title text-body">{item.name}</span>
              <Chip>
                {item.kind === "direct" ? t("settings.proxy.direct") : item.kind === "http_proxy" ? t("settings.proxy.http") : t("settings.proxy.socks5")}
              </Chip>
              <StatePill tone={item.enabled ? "online" : "muted"}>{item.enabled ? t("common.enabled") : t("common.disabled")}</StatePill>
              {item.kind !== "direct" && (
                <StatePill tone={item.credential_configured ? "online" : "muted"}>
                  {item.credential_configured ? t("common.credentialSet") : t("common.credentialUnset")}
                </StatePill>
              )}
              {testResults[item.id] && (
                <StatePill tone={testHealthTone(testResults[item.id]!.health)}>
                  {testHealthLabel(testResults[item.id]!.health)}
                </StatePill>
              )}
              <span className="settings-connections-spacer" />
              <Button variant="ghost" aria-label={t("settings.connections.edit")} size="sm" onClick={() => openEditor(routeDraftFrom(item))}>
                <Pencil size={14} />
                {t("settings.connections.edit")}
              </Button>
              <Button aria-label={testing === item.id ? t("settings.connections.testing") : t("settings.connections.test")} size="sm" disabled={testing === item.id} onClick={() => void test(item)}>
                {testing === item.id ? t("settings.connections.testing") : t("settings.connections.test")}
              </Button>
            </div>
            {testResults[item.id]?.text && (
              <p className="settings-connections-description text-caption">{testResults[item.id]!.text}</p>
            )}
          </li>
        ))}
      </ul>

      {draft && (
        <SettingsEditDialog
          open
          onOpenChange={(o) => { if (!o) closeEditor(); }}
          busy={busy}
          title={draft.id ? t("settings.proxy.editTitle") : t("settings.proxy.newTitle")}
          dirty={() => !!draft && !valuesEqual(draft, savedDraft)}
          footer={(requestClose) => (
            <>
              <Button variant="ghost" disabled={busy} onClick={requestClose}>{t("settings.connections.cancel")}</Button>
              <Button variant="primary" type="submit" form="route-form" aria-label={t("settings.connections.save")} disabled={busy || routeDraftInvalid(draft)}>
                {busy ? t("settings.connections.saving") : t("settings.connections.save")}
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
            id="route-form"
            className="tw:grid tw:grid-cols-2 tw:items-start tw:gap-3 tw:phone:grid-cols-1"
            onSubmit={(event) => {
              event.preventDefault();
              if (!busy && !routeDraftInvalid(draft)) void saveRoute();
            }}
          >
            <Field label={t("settings.connections.typeLabel")}>
              <Select label={t("settings.connections.typeLabel")} value={draft.kind} disabled={!!draft.id} options={[
                  { value: "direct", label: t("settings.proxy.direct") },
                  { value: "http_proxy", label: t("settings.proxy.http") },
                  { value: "socks5", label: t("settings.proxy.socks5") },
                ]} onChange={(kind) => setDraft({ ...draft, kind: kind as RouteDraft["kind"] })} />
            </Field>
            <Field label={t("settings.connections.nameLabel")}>
              <Input aria-label={t("settings.connections.nameLabel")} maxLength={NAME_MAX} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </Field>

            {(draft.kind === "http_proxy" || draft.kind === "socks5") && (
              <>
                <Field
                  label={t("settings.proxy.addressLabel")}
                  className="tw:col-span-2 tw:phone:col-span-1"
                  hint={
                    draft.endpoint.trim() !== "" && routeEndpointError(draft.kind, draft.endpoint) ? (
                      <span className="settings-connections-error">{routeEndpointError(draft.kind, draft.endpoint)}</span>
                    ) : draft.kind === "http_proxy" ? (
                      t("settings.proxy.httpHint")
                    ) : (
                      t("settings.proxy.socksHint")
                    )
                  }
                >
                  <Input
                    aria-label={t("settings.proxy.addressLabel")}
                    value={draft.endpoint}
                    placeholder={draft.kind === "http_proxy" ? "http://proxy.local:7890" : "socks5://proxy.local:1080"}
                    onChange={(e) => setDraft({ ...draft, endpoint: e.target.value })}
                  />
                </Field>
                {draft.credential_configured && (
                  <SecretField
                    className="tw:col-span-2 tw:phone:col-span-1"
                    editing
                    mode={draft.secret_mode}
                    onModeChange={(mode) => setDraft({ ...draft, secret_mode: mode })}
                    subject={t("settings.proxy.credentialSubject")}
                    replaceHint={t("settings.proxy.replaceHint")}
                  />
                )}
                <Field label={t("settings.proxy.usernameLabel")} hint={credentialCheck.usernameError && <span className="settings-connections-error">{credentialCheck.usernameError}</span>}>
                  <Input aria-label={t("settings.proxy.usernameLabel")} maxLength={USERNAME_MAX} value={draft.username} disabled={!routeCredentialInputsActive(draft)} onChange={(e) => setDraft({ ...draft, username: e.target.value })} />
                </Field>
                {routeCredentialInputsActive(draft) && (
                  <Field label={t("settings.proxy.passwordLabel")} hint={credentialCheck.passwordError && <span className="settings-connections-error">{credentialCheck.passwordError}</span>}>
                    <Input type="password" aria-label={t("settings.proxy.passwordLabel")} maxLength={SECRET_MAX} value={draft.password} onChange={(e) => setDraft({ ...draft, password: e.target.value })} />
                  </Field>
                )}
              </>
            )}

            <Checkbox className="tw:col-span-2 tw:phone:col-span-1" checked={draft.enabled} onCheckedChange={(checked) => setDraft({ ...draft, enabled: checked })}>
              {t("settings.connections.enable")}
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
    <label className={`settings-connections-field${className ? ` ${className}` : ""} text-caption`}>
      <span>{label}</span>
      {children}
      {hint && <span className="settings-connections-field-hint text-caption">{hint}</span>}
    </label>
  );
}
