import type { components, operations } from "./generated";
import { describeError, rememberErrorDetails } from "../errors";

// Typed API client. `generated.ts` is the only type source; every
// product request goes through the typed functions below and `/api/v1`.
// Session tokens are supplied by the shell through a registered provider so
// this module stays free of storage/routing concerns.

export type Schema<Name extends keyof components["schemas"]> = components["schemas"][Name];

export type OperationRequest<Name extends keyof operations> =
  operations[Name] extends {
    requestBody: { content: { "application/json": infer Body } };
  }
    ? Body
    : never;

type SuccessStatus = 200 | 201 | 202 | 204;

type JsonResponse<Response> = Response extends {
  content: { "application/json": infer Body };
}
  ? Body
  : never;

export type OperationResponse<Name extends keyof operations> =
  operations[Name] extends { responses: infer Responses }
    ? JsonResponse<Responses[Extract<keyof Responses, SuccessStatus>]>
    : never;

// --- Schema aliases used by workspaces -------------------------------------

export type ID = Schema<"ID">;
export type Version = Schema<"Version">;
export type SystemHealth = Schema<"SystemHealth">;
export type ReadinessReport = Schema<"ReadinessReport">;
export type DeploymentIdentity = Schema<"DeploymentIdentity">;
export type BootstrapStatus = Schema<"BootstrapStatus">;
export type LoginResult = Schema<"LoginResult">;
export type AuthSession = Schema<"AuthSession">;
export type OwnerAccount = Schema<"OwnerAccount">;
export type AccountUpdateInput = Schema<"AccountUpdateInput">;
export type AccountUpdateResult = Schema<"AccountUpdateResult">;
export type ApiToken = Schema<"ApiToken">;
export type ApiTokenScope = Schema<"ApiTokenScope">;
export type ApiTokenCreateResult = Schema<"ApiTokenCreateResult">;
export type ApiTokenCreateInput = Schema<"ApiTokenCreateInput">;
export type ApiTokenRaw = Schema<"ApiTokenRaw">;
export type Connection = Schema<"Connection">;
export type ConnectionInput = Schema<"ConnectionInput">;
export type QBittorrentConnectionInput = Schema<"QBittorrentConnectionInput">;
export type RSSHTTPConnectionInput = Schema<"RSSHTTPConnectionInput">;
export type TMDBConnectionInput = Schema<"TMDBConnectionInput">;
export type AniBTConnectionInput = Schema<"AniBTConnectionInput">;
export type MikanConnectionInput = Schema<"MikanConnectionInput">;
export type ConnectionTestResult = Schema<"ConnectionTestResult">;
export type QBittorrentTorrent = Schema<"QBittorrentTorrent">;
export type QBittorrentTorrentFile = Schema<"QBittorrentTorrentFile">;
export type QBittorrentServerStatus = Schema<"QBittorrentServerStatus">;
export type NetworkRoute = Schema<"NetworkRoute">;
export type NetworkRouteInput = Schema<"NetworkRouteInput">;
export type NetworkRouteTestResult = Schema<"NetworkRouteTestResult">;
export type ManagedAnime = Schema<"ManagedAnime">;
export type Subscription = Schema<"Subscription">;
export type SubscriptionOrigin = Schema<"SubscriptionOrigin">;
export type AnimeListItem = Schema<"AnimeListItem">;
export type AnimeMetadata = Schema<"AnimeMetadata">;
export type AnimeMetadataDraft = Schema<"AnimeMetadataDraft">;
export type ReleaseFormat = Schema<"ReleaseFormat">;
export type AnimeMetadataPatch = Schema<"AnimeMetadataPatch">;
export type AnimeMetadataPreview = Schema<"AnimeMetadataPreview">;
export type MetadataMatchInput = Schema<"MetadataMatchInput">;
export type MetadataMatchResult = Schema<"MetadataMatchResult">;
export type BGMBinding = Schema<"BGMBinding">;
export type TMDBBinding = Schema<"TMDBBinding">;
export type MetadataHold = Schema<"MetadataHold">;
export type RSSSource = Schema<"RSSSource">;
export type RSSSourceDraft = Schema<"RSSSourceDraft">;
export type RSSSourceRuntime = Schema<"RSSSourceRuntime">;
export type Release = Schema<"Release">;
export type ReleaseFile = Schema<"ReleaseFile">;
export type ReleaseTorrentStructureView = Schema<"ReleaseTorrentStructureView">;
export type ReleaseTorrentStructureFile = Schema<"ReleaseTorrentStructureFile">;
export type ParsedReleaseFacts = Schema<"ParsedReleaseFacts">;
export type ReleaseVerdict = Schema<"ReleaseVerdict">;
export type AcquisitionTarget = Schema<"AcquisitionTarget">;
export type TargetObservation = Schema<"TargetObservation">;
export type DownloadTask = Schema<"DownloadTask">;
export type Artifact = Schema<"Artifact">;
export type ArtifactFile = Schema<"ArtifactFile">;
export type Fingerprint = Schema<"Fingerprint">;
export type FileOperation = Schema<"FileOperation">;
export type SubgroupsSection = Schema<"SubgroupsSection">;
export type SubgroupsPatch = Schema<"SubgroupsPatch">;
export type SubgroupsOwnerResult = Schema<"SubgroupsOwnerResult">;
export type DiscoverSubgroupsInput = Schema<"DiscoverSubgroupsInput">;
export type DiscoverSubgroupsResult = Schema<"DiscoverSubgroupsResult">;
export type ProviderGroupSelection = Schema<"ProviderGroupSelection">;
export type SubgroupScoreEntry = Schema<"SubgroupScoreEntry">;
export type SubgroupScoreBreakdown = Schema<"SubgroupScoreBreakdown">;
export type ProviderGroupDefault = Schema<"ProviderGroupDefault">;
export type ProviderGroupDefaults = Schema<"ProviderGroupDefaults">;
export type NameKeywordRule = Schema<"NameKeywordRule">;
export type ProviderNameKeywordRules = Schema<"ProviderNameKeywordRules">;
export type GroupAction = Schema<"GroupAction">;
export type RulesDraft = Schema<"RulesDraft">;
export type RulesSections = Schema<"RulesSections">;
export type TagFilterRow = Schema<"TagFilterRow">;
export type TagFilterRules = Schema<"TagFilterRules">;
export type TagFilterRulesOverride = Schema<"TagFilterRulesOverride">;
export type DownloadCompletionAction = Schema<"DownloadCompletionAction">;
export type DownloadCompletionRules = Schema<"DownloadCompletionRules">;
export type DownloadCompletionSeedLimits = Schema<"DownloadCompletionSeedLimits">;
export type TitleTagVocabulary = Schema<"TitleTagVocabulary">;
export type RulesPatch = Schema<"RulesPatch">;
export type AnimeRules = Schema<"AnimeRules">;
export type EffectiveRules = Schema<"EffectiveRules">;
export type ParserRules = Schema<"ParserRules">;
export type ReleaseFilterRules = Schema<"ReleaseFilterRules">;
export type FileFilterRules = Schema<"FileFilterRules">;
export type NamingRules = Schema<"NamingRules">;
export type FileEffectRules = Schema<"FileEffectRules">;
export type AnimeDraft = Schema<"AnimeDraft">;
export type PreviewFeedSnapshotRef = Schema<"PreviewFeedSnapshotRef">;
export type RSSListView = Schema<"RSSListView">;
export type RSSListViewInput = Schema<"RSSListViewInput">;
export type SubgroupsSelectionPreviewInput = Schema<"SubgroupsSelectionPreviewInput">;
export type SubgroupsSelectionPreview = Schema<"SubgroupsSelectionPreview">;
export type SubgroupSelectionFact = Schema<"SubgroupSelectionFact">;
export type RSSListGroup = Schema<"RSSListGroup">;
export type RSSListObservation = Schema<"RSSListObservation">;
export type RSSListObservationRef = Schema<"RSSListObservationRef">;
export type RSSListGroupScope = Schema<"RSSListGroupScope">;
export type RSSListGroupSelection = Schema<"RSSListGroupSelection">;
export type RSSListOffset = Schema<"RSSListOffset">;
export type RSSListFeedStatus = Schema<"RSSListFeedStatus">;
export type RSSListSeedFacts = Schema<"RSSListSeedFacts">;
export type RSSListTorrentState = Schema<"RSSListTorrentState">;
export type TorrentManifestView = Schema<"TorrentManifestView">;
export type RSSListEvaluation = Schema<"RSSListEvaluation">;
export type RSSListFile = Schema<"RSSListFile">;
export type RSSListDownloadAction = Schema<"RSSListDownloadAction">;
export type RSSListItem = Schema<"RSSListItem">;
export type RSSListViewVersions = Schema<"RSSListViewVersions">;
export type SnapshotSeedDownloadInput = Schema<"SnapshotSeedDownloadInput">;
export type SnapshotSeedExpandInput = Schema<"SnapshotSeedExpandInput">;
export type SnapshotSeedExpandResult = Schema<"SnapshotSeedExpandResult">;
export type SubscribeResult = Schema<"SubscribeResult">;
export type AnimeConfigCandidate = Schema<"AnimeConfigCandidate">;
export type SubscriptionPatch = Schema<"SubscriptionPatch">;
export type NewReleasesOnlyInput = Schema<"NewReleasesOnlyInput">;
export type SubscriptionDelete = Schema<"SubscriptionDelete">;
export type SubscriptionDuplicateInput = Schema<"SubscriptionDuplicateInput">;
export type LocalDataCleanup = Schema<"LocalDataCleanup">;
export type ReleaseRetryResult = Schema<"ReleaseRetryResult">;
export type ReleaseReevaluationResult = Schema<"ReleaseReevaluationResult">;
export type CandidateSubscribeInput = Schema<"CandidateSubscribeInput">;
export type ManualRetryInput = Schema<"ManualRetryInput">;
export type ManualRetryResult = Schema<"ManualRetryResult">;
export type PlacementHandoffView = Schema<"PlacementHandoffView">;
export type AnimeWorkspace = Schema<"AnimeWorkspace">;
export type SubscriptionLogEntry = Schema<"SubscriptionLogEntry">;
export type RSSSourceOffsetPatch = Schema<"RSSSourceOffsetPatch">;
export type RSSRefreshAccepted = Schema<"RSSRefreshAccepted">;
export type AutomationSchedule = Schema<"AutomationSchedule">;
export type AutomationSchedulePatch = Schema<"AutomationSchedulePatch">;
export type AutomationRunAccepted = Schema<"AutomationRunAccepted">;
export type AutomationDispatchRun = Schema<"AutomationDispatchRun">;
export type UpdateAdmitInput = Schema<"UpdateAdmitInput">;
export type UpdateCheck = Schema<"UpdateCheck">;
export type EmbeddedUpdateStatus = Schema<"EmbeddedUpdateStatus">;
export type BackupInfo = Schema<"BackupInfo">;
export type FeedSite = Schema<"FeedSite">;
export type FeedSiteList = Schema<"FeedSiteList">;
export type BackupRestoreAccepted = Schema<"BackupRestoreAccepted">;
export type BackupDownloadTicket = Schema<"BackupDownloadTicket">;
export type AutomationKind = Schema<"AutomationKind">;
export type AutomationIntervalUnit = Schema<"AutomationIntervalUnit">;
export type AutomationState = Schema<"AutomationState">;
export type AutomationTrigger = Schema<"AutomationTrigger">;
export type RoutineActivityKind = Schema<"RoutineActivityKind">;
export type ObservabilityRecord = Schema<"ObservabilityRecord">;
export type ObservabilityRecordPage = Schema<"ObservabilityRecordPage">;
export type DiagnosticEpisode = Schema<"DiagnosticEpisode">;
export type DiagnosticEpisodePage = Schema<"DiagnosticEpisodePage">;
export type AcquisitionRecordSummary = Schema<"AcquisitionRecordSummary">;
export type AcquisitionRecordPage = Schema<"AcquisitionRecordPage">;
export type AcquisitionRecordDetail = Schema<"AcquisitionRecordDetail">;
export type AcquisitionRecordState = Schema<"AcquisitionRecordState">;
export type AcquisitionEntryKind = Schema<"AcquisitionEntryKind">;
export type AcquisitionRecordOrigin = Schema<"AcquisitionRecordOrigin">;
export type AcquisitionStage = Schema<"AcquisitionStage">;
export type AcquisitionTransfer = Schema<"AcquisitionTransfer">;
export type AcquisitionStageName = Schema<"AcquisitionStageName">;
export type AcquisitionStageState = Schema<"AcquisitionStageState">;
export type AcquisitionDisplayValue = Schema<"AcquisitionDisplayValue">;
export type AcquisitionFile = Schema<"AcquisitionFile">;
export type AcquisitionFilePlacementFailure = Schema<"AcquisitionFilePlacementFailure">;
export type AcquisitionImportRetryResult = Schema<"AcquisitionImportRetryResult">;
export type AcquisitionFilePage = Schema<"AcquisitionFilePage">;
export type FileManagerMount = Schema<"FileManagerMount">;
export type FileManagerMountList = Schema<"FileManagerMountList">;
export type FileManagerEntry = Schema<"FileManagerEntry">;
export type FileManagerEntryState = Schema<"FileManagerEntryState">;
export type FileManagerEntryRole = Schema<"FileManagerEntryRole">;
export type FileManagerListing = Schema<"FileManagerListing">;
export type FileManagerEntryDetail = Schema<"FileManagerEntryDetail">;
export type FileManagerLinkedFile = Schema<"FileManagerLinkedFile">;
export type FileManagerInProgressKind = Schema<"FileManagerInProgressKind">;
export type FileManagerDetachedReason = Schema<"FileManagerDetachedReason">;
export type FileManagerOperation = Schema<"FileManagerOperation">;
export type FileManagerOperationBlock = Schema<"FileManagerOperationBlock">;
export type FileManagerOperationRule = Schema<"FileManagerOperationRule">;
export type FileManagerLocation = Schema<"FileManagerLocation">;
export type FileManagerUserOperation = Schema<"FileManagerUserOperation">;
export type FileManagerConflictChoice = Schema<"FileManagerConflictChoice">;
export type FileManagerTarget = Schema<"FileManagerTarget">;
export type FileManagerOperationRequest = Schema<"FileManagerOperationRequest">;
export type FileManagerOperationState = Schema<"FileManagerOperationState">;
export type FileManagerOperationFailure = Schema<"FileManagerOperationFailure">;
export type FileManagerOperationView = Schema<"FileManagerOperationView">;
export type FileManagerReleaseRequest = Schema<"FileManagerReleaseRequest">;
export type FileManagerReleaseResult = Schema<"FileManagerReleaseResult">;
export type FileManagerAlignRequest = Schema<"FileManagerAlignRequest">;
export type FileManagerAlignResult = Schema<"FileManagerAlignResult">;
export type FileManagerMatchPreviewRequest = Schema<"FileManagerMatchPreviewRequest">;
export type FileManagerMatchPreview = Schema<"FileManagerMatchPreview">;
export type FileManagerMatchBlocker = Schema<"FileManagerMatchBlocker">;
export type FileManagerMatchRequest = Schema<"FileManagerMatchRequest">;
export type TMDBSeason = Schema<"TMDBSeason">;
export type TMDBSeasonList = Schema<"TMDBSeasonList">;
export type TMDBEpisode = Schema<"TMDBEpisode">;
export type TMDBSeasonEpisodeList = Schema<"TMDBSeasonEpisodeList">;
export type GlobalSectionName = Schema<"GlobalSectionName">;
export type GlobalSectionView = Schema<"GlobalSectionView">;
export type GlobalSectionInput = Schema<"GlobalSectionInput">;
export type DownloadTimeouts = Schema<"DownloadTimeouts">;
export type NamingTemplate = Schema<"NamingTemplate">;
export type NamingTemplateLibraryView = Schema<"NamingTemplateLibraryView">;
export type NamingTemplateLibraryInput = Schema<"NamingTemplateLibraryInput">;
export type ErrorDetail = Schema<"ErrorDetail">;

// --- Metadata search -----------------------------------------------------------

export type MetadataSearchResult = Schema<"MetadataSearchResult">;
export type MetadataSearchCandidate = Schema<"MetadataSearchCandidate">;

export function searchMetadata(
  provider: "bgm" | "tmdb",
  query: string,
  limit?: number,
): Promise<MetadataSearchResult> {
  const params = new URLSearchParams({ provider, query });
  if (limit !== undefined) params.set("limit", String(limit));
  return request(`/metadata/search?${params.toString()}`, { method: "GET" });
}

// --- Errors ----------------------------------------------------------------

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly requestId?: string;
  readonly currentVersion?: number;
  readonly details?: ErrorDetail[];
  /** Present on 429 responses that carry a Retry-After header. */
  readonly retryAfterSeconds?: number;

  constructor(input: {
    status: number;
    message: string;
    code?: string;
    requestId?: string;
    currentVersion?: number;
    details?: ErrorDetail[];
    retryAfterSeconds?: number;
  }) {
    super(input.message);
    this.name = "ApiError";
    this.status = input.status;
    this.code = input.code;
    this.requestId = input.requestId;
    this.currentVersion = input.currentVersion;
    this.details = input.details;
    this.retryAfterSeconds = input.retryAfterSeconds;
  }
}

export function isVersionConflict(cause: unknown): cause is ApiError {
  return cause instanceof ApiError && cause.status === 409 && cause.currentVersion !== undefined;
}

export function toUserMessage(cause: unknown, fallback: string): string {
  const info = describeError(cause instanceof ApiError ? cause : undefined, cause, fallback);
  rememberErrorDetails(info.message, info.details);
  return info.message;
}

// --- Session plumbing --------------------------------------------------------

let tokenProvider: () => string = () => "";
let sessionPresenceProvider: () => boolean = () => false;
let csrfTokenProvider: () => string = () => "";
let unauthorizedHandler: (() => void) | undefined;

/** Registered by the shell so an explicitly-supplied bearer (a test or an
 *  embedded client) can still authenticate. The product browser session never
 *  uses this: it rides the HttpOnly cookie. */
export function setSessionTokenProvider(provider: () => string): void {
  tokenProvider = provider;
}

/** Registered by the shell: is a session believed open? Used only to decide
 *  whether a 401 means "the session died" or "you were never signed in". */
export function setSessionPresenceProvider(provider: () => boolean): void {
  sessionPresenceProvider = provider;
}

/** Registered by the shell: returns the double-submit value to echo in
 *  X-CSRF-Token on unsafe requests. */
export function setCsrfTokenProvider(provider: () => string): void {
  csrfTokenProvider = provider;
}

/** Called when an authenticated request is rejected as unauthorized. */
export function setUnauthorizedHandler(handler: () => void): void {
  unauthorizedHandler = handler;
}

// --- Transport ---------------------------------------------------------------

const apiRoot = "/api/v1";

/**
 * Generates UUID-like request/resource IDs in both secure and plain HTTP
 * browser contexts. `crypto.randomUUID()` is unavailable on LAN pages served
 * over plain HTTP, while `getRandomValues()` remains available there.
 */
export function newUUID(): string {
  const webCrypto = globalThis.crypto;
  if (typeof webCrypto?.randomUUID === "function") {
    return webCrypto.randomUUID();
  }
  if (typeof webCrypto?.getRandomValues === "function") {
    const bytes = webCrypto.getRandomValues(new Uint8Array(16));
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  return `fallback-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`;
}

function encodePathSegment(value: string): string {
  return encodeURIComponent(value);
}

type RequestOptions = {
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  bearerToken?: string;
  idempotencyKey?: string;
  ifMatchVersion?: number;
  acceptStatuses?: readonly number[];
  /** "text" returns the success body as a string instead of parsing JSON. */
  responseType?: "json" | "text";
};

function readErrorPayload(payload: unknown): Partial<Schema<"ErrorResponse">> {
  if (!payload || typeof payload !== "object") {
    return {};
  }
  const record = payload as Record<string, unknown>;
  return {
    code: typeof record.code === "string" ? record.code : undefined,
    message: typeof record.message === "string" ? record.message : undefined,
    request_id: typeof record.request_id === "string" ? record.request_id : undefined,
    current_version:
      typeof record.current_version === "number" ? record.current_version : undefined,
    details: Array.isArray(record.details)
      ? record.details.filter(
          (detail): detail is ErrorDetail =>
            !!detail && typeof detail === "object" && typeof (detail as ErrorDetail).message === "string",
        )
      : undefined,
  };
}

function readRetryAfterSeconds(value: string | null): number | undefined {
  if (!value) return undefined;
  const seconds = Number.parseInt(value, 10);
  return Number.isFinite(seconds) && seconds > 0 ? seconds : undefined;
}

async function request<Result>(path: string, options: RequestOptions): Promise<Result> {
  const headers = new Headers();
  if (options.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const token = options.bearerToken ?? tokenProvider();
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  applyCsrfHeader(headers, options.method);
  if (options.idempotencyKey) {
    headers.set("Idempotency-Key", options.idempotencyKey);
  }
  if (options.ifMatchVersion !== undefined) {
    headers.set("If-Match", String(options.ifMatchVersion));
  }

  let response: Response;
  try {
    response = await fetch(`${apiRoot}${path}`, {
      method: options.method,
      headers,
      // The browser session is an HttpOnly cookie, so the request must carry
      // same-origin credentials or the cookie is never attached. A bearer
      // caller is unaffected: it authenticated through its own header.
      credentials: "same-origin",
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    });
  } catch (cause) {
    throw new ApiError({
      status: 0,
      code: "NETWORK_ERROR",
      message: cause instanceof Error ? cause.message : "无法连接到服务",
    });
  }

  if (options.responseType === "text" && response.ok) {
    return (await response.text()) as Result;
  }
  const payload = response.status === 204 ? undefined : await response.json().catch(() => undefined);
  if (!response.ok) {
    if (options.acceptStatuses?.includes(response.status)) {
      return payload as Result;
    }
    const error = readErrorPayload(payload);
    if (response.status === 401 && (token || sessionPresenceProvider())) {
      unauthorizedHandler?.();
    }
    throw new ApiError({
      status: response.status,
      code: error.code,
      message: error.message ?? `请求失败 (${response.status})`,
      requestId: error.request_id,
      currentVersion: error.current_version,
      details: error.details,
      retryAfterSeconds: readRetryAfterSeconds(response.headers.get("Retry-After")),
    });
  }

  return payload as Result;
}

/** Attaches the double-submit token on a state-changing request. The server
 *  only checks it when the credential came from the cookie, so a bearer
 *  caller never needs it; sending it anyway is harmless and keeps this
 *  transport free of "which credential am I using" bookkeeping. */
function applyCsrfHeader(headers: Headers, method: string): void {
  if (method === "GET") return;
  const csrf = csrfTokenProvider();
  if (csrf) headers.set("X-CSRF-Token", csrf);
}

/** Fetches a same-origin API asset (poster images) as an object URL. <img>
 *  elements cannot attach the bearer token, and an authenticated asset must
 *  ride the session the same way the rest of the client does, so this sends
 *  same-origin credentials and hands the element a blob. The shared
 *  authenticated-image cache owns the returned URL lifecycle. */
export async function fetchAuthedAssetObjectUrl(assetPath: string): Promise<string> {
  const headers = new Headers();
  const token = tokenProvider();
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  const path = assetPath.startsWith(apiRoot) ? assetPath.slice(apiRoot.length) : assetPath;
  let response: Response;
  try {
    response = await fetch(`${apiRoot}${path}`, { headers, credentials: "same-origin" });
  } catch (cause) {
    throw new ApiError({
      status: 0,
      code: "NETWORK_ERROR",
      message: cause instanceof Error ? cause.message : "无法连接到服务",
    });
  }
  if (!response.ok) {
    if (response.status === 401 && (token || sessionPresenceProvider())) {
      unauthorizedHandler?.();
    }
    throw new ApiError({ status: response.status, message: `资源加载失败 (${response.status})` });
  }
  return URL.createObjectURL(await response.blob());
}

// --- System / bootstrap / auth ----------------------------------------------

// /readyz is the admission surface, but its optional deployment_identity
// section is the combined identity view the version-and-upgrade page renders
// (process build id comes from BuildInfo-backed checks; the identity section
// reports source/release/payload layers). Public endpoint, no auth required.
export function getReadyz(): Promise<ReadinessReport> {
  // /readyz uses HTTP 503 as the hard admission verdict, but its JSON body is
  // the useful diagnostic read model. Preserve that body for the settings
  // page instead of turning a structured readiness failure into a generic
  // request error.
  return request("/readyz", { method: "GET", acceptStatuses: [503] });
}

export function getBootstrapStatus(): Promise<BootstrapStatus> {
  return request("/bootstrap/status", { method: "GET" });
}

// Fresh instances create the owner directly on first visit; there is no
// bootstrap grant or setup session. The body stays a local explicit type so
// this module does not depend on generated types the backend lane removes.
// The transport is spelled out because this call IS the browser: asking for
// the cookie is what keeps the new credential out of script reach.
export type CreateBootstrapOwnerInput = {
  username: string;
  password: string;
};

export function createBootstrapOwner(input: CreateBootstrapOwnerInput): Promise<LoginResult> {
  return request("/bootstrap/owner", {
    method: "POST",
    body: { ...input, session_transport: "cookie" },
  });
}

/** Reports the session the request is authenticated with, or 401. The cookie
 *  transport means the page has no other way to know whether it is signed in:
 *  the credential is not readable, so only the server can say. */
export function getAuthSession(): Promise<AuthSession> {
  return request("/auth/session", { method: "GET" });
}

export function login(input: OperationRequest<"login">): Promise<LoginResult> {
  return request("/auth/login", { method: "POST", body: input });
}

/** Change the signed-in owner's username and/or password. The current
 *  password is always required; a password change revokes other sessions. */
export function updateAccount(input: OperationRequest<"updateAccount">): Promise<AccountUpdateResult> {
  return request("/auth/account", { method: "PATCH", body: input });
}

export function logout(): Promise<void> {
  return request("/auth/logout", { method: "POST" });
}

// --- Connections / network routes -------------------------------------------

export function listConnections(): Promise<Connection[]> {
  return request("/config/connections", { method: "GET" });
}

export function putConnection(
  connectionID: string,
  input: ConnectionInput,
  expectedVersion: number,
): Promise<Connection> {
  return request(`/config/connections/${encodePathSegment(connectionID)}`, {
    method: "PUT",
    body: input,
    ifMatchVersion: expectedVersion,
  });
}

export function testConnection(connectionID: string): Promise<ConnectionTestResult> {
  return request(`/config/connections/${encodePathSegment(connectionID)}/test`, { method: "POST" });
}

export function listNetworkRoutes(): Promise<NetworkRoute[]> {
  return request("/config/network-routes", { method: "GET" });
}

export function putNetworkRoute(
  routeID: string,
  input: NetworkRouteInput,
  expectedVersion: number,
): Promise<NetworkRoute> {
  return request(`/config/network-routes/${encodePathSegment(routeID)}`, {
    method: "PUT",
    body: input,
    ifMatchVersion: expectedVersion,
  });
}

export function testNetworkRoute(routeID: string): Promise<NetworkRouteTestResult> {
  return request(`/config/network-routes/${encodePathSegment(routeID)}/test`, { method: "POST" });
}

// --- qBittorrent management (operational passthrough) ------------------------

export function listQBittorrentTorrents(connectionID: string): Promise<QBittorrentTorrent[]> {
  return request(`/config/connections/${encodePathSegment(connectionID)}/qbittorrent/torrents`, { method: "GET" });
}

export function pauseQBittorrentTorrent(connectionID: string, hash: string): Promise<void> {
  return request(`/config/connections/${encodePathSegment(connectionID)}/qbittorrent/torrents/${encodePathSegment(hash)}/pause`, { method: "POST" });
}

export function resumeQBittorrentTorrent(connectionID: string, hash: string): Promise<void> {
  return request(`/config/connections/${encodePathSegment(connectionID)}/qbittorrent/torrents/${encodePathSegment(hash)}/resume`, { method: "POST" });
}

export function deleteQBittorrentTorrent(connectionID: string, hash: string, deleteFiles: boolean): Promise<void> {
  const query = deleteFiles ? "?delete_files=true" : "";
  return request(`/config/connections/${encodePathSegment(connectionID)}/qbittorrent/torrents/${encodePathSegment(hash)}${query}`, { method: "DELETE" });
}

export function getQBittorrentTorrentFiles(connectionID: string, hash: string): Promise<QBittorrentTorrentFile[]> {
  return request(`/config/connections/${encodePathSegment(connectionID)}/qbittorrent/torrents/${encodePathSegment(hash)}/files`, { method: "GET" });
}

export function getQBittorrentServerStatus(connectionID: string): Promise<QBittorrentServerStatus> {
  return request(`/config/connections/${encodePathSegment(connectionID)}/qbittorrent/status`, { method: "GET" });
}

// --- Anime catalog / draft / subscribe ---------------------------------------

export function listAnime(): Promise<AnimeListItem[]> {
  return request("/anime", { method: "GET" });
}

/**
 * Reads one unified RSS list read model: subgroups → seeds → torrent file tree.
 * Pure read: triggers no provider fetch and writes nothing. Provider sources must carry
 * the exact feed snapshot identity per group (snapshot_id + snapshot_version +
 * content_hash); a selected subgroup without an identity is judged refresh_required by the
 * server instead of degrading into "the latest snapshot".
 */
export function readAnimeRSSListView(input: RSSListViewInput): Promise<RSSListView> {
  return request("/anime/rss-list-view", { method: "POST", body: input });
}

/**
 * Previews which groups an unsaved subgroup value would select: the full draft before
 * subscribing (current_input), the owner's full unsaved subgroups after (owner_draft).
 * Computed by the server's same selection decision, a pure read; groups the server has no
 * facts about never appear in the result.
 */
export function previewAnimeSubgroupsSelection(input: SubgroupsSelectionPreviewInput): Promise<SubgroupsSelectionPreview> {
  return request("/anime/subgroups-selection-preview", { method: "POST", body: input });
}

/**
 * Starts a one-shot download of one seed in a persisted snapshot. The request carries only
 * identity: the locator, title, publish time and strong content identity are all read by the
 * server from that snapshot, and the torrent bytes come from the same persisted blob, so
 * "the bytes the user viewed" and "the bytes the downloader receives" are identical and the
 * click never visits the provider again.
 */
export function downloadSnapshotSeed(
  snapshotID: string,
  stableSeedKey: string,
  input: SnapshotSeedDownloadInput,
  idempotencyKey: string,
): Promise<OneShotAdmissionResult> {
  return request(
    `/provider-feed-snapshots/${encodePathSegment(snapshotID)}/seeds/${encodePathSegment(stableSeedKey)}/download`,
    { method: "POST", body: input, idempotencyKey },
  );
}

/**
 * Expands one seed in a persisted snapshot: the server fetches the torrent once by the
 * locator the snapshot published, verifies it and writes it into the long-term cache. Like
 * the download, the request carries only identity; the frontend takes no part in judging the
 * locator or the bytes.
 *
 * Naturally idempotent (the cache keys on the locator), so no Idempotency-Key. The returned
 * manifest is a summary only, without per-file rows or target paths: getting the file list
 * takes one more read of the read model after expanding.
 */
export function expandSnapshotSeed(
  snapshotID: string,
  stableSeedKey: string,
  input: SnapshotSeedExpandInput,
): Promise<SnapshotSeedExpandResult> {
  return request(
    `/provider-feed-snapshots/${encodePathSegment(snapshotID)}/seeds/${encodePathSegment(stableSeedKey)}/expand`,
    { method: "POST", body: input },
  );
}

export function subscribeAnime(draft: AnimeDraft, idempotencyKey: string): Promise<SubscribeResult> {
  return request("/anime/subscribe", { method: "POST", body: { draft }, idempotencyKey });
}

// --- Anime workspace owners ----------------------------------------------------

export function getAnimeWorkspace(managedAnimeID: string): Promise<AnimeWorkspace> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/workspace`, { method: "GET" });
}

/**
 * Source snapshot fetch for a not-yet-started custom-source subscription: the source is read
 * from the subscription and the frontend carries only the subscription ID. The network is
 * touched only at fetch time; the returned snapshot reference is then handed to the owner's
 * RSS preview read.
 */
export function fetchOwnerCustomSnapshot(managedAnimeID: string): Promise<ProviderRSSPreviewResult> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/custom-feed-snapshots:fetch`, { method: "POST" });
}

export function patchAnimeMetadata(
  managedAnimeID: string,
  patch: AnimeMetadataPatch,
): Promise<AnimeMetadata> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/metadata`, {
    method: "PATCH",
    body: patch,
  });
}

/** Re-pull the bound works bypassing the positive caches (metadata refresh). */
export function refreshAnimeMetadata(managedAnimeID: string): Promise<AnimeMetadata> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/metadata/refresh`, { method: "POST" });
}

export function matchAnimeMetadata(
  managedAnimeID: string,
  input: MetadataMatchInput,
  idempotencyKey: string,
): Promise<MetadataMatchResult> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/metadata/match`, {
    method: "POST",
    body: input,
    idempotencyKey,
  });
}

export function patchAnimeSubgroups(
  managedAnimeID: string,
  patch: SubgroupsPatch,
): Promise<SubgroupsOwnerResult> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/subgroups`, {
    method: "PATCH",
    body: patch,
  });
}

export function discoverAnimeSubgroups(
  managedAnimeID: string,
  input: DiscoverSubgroupsInput,
): Promise<DiscoverSubgroupsResult> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/subgroups/discover`, {
    method: "POST",
    body: input,
  });
}

export function patchAnimeRules(
  managedAnimeID: string,
  patch: RulesPatch,
): Promise<OperationResponse<"patchAnimeRules">> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/rules`, {
    method: "PATCH",
    body: patch,
  });
}


// Re-evaluation: re-queues evaluation for every unsubmitted release (submitted ones stay frozen).
export function reevaluateAnimeReleases(managedAnimeID: string): Promise<ReleaseReevaluationResult> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/releases/reevaluate`, {
    method: "POST",
  });
}

export function patchAnimeSubscription(
  managedAnimeID: string,
  patch: SubscriptionPatch,
): Promise<Subscription> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/subscription`, {
    method: "PATCH",
    body: patch,
  });
}

/** Fetch new seeds only: changeable only before the start; afterwards the server rejects with NEW_RELEASES_ONLY_LOCKED. */
export function setAnimeNewReleasesOnly(
  managedAnimeID: string,
  input: NewReleasesOnlyInput,
): Promise<Subscription> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/subscription/new-releases-only`, {
    method: "PUT",
    body: input,
  });
}

export function deleteAnimeSubscription(
  managedAnimeID: string,
  input: SubscriptionDelete,
): Promise<void> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/subscription`, {
    method: "DELETE",
    body: input,
  });
}

/** Duplicates a subscription: creates a not-yet-started subscription from the source
 *  instance's currently saved settings (state, history and files are not copied).
 *  A replay with the same key and body returns the first result. */
export function duplicateAnimeSubscription(
  managedAnimeID: string,
  input: SubscriptionDuplicateInput,
  idempotencyKey: string,
): Promise<SubscribeResult> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/duplicate`, {
    method: "POST",
    body: input,
    idempotencyKey,
  });
}

/** Physically cleans up the local records of a retired instance (qB data and media files are not deleted). */
export function cleanupAnimeLocalData(
  managedAnimeID: string,
  input: LocalDataCleanup,
  idempotencyKey: string,
): Promise<void> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/local-data`, {
    method: "DELETE",
    body: input,
    idempotencyKey,
  });
}

// --- Anime config candidates ----------------------------------------------------

export function listAnimeConfigCandidates(query?: {
  origin?: string;
  origin_entry_ref?: string;
}): Promise<AnimeConfigCandidate[]> {
  const params = new URLSearchParams();
  if (query?.origin) params.set("origin", query.origin);
  if (query?.origin_entry_ref) params.set("origin_entry_ref", query.origin_entry_ref);
  const suffix = params.toString();
  return request<OperationResponse<"listAnimeConfigCandidates">>(
    `/anime-config-candidates${suffix ? `?${suffix}` : ""}`,
    { method: "GET" },
  ).then((response) => response.candidates);
}

export function getAnimeConfigCandidate(candidateID: string): Promise<AnimeConfigCandidate> {
  return request(`/anime-config-candidates/${encodePathSegment(candidateID)}`, { method: "GET" });
}

export function saveAnimeConfigCandidate(draft: AnimeDraft): Promise<AnimeConfigCandidate> {
  return request("/anime-config-candidates", { method: "POST", body: { draft } });
}

export function patchAnimeConfigCandidate(
  candidateID: string,
  draft: AnimeDraft,
  expectedVersion: number,
): Promise<AnimeConfigCandidate> {
  return request(`/anime-config-candidates/${encodePathSegment(candidateID)}`, {
    method: "PATCH",
    body: { draft },
    ifMatchVersion: expectedVersion,
  });
}

export function deleteAnimeConfigCandidate(candidateID: string): Promise<void> {
  return request(`/anime-config-candidates/${encodePathSegment(candidateID)}`, { method: "DELETE" });
}

export function subscribeAnimeConfigCandidate(
  candidateID: string,
  expectedVersion: number,
  idempotencyKey: string,
  input: CandidateSubscribeInput,
): Promise<SubscribeResult> {
  return request(`/anime-config-candidates/${encodePathSegment(candidateID)}/subscribe`, {
    method: "POST",
    body: input,
    ifMatchVersion: expectedVersion,
    idempotencyKey,
  });
}

// --- Finite actions -------------------------------------------------------------

// The one-level structure of a release's retained torrent payload: the folders and files
// directly under the torrent root, with each folder's recursive size. Pure read — it never
// fetches a payload. A release whose payload was never retained, or is past its retention
// window, answers 404 TORRENT_STRUCTURE_UNAVAILABLE; callers treat that as a normal state.
export function getReleaseTorrentStructure(releaseID: string): Promise<ReleaseTorrentStructureView> {
  return request(`/releases/${encodePathSegment(releaseID)}/torrent-structure`, { method: "GET" });
}

// Retries one seed: runs the normal acceptance flow again under the current rules
// (filtering and the metadata hold are not bypassed). filtered / held / present / failed
// are outcomes, not errors; when a retry is not possible the answer is
// 409 RELEASE_NOT_RETRYABLE with the reason code in details[0].code.
export function retryRelease(releaseID: string, idempotencyKey: string): Promise<ReleaseRetryResult> {
  return request(`/releases/${encodePathSegment(releaseID)}/retry`, {
    method: "POST",
    idempotencyKey,
  });
}

// Files a scan found missing or changed: moved out of management (the files stay where they
// are, the episodes are marked as not imported). When unavailable the answer is
// 409 PLACEMENT_EXIT_UNAVAILABLE with the reason code in details[0].code.
export function releasePlacement(placementID: string, idempotencyKey: string): Promise<PlacementReleaseResult> {
  return request(`/placements/${encodePathSegment(placementID)}/release`, {
    method: "POST",
    idempotencyKey,
  });
}

export function retryRSSSourceRuntime(
  rssSourceID: string,
  input: ManualRetryInput,
  idempotencyKey: string,
): Promise<ManualRetryResult> {
  return request(`/rss-source-runtimes/${encodePathSegment(rssSourceID)}/retry`, {
    method: "POST",
    body: input,
    idempotencyKey,
  });
}

export function retryDownloadTask(
  downloadTaskID: string,
  input: ManualRetryInput,
  idempotencyKey: string,
): Promise<ManualRetryResult> {
  return request(`/download-tasks/${encodePathSegment(downloadTaskID)}/retry`, {
    method: "POST",
    body: input,
    idempotencyKey,
  });
}

export function retryArtifact(
  artifactID: string,
  input: ManualRetryInput,
  idempotencyKey: string,
): Promise<ManualRetryResult> {
  return request(`/artifacts/${encodePathSegment(artifactID)}/retry`, {
    method: "POST",
    body: input,
    idempotencyKey,
  });
}

// --- One-shot seed acquisition -------------------------------------------------
// acquireSeedOnce and the attempt reads below have no UI caller: the shipped one-shot
// dialog acquires through downloadSnapshotSeed instead. They stay (with their tests) as the
// typed client of the finite one-shot API.

export type AcquireSeedOnceInput = Schema<"AcquireSeedOnceInput">;
export type SelectedSeed = Schema<"SelectedSeed">;
export type OneShotAdmissionResult = Schema<"OneShotAdmissionResult">;
export type OneShotAcquisitionView = Schema<"OneShotAcquisitionView">;

/** A bounded one-shot seed acquisition: {candidate_id, revision, selected_seed} in one
 *  submission; the idempotency key dedupes and a replay with the same key and body returns
 *  the original handle. Previews stay pure reads; only this explicit command creates the
 *  temporary owner mount. */
export function acquireSeedOnce(
  input: AcquireSeedOnceInput,
  idempotencyKey: string,
): Promise<OneShotAdmissionResult> {
  return request("/one-shot-acquisitions", { method: "POST", body: input, idempotencyKey });
}

/** The one-shot acquisition read side: 404 = the marker has been cleaned up (the replay
 *  window ended); returns null so the caller stops polling and shows "record cleaned up";
 *  other errors throw as usual. */
export async function getOneShotAcquisition(
  oneShotID: string,
): Promise<OneShotAcquisitionView | null> {
  try {
    return await request<OneShotAcquisitionView>(
      `/one-shot-acquisitions/${encodePathSegment(oneShotID)}`,
      { method: "GET" },
    );
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 404) return null;
    throw cause;
  }
}

// --- One-shot execution attempts -----------------------------------------------
// A failed one-shot download is terminal: no retry, no re-planning; downloading again is a
// new one-shot download.

export type OneShotExecutionAttemptSummary = Schema<"OneShotExecutionAttemptSummary">;
export type OneShotExecutionAttemptPage = Schema<"OneShotExecutionAttemptPage">;
export type OneShotExecutionAttempt = Schema<"OneShotExecutionAttempt">;

/** Execution history: keyset pagination in sequence DESC order, the cursor opaque to the client. */
export function listOneShotExecutionAttempts(
  oneShotID: string,
  query: { limit?: number; cursor?: string } = {},
): Promise<OneShotExecutionAttemptPage> {
  return request(
    `/one-shot-acquisitions/${encodePathSegment(oneShotID)}/attempts${queryString(query)}`,
    { method: "GET" },
  );
}

/** The full redacted detail of one execution attempt (including input_snapshot). */
export function getOneShotExecutionAttempt(
  oneShotID: string,
  attemptID: string,
): Promise<OneShotExecutionAttempt> {
  return request(
    `/one-shot-acquisitions/${encodePathSegment(oneShotID)}/attempts/${encodePathSegment(attemptID)}`,
    { method: "GET" },
  );
}

// --- Catalog (provider browse) -----------------------------------------------

export type CatalogSnapshot = Schema<"CatalogSnapshot">;
export type CatalogItem = Schema<"CatalogItem">;
export type CatalogWorkspace = Schema<"CatalogWorkspace">;
export type CatalogPreference = Schema<"CatalogPreference">;
export type CatalogPreferenceInput = Schema<"CatalogPreferenceInput">;
export type CatalogSuppression = Schema<"CatalogSuppression">;
export type ProviderGroupDiscoveryResult = Schema<"ProviderGroupDiscoveryResult">;
export type ProviderGroupDiscoveryGroup = Schema<"ProviderGroupDiscoveryGroup">;
export type ProviderRSSPreviewInput = Schema<"ProviderRSSPreviewInput">;
export type CustomSnapshotFetchInput = Schema<"CustomSnapshotFetchInput">;

/** Explicit fetch of a saved custom candidate; URLs are resolved server-side. */
export function fetchCustomSnapshot(input: CustomSnapshotFetchInput): Promise<ProviderRSSPreviewResult> {
  return request("/catalog/custom/feed-snapshots:fetch", { method: "POST", body: input });
}
export type ProviderRSSPreviewResult = Schema<"ProviderRSSPreviewResult">;
export type ProviderRSSPreviewSeed = Schema<"ProviderRSSPreviewSeed">;

export function readCatalogWorkspace(
  provider: "anibt" | "mikan",
  season: string,
): Promise<CatalogWorkspace> {
  return request(`/catalog/providers/${encodePathSegment(provider)}/seasons/${encodePathSegment(season)}`, {
    method: "GET",
  });
}

export function searchProviderCatalog(
  provider: "anibt" | "mikan",
  q: string,
): Promise<CatalogItem[]> {
  const params = new URLSearchParams({ q });
  return request(`/catalog/providers/${encodePathSegment(provider)}/search?${params.toString()}`, {
    method: "GET",
  });
}

export type SeedSearchSnapshotInput = Schema<"SeedSearchSnapshotInput">;
export type SeedSearchSnapshotView = Schema<"SeedSearchSnapshotView">;
export type SeedSearchSnapshotSeed = Schema<"SeedSearchSnapshotSeed">;

/**
 * Runs one keyword torrent search and persists its results as a short-lived search
 * snapshot. It is a write (x-scope write): the upstream is contacted and a snapshot row
 * is appended, even though no subscription or owner is created. The returned identity
 * (snapshot_id + snapshot_version + content_hash plus the derived scope_ref/query_ref)
 * covers the complete result set; `limit` only bounds the window this response renders.
 */
export function fetchSeedSearchSnapshot(
  provider: "anibt" | "mikan",
  input: SeedSearchSnapshotInput,
): Promise<SeedSearchSnapshotView> {
  return request(`/catalog/providers/${encodePathSegment(provider)}/seed-search-snapshots:fetch`, {
    method: "POST",
    body: input,
  });
}

/**
 * Reads one window of one exact search snapshot identity. It never contacts the provider
 * and never falls back to a latest snapshot: a stale or differently-kinded identity is a
 * typed 409 refresh-required failure, which the caller surfaces as "search again".
 */
export function readSeedSearchSnapshot(
  provider: "anibt" | "mikan",
  snapshotID: string,
  params: {
    scope_ref: string;
    query_ref: string;
    snapshot_version: number;
    content_hash: string;
    cursor?: string;
    limit?: number;
  },
): Promise<SeedSearchSnapshotView> {
  const query = new URLSearchParams({
    scope_ref: params.scope_ref,
    query_ref: params.query_ref,
    snapshot_version: String(params.snapshot_version),
    content_hash: params.content_hash,
  });
  if (params.cursor !== undefined) query.set("cursor", params.cursor);
  if (params.limit !== undefined) query.set("limit", String(params.limit));
  return request(
    `/catalog/providers/${encodePathSegment(provider)}/seed-search-snapshots/${encodePathSegment(snapshotID)}?${query.toString()}`,
    { method: "GET" },
  );
}

export function refreshCatalogProvider(
  provider: "anibt" | "mikan",
  season: string,
): Promise<CatalogSnapshot> {
  return request(`/catalog/providers/${encodePathSegment(provider)}/seasons/${encodePathSegment(season)}/refresh`, {
    method: "POST",
  });
}

export function readCatalogPreference(
  provider: "anibt" | "mikan",
  season: string,
): Promise<CatalogPreference> {
  return request(`/catalog/providers/${encodePathSegment(provider)}/seasons/${encodePathSegment(season)}/preference`, {
    method: "GET",
  });
}

export function setCatalogPreference(
  provider: "anibt" | "mikan",
  season: string,
  input: CatalogPreferenceInput,
  expectedVersion: number,
): Promise<CatalogPreference> {
  return request(`/catalog/providers/${encodePathSegment(provider)}/seasons/${encodePathSegment(season)}/preference`, {
    method: "PUT",
    body: input,
    ifMatchVersion: expectedVersion,
  });
}

// Suppressions are not season-scoped: an entry stays suppressed across seasons
// until it is explicitly restored, so the list endpoint takes no season.
export function listCatalogSuppressions(): Promise<CatalogSuppression[]> {
  return request(`/catalog/suppressions`, { method: "GET" });
}

export function suppressCatalogEntry(
  provider: "anibt" | "mikan",
  providerEntryRef: string,
  titleSnapshot: string,
): Promise<CatalogSuppression> {
  return request(`/catalog/suppressions/${encodePathSegment(provider)}/${encodePathSegment(providerEntryRef)}`, {
    method: "PUT",
    body: { title_snapshot: titleSnapshot },
  });
}

export function unsuppressCatalogEntry(
  provider: "anibt" | "mikan",
  providerEntryRef: string,
): Promise<void> {
  return request(`/catalog/suppressions/${encodePathSegment(provider)}/${encodePathSegment(providerEntryRef)}`, {
    method: "DELETE",
  });
}

/** Read-only group discovery of an unsubscribed entry: the snapshot persists on the server and can be refreshed manually before subscribing. */
export function discoverProviderEntryGroups(
  provider: "anibt" | "mikan",
  providerEntryRef: string,
): Promise<ProviderGroupDiscoveryResult> {
  return request(
    `/catalog/providers/${encodePathSegment(provider)}/entries/${encodePathSegment(providerEntryRef)}/groups:discover`,
    { method: "POST" },
  );
}

/** Reads an unsubscribed entry's latest RSS snapshot; triggers no provider request or server write. */
export function readProviderEntryRSS(
  provider: "anibt" | "mikan",
  providerEntryRef: string,
  groupRef: string,
): Promise<ProviderRSSPreviewResult> {
  const params = new URLSearchParams({ group_ref: groupRef });
  return request(
    `/catalog/providers/${encodePathSegment(provider)}/entries/${encodePathSegment(providerEntryRef)}/rss?${params.toString()}`,
    { method: "GET" },
  );
}

/** Explicitly refreshes an unsubscribed entry's RSS snapshot; only a user action crosses this provider fetch boundary. */
export function previewProviderEntryRSS(
  provider: "anibt" | "mikan",
  providerEntryRef: string,
  input: ProviderRSSPreviewInput,
): Promise<ProviderRSSPreviewResult> {
  return request(
    `/catalog/providers/${encodePathSegment(provider)}/entries/${encodePathSegment(providerEntryRef)}/rss`,
    { method: "POST", body: input },
  );
}

// --- RSS source actions -------------------------------------------------------

export function patchRSSSourceOffset(
  managedAnimeID: string,
  rssSourceID: string,
  patch: RSSSourceOffsetPatch,
): Promise<RSSSource> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/rss-sources/${encodePathSegment(rssSourceID)}/offset`, {
    method: "PATCH",
    body: patch,
  });
}

export function refreshRSSSource(
  rssSourceID: string,
  idempotencyKey: string,
): Promise<RSSRefreshAccepted> {
  return request(`/rss-sources/${encodePathSegment(rssSourceID)}/refresh`, {
    method: "POST",
    idempotencyKey,
  });
}

// --- Automation ---------------------------------------------------------------

export function listAutomationSchedules(): Promise<AutomationSchedule[]> {
  return request("/automation", { method: "GET" });
}

export function patchAutomationSchedule(
  kind: AutomationKind,
  patch: AutomationSchedulePatch,
): Promise<AutomationSchedule> {
  return request(`/automation/${encodePathSegment(kind)}`, {
    method: "PATCH",
    body: patch,
  });
}

export function runAutomation(
  kind: AutomationKind,
  idempotencyKey: string,
): Promise<AutomationRunAccepted> {
  return request(`/automation/${encodePathSegment(kind)}/run`, {
    method: "POST",
    idempotencyKey,
  });
}

export function getAutomationRun(runID: string): Promise<AutomationDispatchRun> {
  return request(`/automation/runs/${encodePathSegment(runID)}`, { method: "GET" });
}

// --- Online update -------------------------------------------------------------

export function checkUpdate(): Promise<UpdateCheck> {
  return request("/update/check", { method: "GET" });
}

export function admitUpdate(
  input: UpdateAdmitInput,
  idempotencyKey: string,
): Promise<EmbeddedUpdateStatus> {
  return request("/update/admit", {
    method: "POST",
    body: input,
    idempotencyKey,
  });
}

// A null current means there is no historical/active update record (404); other errors still reject.
export async function getUpdateStatus(): Promise<EmbeddedUpdateStatus | null> {
  try {
    const status: EmbeddedUpdateStatus = await request("/update/status", { method: "GET" });
    if (!status || typeof status.operation_id !== "string" || typeof status.step !== "string") {
      throw new ApiError({ status: 502, code: "UPDATE_STATUS_INVALID", message: "更新状态响应无效" });
    }
    return status;
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 404) return null;
    throw cause;
  }
}

/** The declared feed-site support list (fixed server code). */
export function listFeedSites(): Promise<FeedSiteList> {
  return request("/catalog/feed-sites", { method: "GET" });
}

// --- Backups -----------------------------------------------------------------

export function getBackupInfo(): Promise<BackupInfo> {
  return request("/backup", { method: "GET" });
}

async function rawFailure(response: Response, token: string): Promise<ApiError> {
  const payload = await response.json().catch(() => undefined);
  const error = readErrorPayload(payload);
  if (response.status === 401 && (token || sessionPresenceProvider())) {
    unauthorizedHandler?.();
  }
  return new ApiError({
    status: response.status,
    code: error.code,
    message: error.message ?? `请求失败 (${response.status})`,
    requestId: error.request_id,
    details: error.details,
  });
}

/** Sends a request whose body or response is a file rather than JSON. */
async function sendRaw(path: string, init: { method: string; contentType?: string; body?: Blob }): Promise<Response> {
  const headers = new Headers();
  if (init.contentType) headers.set("Content-Type", init.contentType);
  const token = tokenProvider();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init.method !== "GET") applyCsrfHeader(headers, init.method);
  let response: Response;
  try {
    response = await fetch(`${apiRoot}${path}`, {
      method: init.method,
      headers,
      credentials: "same-origin",
      body: init.body,
    });
  } catch (cause) {
    throw new ApiError({ status: 0, code: "NETWORK_ERROR", message: cause instanceof Error ? cause.message : "无法连接到服务" });
  }
  if (!response.ok) throw await rawFailure(response, token);
  return response;
}

/** Prepares a backup for download: the server dumps and verifies it and
 *  returns a short-lived single-use ticket (see backupDownloadUrl). */
export function prepareBackupDownload(): Promise<BackupDownloadTicket> {
  return request("/backup/download-ticket", { method: "POST" });
}

/** Uploads a backup file to replace all current data. On success the service
 *  restarts on the restored data and every session is signed out. */
export async function restoreBackup(file: Blob): Promise<BackupRestoreAccepted> {
  const response = await sendRaw("/backup/restore", { method: "POST", contentType: "application/octet-stream", body: file });
  return (await response.json()) as BackupRestoreAccepted;
}

/** The address that streams a prepared backup. The ticket is the credential,
 *  so a plain browser navigation saves the file straight to disk. */
export function backupDownloadUrl(ticket: string): string {
  return apiRoot + "/backup/download?ticket=" + encodeURIComponent(ticket);
}

export type ObservabilityRecordsQuery = NonNullable<
  operations["listObservabilityRecords"]["parameters"]["query"]
>;
export type ObservabilityDiagnosticsQuery = NonNullable<
  operations["listObservabilityDiagnostics"]["parameters"]["query"]
>;

// Scalars use set (a later same key overwrites the earlier), array values append repeatedly
// per form explode (kind=business&kind=plugin), matching the contract's repeatable query
// parameters.
function queryString(params: Record<string, string | number | readonly string[] | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined) continue;
    if (Array.isArray(value)) {
      for (const item of value) search.append(key, item);
    } else {
      search.set(key, String(value));
    }
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

export function listObservabilityRecords(
  query: ObservabilityRecordsQuery = {},
): Promise<ObservabilityRecordPage> {
  return request(`/observability/records${queryString(query)}`, { method: "GET" });
}

export function listObservabilityDiagnostics(
  query: ObservabilityDiagnosticsQuery = {},
): Promise<DiagnosticEpisodePage> {
  return request(`/observability/diagnostics${queryString(query)}`, { method: "GET" });
}

// Execution records: the read-only list, detail and file pages of admitted acquisitions. Reads never retry, reorder or recover anything.
export type AcquisitionRecordsQuery = NonNullable<operations["listAcquisitionRecords"]["parameters"]["query"]>;
export type AcquisitionFilesQuery = NonNullable<operations["listAcquisitionRecordFiles"]["parameters"]["query"]>;

export function listAcquisitionRecords(query: AcquisitionRecordsQuery = {}): Promise<AcquisitionRecordPage> {
  return request(`/acquisitions${queryString(query)}`, { method: "GET" });
}

export function getAcquisitionRecord(acquisitionID: string): Promise<AcquisitionRecordDetail> {
  return request(`/acquisitions/${encodePathSegment(acquisitionID)}`, { method: "GET" });
}

/** Retries the failed import stage: re-runs every failed file of this record; imported files
 *  are untouched and nothing is re-downloaded. `expectedVersion` is the detail's `version`;
 *  available only while `import_retry_available` is true, otherwise 409. */
export function retryAcquisitionImport(
  acquisitionID: string,
  expectedVersion: number,
  idempotencyKey: string,
): Promise<AcquisitionImportRetryResult> {
  return request(`/acquisitions/${encodePathSegment(acquisitionID)}/import/retry`, {
    method: "POST",
    ifMatchVersion: expectedVersion,
    idempotencyKey,
  });
}

export function listAcquisitionRecordFiles(acquisitionID: string, query: AcquisitionFilesQuery = {}): Promise<AcquisitionFilePage> {
  return request(`/acquisitions/${encodePathSegment(acquisitionID)}/files${queryString(query)}`, { method: "GET" });
}

// The file manager (read-only): absolute-path browsing, the ownership state derived per
// entry, and shortcut entries (mounts). A shortcut is only a navigation aid and never
// limits the readable/writable scope; GET never writes.
export type FileManagerEntriesQuery = NonNullable<operations["listFileManagerEntries"]["parameters"]["query"]>;

export function listFileManagerMounts(): Promise<FileManagerMountList> {
  return request(`/file-manager/mounts`, { method: "GET" });
}

export function listFileManagerEntries(query: FileManagerEntriesQuery): Promise<FileManagerListing> {
  return request(`/file-manager/entries${queryString(query)}`, { method: "GET" });
}

export function getFileManagerEntry(path: string): Promise<FileManagerEntryDetail> {
  return request(`/file-manager/entry${queryString({ path })}`, { method: "GET" });
}

/** User-initiated move, rename, copy and delete; 202 returns the operation view, afterwards
 *  follow it with getFileManagerOperation or the detail's last_operation. */
export function submitFileManagerOperation(input: FileManagerOperationRequest, idempotencyKey: string): Promise<FileManagerOperationView> {
  return request(`/file-manager/operations`, { method: "POST", body: input, idempotencyKey });
}

export function getFileManagerOperation(operationID: string): Promise<FileManagerOperationView> {
  return request(`/file-manager/operations/${encodePathSegment(operationID)}`, { method: "GET" });
}

export function releaseFileManagerEntry(input: FileManagerReleaseRequest, idempotencyKey: string): Promise<FileManagerReleaseResult> {
  return request(`/file-manager/entry/release`, { method: "POST", body: input, idempotencyKey });
}

export function alignFileManagerEntry(input: FileManagerAlignRequest, idempotencyKey: string): Promise<FileManagerAlignResult> {
  return request(`/file-manager/entry/align`, { method: "POST", body: input, idempotencyKey });
}

/** Match preview: writes nothing; returns the tidied location, folders that would be created, conflicts and blockers. */
export function previewFileManagerMatch(input: FileManagerMatchPreviewRequest): Promise<FileManagerMatchPreview> {
  return request(`/file-manager/entry/match/preview`, { method: "POST", body: input });
}

/** Submits one match: moves unmanaged videos to the location the anime's naming rules render; the result stays unmanaged; 202 returns the operation view. */
export function matchFileManagerEntry(input: FileManagerMatchRequest, idempotencyKey: string): Promise<FileManagerOperationView> {
  return request(`/file-manager/entry/match`, { method: "POST", body: input, idempotencyKey });
}

/** Season list (including season 0) of the TMDB work the anime is bound to; 409 TMDB_BINDING_REQUIRED when no TMDB binding exists. */
export function listAnimeTMDBSeasons(managedAnimeID: string): Promise<TMDBSeasonList> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/tmdb/seasons`, { method: "GET" });
}

export function listAnimeTMDBSeasonEpisodes(managedAnimeID: string, season: number): Promise<TMDBSeasonEpisodeList> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/tmdb/seasons/${season}/episodes`, { method: "GET" });
}

export function locateFileManagerAnime(managedAnimeID: string): Promise<FileManagerLocation> {
  return request(`/file-manager/anime/${encodePathSegment(managedAnimeID)}/location`, { method: "GET" });
}

// --- Global config sections ---------------------------------------------------

export type BusinessSnapshot = Schema<"BusinessSnapshot">;
export type BusinessPresetView = Schema<"BusinessPresetView">;

export type BusinessEditView = Schema<"BusinessEditView">;
export type CandidateApplyInput = Schema<"CandidateApplyInput">;
export type CandidateApplyResult = Schema<"CandidateApplyResult">;
export function getAnimeBusinessEdit(id: string): Promise<BusinessEditView> {
  return request(`/anime/${encodeURIComponent(id)}/business-edit`, { method: "GET" });
}
export function applyAnimeConfigCandidate(id: string, input: CandidateApplyInput, key: string): Promise<CandidateApplyResult> {
  return request(`/anime-config-candidates/${encodeURIComponent(id)}/apply`, { method: "POST", body: input, idempotencyKey: key });
}

export function getBusinessPreset(): Promise<BusinessPresetView> {
  return request("/config/business-preset", { method: "GET" });
}

export function getTitleTagVocabulary(): Promise<TitleTagVocabulary> {
  return request("/config/tag-filters/title-vocabulary", { method: "GET" });
}

export function listGlobalSections(): Promise<GlobalSectionView[]> {
  return request("/config/global-sections", { method: "GET" });
}

export function putGlobalSection(
  sectionName: GlobalSectionName,
  input: GlobalSectionInput,
  expectedVersion: number,
): Promise<GlobalSectionView> {
  return request(`/config/global-sections/${encodePathSegment(sectionName)}`, {
    method: "PUT",
    body: input,
    ifMatchVersion: expectedVersion,
  });
}

/** The naming template library: the template list maintained in global settings; the default template is the global naming. */
export function getNamingTemplateLibrary(): Promise<NamingTemplateLibraryView> {
  return request("/config/naming-templates", { method: "GET" });
}

export function putNamingTemplateLibrary(
  input: NamingTemplateLibraryInput,
  expectedVersion: number,
): Promise<NamingTemplateLibraryView> {
  return request("/config/naming-templates", {
    method: "PUT",
    body: input,
    ifMatchVersion: expectedVersion,
  });
}

// --- Subgroup dictionary (provider pool) ---------------------------------------

export type SubgroupGroupCandidate = Schema<"SubgroupGroupCandidate">;
export type SubgroupGroupCandidateList = Schema<"SubgroupGroupCandidateList">;
export type SubgroupGroupCandidatePage = Schema<"SubgroupGroupCandidatePage">;
export type SubgroupGroupCandidateSource = Schema<"SubgroupGroupCandidateSource">;

/** Purely local dictionary read: no provider request, no configured connection
 *  needed. Every filter is optional; each response is one page (limit default
 *  50, max 500) in dictionary order, continued by `next_offset` (null on the
 *  last page). `hide_retired` leaves out collected entries recorded as retired;
 *  the user's own additions always stay. */
export function listSubgroupGroupCandidates(
  provider: "anibt" | "mikan",
  params: {
    q?: string;
    source?: SubgroupGroupCandidateSource;
    hide_retired?: boolean;
    limit?: number;
    offset?: number;
  } = {},
): Promise<SubgroupGroupCandidatePage> {
  const query = new URLSearchParams({ provider });
  if (params.q !== undefined && params.q !== "") query.set("q", params.q);
  if (params.source !== undefined) query.set("source", params.source);
  if (params.hide_retired) query.set("hide_retired", "true");
  if (params.limit !== undefined) query.set("limit", String(params.limit));
  if (params.offset !== undefined && params.offset > 0) query.set("offset", String(params.offset));
  return request(`/config/subgroups/groups?${query.toString()}`, { method: "GET" });
}

/** Discovery: the only dictionary call that may read a provider. The query must
 *  be a key (Mikan group id, AniBT slug, or a group page link); a name answers
 *  400 SUBGROUP_DISCOVER_KEY_REQUIRED. A key the dictionary holds answers with
 *  no provider request; one it does not hold is confirmed with the provider and
 *  written to the user layer. An empty list means the provider confirmed
 *  nothing, not a failure. */
export function retrieveSubgroupGroupCandidate(
  provider: "anibt" | "mikan",
  query: string,
): Promise<SubgroupGroupCandidateList> {
  return request("/config/subgroups/groups", { method: "POST", body: { provider, query } });
}

/** Removes one user-layer entry. The collected layer is reference data and
 *  answers 409; an entry no layer holds answers 404. */
export function deleteSubgroupGroupDictionaryEntry(
  provider: "anibt" | "mikan",
  groupRef: string,
): Promise<void> {
  return request(
    `/config/subgroups/groups/${encodePathSegment(provider)}/${encodePathSegment(groupRef)}`,
    { method: "DELETE" },
  );
}

// --- Notification plugin targets / deliveries ---------------------------------

export type NotificationTriggerKind = Schema<"NotificationTriggerKind">;
export type NotificationTriggerKindVocabulary = Schema<"NotificationTriggerKindVocabulary">;
export type PluginAdapterKind = Schema<"PluginAdapterKind">;
export type PluginAdapterConfig = Schema<"PluginAdapterConfig">;
export type PluginAdapterSecretValue = Schema<"PluginAdapterSecretValue">;
export type PluginTargetConfig = Schema<"PluginTargetConfig">;
export type PluginTargetCreate = Schema<"PluginTargetCreate">;
export type PluginTargetUpdate = Schema<"PluginTargetUpdate">;
export type PluginTargetPage = Schema<"PluginTargetPage">;
export type PluginTargetTestInput = Schema<"PluginTargetTestInput">;
export type PluginTestAccepted = Schema<"PluginTestAccepted">;
export type PluginDeliveryDetail = Schema<"PluginDeliveryDetail">;
export type PluginDeliveryList = Schema<"PluginDeliveryList">;
export type PluginDeliveryState = Schema<"PluginDeliveryState">;
export type NotificationTemplateCatalog = Schema<"NotificationTemplateCatalog">;
export type NotificationTemplatePreviewInput = Schema<"NotificationTemplatePreviewInput">;
export type NotificationTemplatePreview = Schema<"NotificationTemplatePreview">;

// --- Long-lived API tokens (PAT) ---------------------------------------------

export function listApiTokens(): Promise<ApiToken[]> {
  return request("/api-tokens", { method: "GET" });
}

// Create echoes the raw token in the response. The raw value is also encrypted
// at rest on the server so it can be re-read and copied later (readApiTokenRaw);
// a 404 from that endpoint means the deployment key is not configured.
// Scopes are fixed for the token's lifetime (rotation keeps them); omitted
// means the server default [read, write].
export function createApiToken(name: string, scopes?: ApiTokenScope[]): Promise<ApiTokenCreateResult> {
  return request("/api-tokens", {
    method: "POST",
    body: (scopes ? { name, scopes } : { name }) satisfies ApiTokenCreateInput,
  });
}

export function revokeApiToken(tokenID: string): Promise<void> {
  return request(`/api-tokens/${encodePathSegment(tokenID)}`, { method: "DELETE" });
}

// Rotate revokes the current token and issues a fresh one for the same owner
// in one atomic server transaction; the new raw value is returned and stored
// encrypted at rest for later display.
export function rotateApiToken(tokenID: string): Promise<ApiTokenCreateResult> {
  return request(`/api-tokens/${encodePathSegment(tokenID)}`, { method: "POST" });
}

// Returns the raw token value for an active token so the UI can keep showing
// and copying the key for its lifetime. 404 when the deployment secret key is
// not configured (raw display unavailable) or the token no longer exists.
export async function readApiTokenRaw(tokenID: string): Promise<string> {
  const result = await request<ApiTokenRaw>(`/api-tokens/${encodePathSegment(tokenID)}/raw`, { method: "GET" });
  return result.token;
}

// The embedded English agent guide, served by the running version (text/markdown).
// "skill" returns it wrapped as a SKILL.md document.
export function readAgentGuide(format: "guide" | "skill" = "guide"): Promise<string> {
  return request(`/agent-guide${format === "skill" ? "?format=skill" : ""}`, { method: "GET", responseType: "text" });
}

export function listPluginTargets(): Promise<PluginTargetPage> {
  return request("/plugin-targets", { method: "GET" });
}

// The closed notification trigger registry, served from the same backend
// registry the delivery admission validates against.
export function listNotificationTriggerKinds(): Promise<NotificationTriggerKindVocabulary> {
  return request("/config/notifications/trigger-kinds", { method: "GET" });
}

export function createPluginTarget(
  input: PluginTargetCreate,
  idempotencyKey: string,
): Promise<PluginTargetConfig> {
  return request("/plugin-targets", { method: "POST", body: input, idempotencyKey });
}

export function updatePluginTarget(
  targetID: string,
  expectedVersion: number,
  input: PluginTargetUpdate,
): Promise<PluginTargetConfig> {
  return request(`/plugin-targets/${encodePathSegment(targetID)}`, {
    method: "PATCH",
    body: input,
    ifMatchVersion: expectedVersion,
  });
}

export function deletePluginTarget(targetID: string): Promise<void> {
  return request(`/plugin-targets/${encodePathSegment(targetID)}`, { method: "DELETE" });
}

export function testPluginTarget(
  targetID: string,
  input: PluginTargetTestInput,
  idempotencyKey: string,
): Promise<PluginTestAccepted> {
  return request(`/plugin-targets/${encodePathSegment(targetID)}/test`, {
    method: "POST",
    body: input,
    idempotencyKey,
  });
}

export function getPluginDelivery(deliveryID: string): Promise<PluginDeliveryDetail> {
  return request(`/plugin-deliveries/${encodePathSegment(deliveryID)}`, { method: "GET" });
}

export interface ListPluginDeliveriesParams {
  trigger_kind?: NotificationTriggerKind;
  source_owner_type?: "download_task" | "file_operation" | "plugin_target";
  source_owner_id?: string;
  state?: PluginDeliveryState;
  limit?: number;
  offset?: number;
}

export function listPluginDeliveries(params: ListPluginDeliveriesParams = {}): Promise<PluginDeliveryList> {
  const search = new URLSearchParams();
  if (params.trigger_kind) search.set("trigger_kind", params.trigger_kind);
  if (params.source_owner_type) search.set("source_owner_type", params.source_owner_type);
  if (params.source_owner_id) search.set("source_owner_id", params.source_owner_id);
  if (params.state) search.set("state", params.state);
  if (params.limit !== undefined) search.set("limit", String(params.limit));
  if (params.offset !== undefined) search.set("offset", String(params.offset));
  const query = search.toString();
  return request(`/plugin-deliveries${query ? `?${query}` : ""}`, { method: "GET" });
}

export type NotificationCatalogVersion = NotificationTemplateCatalog["catalog_version"];

export function getNotificationTemplateCatalog(
  triggerKind: NotificationTriggerKind,
  catalogVersion?: NotificationCatalogVersion,
): Promise<NotificationTemplateCatalog> {
  const query = catalogVersion ? `?${new URLSearchParams({ catalog_version: catalogVersion }).toString()}` : "";
  return request(`/template-catalogs/notification/${encodePathSegment(triggerKind)}${query}`, {
    method: "GET",
  });
}

export function previewNotificationTemplate(
  input: NotificationTemplatePreviewInput,
): Promise<NotificationTemplatePreview> {
  return request("/template-previews/notification", { method: "POST", body: input });
}

// --- Naming templates ---------------------------------------------------------

export type TemplateVariable = Schema<"TemplateVariable">;
export type TemplateFactPhase = Schema<"TemplateFactPhase">;
export type TemplateFunction = Schema<"TemplateFunction">;
export type TemplateWarning = Schema<"TemplateWarning">;
export type TemplateSourceError = Schema<"TemplateSourceError">;
export type NamingTemplateCatalog = Schema<"NamingTemplateCatalog">;
export type NamingCatalogField = Schema<"NamingCatalogField">;
export type NamingCatalogFunction = Schema<"NamingCatalogFunction">;
export type NamingCatalogSample = Schema<"NamingCatalogSample">;
export type NamingResolvedValue = Schema<"NamingResolvedValue">;
export type NamingTemplatePreviewInput = Schema<"NamingTemplatePreviewInput">;
export type NamingTemplatePreview = Schema<"NamingTemplatePreview">;
export type NamingTemplateKind = NamingTemplatePreviewInput["template_kind"];

export type NamingLanguage = NamingTemplateCatalog["language"];

export function getNamingTemplateCatalog(language?: NamingLanguage): Promise<NamingTemplateCatalog> {
  const query = language ? `?${new URLSearchParams({ language }).toString()}` : "";
  return request(`/template-catalogs/naming${query}`, { method: "GET" });
}

export function previewNamingTemplate(
  input: NamingTemplatePreviewInput,
): Promise<NamingTemplatePreview> {
  return request("/template-previews/naming", { method: "POST", body: input });
}

// --- Naming Vars program editor ------------------------------------------------

export type NamingVarsState = Schema<"NamingVarsState">;
export type NamingVarsProgram = Schema<"NamingVarsProgram">;
export type NamingVarsVersion = Schema<"NamingVarsVersion">;
export type NamingVarsInputFact = Schema<"NamingVarsInputFact">;
export type NamingVarsSample = Schema<"NamingVarsSample">;
export type NamingVarsDiagnostic = Schema<"NamingVarsDiagnostic">;
export type NamingVarsDryRunTarget = Schema<"NamingVarsDryRunTarget">;
export type NamingVarsDryRunTargetResult = Schema<"NamingVarsDryRunTargetResult">;
export type NamingVarsDryRunResult = Schema<"NamingVarsDryRunResult">;
export type NamingVarsReleaseOption = Schema<"NamingVarsReleaseOption">;
export type NamingVarsDocument = Schema<"NamingVarsDocument">;

export function getNamingVars(): Promise<NamingVarsState> {
  return request("/naming-vars", { method: "GET" });
}

export function getNamingVarsVersion(revision: number): Promise<NamingVarsProgram> {
  return request(`/naming-vars/versions/${revision}`, { method: "GET" });
}

export function listNamingVarsReleases(): Promise<NamingVarsReleaseOption[]> {
  return request("/naming-vars/releases", { method: "GET" });
}

export function dryRunNamingVars(source: string, targets: NamingVarsDryRunTarget[]): Promise<NamingVarsDryRunResult> {
  return request("/naming-vars/dry-runs", { method: "POST", body: { source, targets } });
}

/** `expectedRevision` is the revision the editor saw active; a mismatch is a 409. */
export function saveNamingVarsVersion(source: string, note: string, expectedRevision: number): Promise<NamingVarsProgram> {
  return request("/naming-vars/versions", {
    method: "POST",
    body: note.trim() ? { source, note } : { source },
    ifMatchVersion: expectedRevision,
  });
}

export function activateNamingVarsVersion(revision: number, expectedRevision: number): Promise<NamingVarsProgram> {
  return request("/naming-vars/active", { method: "PUT", body: { revision }, ifMatchVersion: expectedRevision });
}

export function exportNamingVars(): Promise<NamingVarsDocument> {
  return request("/naming-vars/export", { method: "GET" });
}

export function importNamingVars(document: NamingVarsDocument, expectedRevision: number): Promise<NamingVarsProgram> {
  return request("/naming-vars/imports", { method: "POST", body: document, ifMatchVersion: expectedRevision });
}


// --- FileMaintenance: neutral scan, owner truth, retention policies ----------

export type FileMaintenancePolicyView = Schema<"FileMaintenancePolicyView">;
export type FileMaintenancePolicyInput = Schema<"FileMaintenancePolicyInput">;
export type RetentionPolicyDefaultView = Schema<"RetentionPolicyDefaultView">;
export type RetentionPolicyDefaultInput = Schema<"RetentionPolicyDefaultInput">;
export type InstanceRetentionPolicyView = Schema<"InstanceRetentionPolicyView">;
export type ParserPolicyView = Schema<"ParserPolicyView">;
export type ParserPolicyInput = Schema<"ParserPolicyInput">;
export type ParserPolicyDefaultView = Schema<"ParserPolicyDefaultView">;
export type ParserPolicyDefaultInput = Schema<"ParserPolicyDefaultInput">;
export type FileEffectPolicyDefaultView = Schema<"FileEffectPolicyDefaultView">;
export type FileEffectPolicyDefaultInput = Schema<"FileEffectPolicyDefaultInput">;
export type InstanceFileEffectPolicyView = Schema<"InstanceFileEffectPolicyView">;
export type InstanceFileEffectPolicyInput = Schema<"InstanceFileEffectPolicyInput">;
export type UnclassifiedRoutePolicyValue = Schema<"UnclassifiedRoutePolicyValue">;
export type OffsetMigrationPlanView = Schema<"OffsetMigrationPlanView">;
export type OffsetMigrationGenerationView = Schema<"OffsetMigrationGenerationView">;
export type InstanceRetentionPolicyInput = Schema<"InstanceRetentionPolicyInput">;
export type ReconciliationScanView = Schema<"ReconciliationScanView">;
export type ReconciliationPathDiffView = Schema<"ReconciliationPathDiffView">;
export type PlacementReleaseResult = Schema<"PlacementReleaseResult">;

/** Manual neutral scan submission. The directory of the last scan scope is
 *  reused (its tree top); the request identity makes every click an independent
 *  trigger (a replay of the same request_id reuses the completed generation). */
export function submitReconciliationScan(managedAnimeID: string): Promise<ReconciliationScanView> {
  const trigger = "manual" as const;
  return request("/reconciliation/scans", {
    method: "POST",
    body: { managed_anime_id: managedAnimeID, trigger, request_id: newUUID() },
  });
}

export type AnimeFilesView = Schema<"AnimeFilesView">;
export type AnimeFile = Schema<"AnimeFile">;
export type AnimeFileCompanion = Schema<"AnimeFileCompanion">;
export type AnimeFileAction = Schema<"AnimeFileAction">;
export type AnimeFilesAlignResult = Schema<"AnimeFilesAlignResult">;

/** One anime's files dialog: imported files, the latest scan result and the per-row available actions (judged by the server). */
export function listAnimeFiles(managedAnimeID: string): Promise<AnimeFilesView> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/files`, { method: "GET" });
}

/** A one-off alignment: not bound by the automatic-alignment switch; `generation` is the
 *  scan generation as read, and an empty placementIDs aligns every file awaiting alignment.
 *  When unavailable: 409 ANIME_FILES_ALIGN_UNAVAILABLE with the reason code in
 *  details[0].code. */
export function alignAnimeFiles(
  managedAnimeID: string,
  generation: number,
  placementIDs?: string[],
): Promise<AnimeFilesAlignResult> {
  return request(`/anime/${encodePathSegment(managedAnimeID)}/files/align`, {
    method: "POST",
    body: placementIDs ? { generation, placement_ids: placementIDs } : { generation },
  });
}

/** Side-effect free path-difference preview (0 = latest generation). */
export function listReconciliationPathDiffs(
  managedAnimeID: string,
  generation = 0,
): Promise<ReconciliationPathDiffView[]> {
  return request(
    `/reconciliation/scans/${encodePathSegment(managedAnimeID)}/path-diffs?generation=${generation}`,
    { method: "GET" },
  );
}

export function getFileMaintenancePolicy(): Promise<FileMaintenancePolicyView> {
  return request("/file-maintenance/policy", { method: "GET" });
}

export function saveFileMaintenancePolicy(input: FileMaintenancePolicyInput): Promise<FileMaintenancePolicyView> {
  return request("/file-maintenance/policy", { method: "PUT", body: input });
}

export function getFileMaintenanceAnimePolicy(managedAnimeID: string): Promise<FileMaintenancePolicyView> {
  return request(`/file-maintenance/policy/anime/${encodePathSegment(managedAnimeID)}`, { method: "GET" });
}

export function saveFileMaintenanceAnimePolicy(
  managedAnimeID: string,
  input: FileMaintenancePolicyInput,
): Promise<FileMaintenancePolicyView> {
  return request(`/file-maintenance/policy/anime/${encodePathSegment(managedAnimeID)}`, { method: "PUT", body: input });
}

/** Global new-instance retention default (future instances only). */
export function getRetentionPolicyDefault(): Promise<RetentionPolicyDefaultView> {
  return request("/file-maintenance/retention/default", { method: "GET" });
}

export function saveRetentionPolicyDefault(
  input: RetentionPolicyDefaultInput,
): Promise<RetentionPolicyDefaultView> {
  return request("/file-maintenance/retention/default", { method: "PUT", body: input });
}

export type QualityProfile = Schema<"QualityProfile">;
export type QualityCompareKey = Schema<"QualityCompareKey">;
export type RetentionCompareVocabulary = Schema<"RetentionCompareVocabulary">;

/** Suggested values per ranked comparison item (of one anime when named). */
export function getRetentionCompareVocabulary(managedAnimeID?: string): Promise<RetentionCompareVocabulary> {
  const query = managedAnimeID ? `?${new URLSearchParams({ managed_anime_id: managedAnimeID }).toString()}` : "";
  return request(`/file-maintenance/retention/compare-vocabulary${query}`, { method: "GET" });
}

/** Complete frozen per-instance retention policy. */
/** Frozen per-instance parser policy (parser kind + default offset). */
/** Frozen offset-correction migration plans of one anime. */
/** Durable offset migration generations of one anime (owner truth). */
/** Singleton file-effect default (new instances only; snapshot semantics). */
export function getFileEffectPolicyDefault(): Promise<FileEffectPolicyDefaultView> {
  return request("/file-maintenance/file-effect/default", { method: "GET" });
}

export function saveFileEffectPolicyDefault(
  input: FileEffectPolicyDefaultInput,
): Promise<FileEffectPolicyDefaultView> {
  return request("/file-maintenance/file-effect/default", { method: "PUT", body: input });
}

/** Frozen per-instance unclassified_route policy (snapshot at subscribe). */
export function getInstanceFileEffectPolicy(managedAnimeID: string): Promise<InstanceFileEffectPolicyView> {
  return request(`/file-maintenance/file-effect/anime/${encodePathSegment(managedAnimeID)}`, { method: "GET" });
}

export function saveInstanceFileEffectPolicy(
  managedAnimeID: string,
  input: InstanceFileEffectPolicyInput,
): Promise<InstanceFileEffectPolicyView> {
  return request(`/file-maintenance/file-effect/anime/${encodePathSegment(managedAnimeID)}`, {
    method: "PUT",
    body: input,
  });
}

/** Frozen per-instance parser policy: the selected parser kind (offsets live
 *  only on RSS links, never on the policy). */
export function getInstanceParserPolicy(managedAnimeID: string): Promise<ParserPolicyView> {
  return request(`/file-maintenance/parser-policy/anime/${encodePathSegment(managedAnimeID)}`, { method: "GET" });
}

export function saveInstanceParserPolicy(
  managedAnimeID: string,
  input: ParserPolicyInput,
): Promise<ParserPolicyView> {
  return request(`/file-maintenance/parser-policy/anime/${encodePathSegment(managedAnimeID)}`, {
    method: "PUT",
    body: input,
  });
}

/** Singleton parser-policy creation template: the parser kind a future instance
 *  freezes at creation. Absent row answers the code default regex with
 *  version 0 ("never saved"). Existing instances never re-inherit it. */
export function getParserPolicyDefault(): Promise<ParserPolicyDefaultView> {
  return request("/file-maintenance/parser-policy/default", { method: "GET" });
}

export function saveParserPolicyDefault(
  input: ParserPolicyDefaultInput,
): Promise<ParserPolicyDefaultView> {
  return request("/file-maintenance/parser-policy/default", { method: "PUT", body: input });
}

export type ParserCatalogEntry = Schema<"ParserCatalogEntry">;

/** Read-only catalog of code-owned registered media parsers; the ep-parser
 *  picker keeps only entries with the release_title capability. */
export function listParserCatalog(): Promise<ParserCatalogEntry[]> {
  return request("/parser-catalog", { method: "GET" });
}
