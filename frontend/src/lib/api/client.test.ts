import { t } from "../i18n";
import { errorDetailsFor } from "../errors";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  acquireSeedOnce,
  createBootstrapOwner,
  retryRelease,
  getOneShotAcquisition,
  getOneShotExecutionAttempt,
  getReadyz,
  listOneShotExecutionAttempts,
  fetchOwnerCustomSnapshot,
  getAuthSession,
  isVersionConflict,
  listAnime,
  listObservabilityDiagnostics,
  logout,
  listObservabilityRecords,
  matchAnimeMetadata,
  newUUID,
  patchAnimeMetadata,
  patchAnimeRules,
  patchAnimeSubgroups,
  readAgentGuide,
  putConnection,
  retryDownloadTask,
  setCsrfTokenProvider,
  setSessionTokenProvider,
  setUnauthorizedHandler,
  subscribeAnime,
  toUserMessage,
  checkUpdate,
  admitUpdate,
  getUpdateStatus,
  type AnimeDraft,
} from "./client";

const emptyDraft: AnimeDraft = {
  origin: "custom",
  origin_entry_ref: null,
  title: "测试番剧",
  title_override: null,
  instance_label: "测试番剧",
  metadata: { bgm: null, tmdb: null },
  subgroups: { max_rss: 3, entries: [] },
  rules: {
    sections: {},
  },
  rss_sources: [
    {
      source_kind: "custom_rss",
      locator: "https://example.com/feed.xml",
      locator_display: "https://example.com/feed.xml",
      group_name: "测试字幕组",
      title: "测试番剧",
      episode_offset: 0,
    },
  ],
};

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("api client", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockReset();
    setSessionTokenProvider(() => "");
    setUnauthorizedHandler(() => {});
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    setSessionTokenProvider(() => "");
  });

  it("generates a UUID-like key when randomUUID is unavailable", () => {
    const originalCrypto = globalThis.crypto;
    vi.stubGlobal("crypto", {
      getRandomValues<T extends ArrayBufferView>(array: T): T {
        const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
        bytes.fill(7);
        return array;
      },
    });

    const value = newUUID();
    expect(value).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-8[0-9a-f]{3}-[0-9a-f]{12}$/);
    vi.stubGlobal("crypto", originalCrypto);
  });

  it("attaches the bearer token from the registered provider", async () => {
    setSessionTokenProvider(() => "token-123");
    fetchMock.mockResolvedValue(jsonResponse(200, []));
    await listAnime();
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer token-123");
  });

  it("sends Idempotency-Key on subscribe", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { managed_anime: { id: "a", title: "t", version: 1 } }));
    await subscribeAnime(emptyDraft, "idem-key-1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime/subscribe");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("idem-key-1");
    expect(JSON.parse(String(init.body))).toEqual({ draft: emptyDraft });
  });

  it("fetches a pending custom subscription's feed snapshot by subscription id only", async () => {
    const response = { snapshot_ref: "custom-source:abc" };
    fetchMock.mockResolvedValue(jsonResponse(200, response));
    await expect(fetchOwnerCustomSnapshot("anime-1")).resolves.toEqual(response);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime/anime-1/custom-feed-snapshots:fetch");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
  });

  it("uses the single online update endpoints and binds admit to the checked candidate", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(200, {
        status: "update_available",
        current: { version: "0.9.0", release_id: "release-current", image_compatibility: "base-v3" },
        candidate: {
          release_id: "release-next",
          version: "0.9.1",
          release_sequence: 4,
          published_at: "2026-08-29T00:00:00Z",
          target_migration_version: "040_online_update",
          required_downtime: true,
          backup_required: true,
          rollback_class: "restore_required",
          manifest_digest: "sha256:manifest",
          payload_size: 123,
        },
        compatibility: { kind: "overlay", min_image_version: "base-v3", reason: "overlay" },
        checked_at: "2026-08-29T00:00:00Z",
      }),
    );
    await checkUpdate();
    let [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/update/check");
    expect(init.method).toBe("GET");

    fetchMock.mockReset();
    fetchMock.mockResolvedValue(jsonResponse(201, { operation_id: "op-1", step: "accepted" }));
    await admitUpdate(
      { target_release_id: "release-next", manifest_digest: "sha256:manifest" },
      "update-key-1",
    );
    [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/update/admit");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("update-key-1");
    expect(JSON.parse(String(init.body))).toEqual({
      target_release_id: "release-next",
      manifest_digest: "sha256:manifest",
    });
  });

  it("maps update status 404 to null and reads the current status endpoint", async () => {
    fetchMock.mockResolvedValue(jsonResponse(404, { code: "UPDATE_NOT_FOUND", message: "not found" }));
    await expect(getUpdateStatus()).resolves.toBeNull();
    let [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/update/status");
    expect(init.method).toBe("GET");

    fetchMock.mockReset();
    fetchMock.mockResolvedValue(jsonResponse(200, { operation_id: "op-1", step: "staged" }));
    await expect(getUpdateStatus()).resolves.toMatchObject({ operation_id: "op-1", step: "staged" });
  });

  it.each([null, {}, { operation_id: "op-1" }, { step: "succeeded" }])(
    "rejects malformed successful update status %j", async (body) => {
      fetchMock.mockResolvedValue(jsonResponse(200, body));
      await expect(getUpdateStatus()).rejects.toMatchObject({ code: "UPDATE_STATUS_INVALID", status: 502 });
    },
  );

  it("sends If-Match version on connection save", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, {}));
    await putConnection(
      "qb-main",
      { kind: "qbittorrent", name: "qb", enabled: true, base_url: "http://qb.local:8080", username: "u", secret: { mode: "replace", value: "p" } },
      3,
    );
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/connections/qb-main");
    expect(new Headers(init.headers).get("If-Match")).toBe("3");
  });

  it("sends credential-free provider connection payloads", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, {}));
    await putConnection("anibt-main", { kind: "anibt", name: "AniBT", enabled: true, base_url: "http://anibt.local:8080" }, 0);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/connections/anibt-main");
    expect(JSON.parse(String(init.body))).toEqual({ kind: "anibt", name: "AniBT", enabled: true, base_url: "http://anibt.local:8080" });
  });

  it("sends expected_version in the patch body", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, {}));
    await patchAnimeMetadata("anime-1", { expected_version: 7, bgm: { work_id: "123" }, tmdb: null });
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      expected_version: 7,
      bgm: { work_id: "123" },
      tmdb: null,
    });
  });

  it("runs automatic metadata matching with the current version", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { outcome: "no_result" }));
    await matchAnimeMetadata("anime/1", { expected_version: 4 }, "key-match-1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime/anime%2F1/metadata/match");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ expected_version: 4 });
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("key-match-1");
  });

  it("sends the complete rules patch body", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, {}));
    const sections = {
      parser: { mode: "replace" as const, value: { episode_patterns: [] } },
      release_filter: {
        mode: "replace" as const,
        value: { must_contain: "", not_in: "" },
      },
      file_filter: {
        mode: "replace" as const,
        value: { must_contain: "", not_in: "" },
      },
      naming: {
        mode: "replace" as const,
        value: {
          language: "js-naming-1" as const,
          unknown: { directory_template: "", video_filename_template: "" },
          tv: { directory_template: "", video_filename_template: "" },
          movie: { directory_template: "", video_filename_template: "" },
          subtitle_suffix_template: "",
        },
      },
      file_effect: {
        mode: "replace" as const,
        value: { operation: "copy" as const, collision_policy: "fail" as const },
      },
    };
    await patchAnimeRules("anime/rules-1", { expected_version: 19, sections });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime/anime%2Frules-1/rules");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(String(init.body))).toEqual({ expected_version: 19, sections });
  });

  it("sends the complete subgroups patch body", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, {}));
    const subgroups = {
      max_rss: 4,
      entries: [
        {
          provider: "anibt" as const,
          group_ref: "group-ranked",
          display_name: "Ranked 组",
          selection_mode: "auto" as const,
          manual_selected: null,
          effective_selected: true,
          score: 80,
          count_in_limit: true,
        },
        {
          provider: "anibt" as const,
          group_ref: "group-always",
          display_name: "Always 组",
          selection_mode: "manual" as const,
          manual_selected: true,
          effective_selected: true,
          score: 0,
          count_in_limit: false,
        },
        {
          provider: "anibt" as const,
          group_ref: "group-never",
          display_name: "Never 组",
          selection_mode: "manual" as const,
          manual_selected: false,
          effective_selected: false,
          score: 20,
          count_in_limit: true,
        },
      ],
    };
    const groupActions = [
      { provider: "anibt" as const, group_ref: "g1", action: "restore_auto" as const },
      { provider: "mikan" as const, group_ref: "g2", action: "force_include" as const },
      { provider: "anibt" as const, group_ref: "g3", action: "force_exclude" as const },
    ];
    await patchAnimeSubgroups("anime/subgroups-1", {
      expected_version: 12,
      subgroups,
      group_actions: groupActions,
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime/anime%2Fsubgroups-1/subgroups");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(String(init.body))).toEqual({
      expected_version: 12,
      subgroups,
      group_actions: groupActions,
    });
  });

  it("sends the manual retry payload and idempotency key", async () => {
    fetchMock.mockResolvedValue(jsonResponse(202, { owner_type: "download_task", owner_id: "dt-1", accepted: true }));
    await retryDownloadTask("dt-1", { failed_work_item_id: "wi-9", reason: "手动修复" }, "req-key-1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/download-tasks/dt-1/retry");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("req-key-1");
    expect(JSON.parse(String(init.body))).toEqual({ failed_work_item_id: "wi-9", reason: "手动修复" });
  });

  it("sends a release retry with idempotency key and no body", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { release_id: "rel-1", verdict: "filtered", outcome: "filtered", reason_code: "filtered_by_rules" }));
    const result = await retryRelease("rel-1", "retry-key-1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/releases/rel-1/retry");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("retry-key-1");
    expect(result.outcome).toBe("filtered");
  });

  it("creates the bootstrap owner directly without any setup token", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(201, { expires_at: "", owner: { id: "o", username: "u", role: "owner" } }),
    );
    await createBootstrapOwner({ username: "owner", password: "password-123" });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/bootstrap/owner");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Authorization")).toBeNull();
    // The browser always asks for the cookie transport: the credential must
    // never come back in a body script can read.
    expect(JSON.parse(String(init.body))).toEqual({
      username: "owner",
      password: "password-123",
      session_transport: "cookie",
    });
  });

  it("echoes the CSRF cookie on unsafe requests only", async () => {
    setCsrfTokenProvider(() => "csrf-value");
    fetchMock.mockResolvedValue(jsonResponse(200, []));
    await listAnime();
    expect(new Headers((fetchMock.mock.calls[0] as [string, RequestInit])[1].headers).get("X-CSRF-Token")).toBeNull();

    fetchMock.mockResolvedValue(jsonResponse(204, undefined));
    await logout();
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe("/api/v1/auth/logout");
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-value");
    // The session rides an HttpOnly cookie, which only same-origin
    // credentials attach.
    expect(init.credentials).toBe("same-origin");
    setCsrfTokenProvider(() => "");
  });

  it("probes the cookie session through GET /auth/session", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(200, { owner: { id: "o", username: "u", role: "owner" }, expires_at: "2030-01-01T00:00:00Z" }),
    );
    const session = await getAuthSession();
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/auth/session");
    expect(init.method).toBe("GET");
    expect(session.owner.username).toBe("u");
  });

  it("parses typed error payloads with code, request id and current version", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(409, {
        code: "OWNER_VERSION_CONFLICT",
        message: "version conflict",
        request_id: "req-42",
        current_version: 11,
        details: [{ field: "expected_version", code: "OWNER_VERSION_CONFLICT", message: "stale" }],
      }),
    );
    const cause = await patchAnimeMetadata("a", { expected_version: 10, bgm: null, tmdb: null }).catch((e) => e);
    expect(cause).toBeInstanceOf(ApiError);
    const error = cause as ApiError;
    expect(error.code).toBe("OWNER_VERSION_CONFLICT");
    expect(error.requestId).toBe("req-42");
    expect(error.currentVersion).toBe(11);
    expect(error.details?.[0]?.field).toBe("expected_version");
    expect(isVersionConflict(error)).toBe(true);
  });

  it("preserves the complete readiness report returned with HTTP 503", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(503, {
        ready: false,
        reason: "not_ready:maintenance:active",
        checks: {
          maintenance: { ok: false, code: "maintenance", detail: "rollback is blocked" },
          deployment_identity: { ok: false, code: "inconsistent", detail: "pointer mismatch" },
        },
        checked_at: "2026-08-28T00:00:00Z",
        deployment_identity: { selected_source: "overlay", selected_release_id: "release-1" },
      }),
    );

    await expect(getReadyz()).resolves.toMatchObject({
      ready: false,
      reason: "not_ready:maintenance:active",
      checks: { deployment_identity: { detail: "pointer mismatch" } },
      deployment_identity: { selected_release_id: "release-1" },
    });
  });

  it("maps network failures to a typed ApiError", async () => {
    fetchMock.mockRejectedValue(new TypeError("fetch failed"));
    const cause = await listAnime().catch((e) => e);
    expect(cause).toBeInstanceOf(ApiError);
    expect((cause as ApiError).status).toBe(0);
    expect((cause as ApiError).code).toBe("NETWORK_ERROR");
  });

  it("invokes the unauthorized handler on 401 when a token was sent", async () => {
    const onUnauthorized = vi.fn();
    setSessionTokenProvider(() => "expired-token");
    setUnauthorizedHandler(onUnauthorized);
    fetchMock.mockResolvedValue(jsonResponse(401, { code: "SESSION_INVALID", message: "expired" }));
    await expect(listAnime()).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it("does not invoke the unauthorized handler on anonymous 401", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    fetchMock.mockResolvedValue(jsonResponse(401, { code: "SESSION_REQUIRED", message: "required" }));
    await expect(listAnime()).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("formats user messages with details and request id", async () => {
    const error = new ApiError({
      status: 422,
      code: "DRAFT_INVALID",
      message: "草稿不合法",
      requestId: "req-9",
      details: [{ field: "title", code: "REQUIRED", message: "标题不能为空" }],
    });
    expect(toUserMessage(error, "失败")).toBe(`${t("error.DRAFT_INVALID")}（${t("field.title")}）`);
    expect(errorDetailsFor(toUserMessage(error, "失败"))?.requestId).toBe("req-9");
    expect(toUserMessage(new Error("boom"), "失败")).toBe("失败");
    expect(toUserMessage(undefined, "失败")).toBe("失败");
  });

  it("serializes observability record filters: kind array repeats, scalars set once", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { records: [], next_cursor: null }));
    await listObservabilityRecords({
      kind: ["business", "plugin"],
      outcome: "failure",
      managed_anime_id: "anime-1",
      limit: 50,
      cursor: "cursor-1",
    });
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    const params = new URL(url, "http://localhost").searchParams;
    expect(params.getAll("kind")).toEqual(["business", "plugin"]);
    expect(params.get("outcome")).toBe("failure");
    expect(params.get("managed_anime_id")).toBe("anime-1");
    expect(params.get("limit")).toBe("50");
    expect(params.get("cursor")).toBe("cursor-1");
  });

  it("omits undefined observability filters from the query string", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { episodes: [], next_cursor: null }));
    await listObservabilityDiagnostics({ limit: 50 });
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/observability/diagnostics?limit=50");
  });

  it("posts one-shot acquisition with the idempotency key and full body", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(201, {
        one_shot_id: "os-1",
        managed_anime_id: "a",
        subscription_id: "s",
        rss_source_id: "r",
        release_id: "rel",
        download_task_id: "t",
        state: "running",
      }),
    );
    const input = {
      candidate_id: "candidate-1",
      revision: 1,
      selected_seed: {
        provider: "anibt" as const,
        provider_entry_ref: "entry-1",
        provider_group_ref: "g-1",
        locator: "https://torrent.example.test/01.torrent",
        title: "[G] 测试番 - 01",
        published_at: "2026-08-20T12:00:00Z",
        info_hash: "abcdef1234567890abcdef1234567890abcdef12",
      },
    };
    await acquireSeedOnce(input, "one-shot-key-1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/one-shot-acquisitions");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("one-shot-key-1");
    expect(JSON.parse(String(init.body))).toEqual(input);
  });

  it("maps one-shot read 404 to null (marker purged) and keeps other errors typed", async () => {
    fetchMock.mockResolvedValue(jsonResponse(404, { code: "ONE_SHOT_NOT_FOUND", message: "gone" }));
    await expect(getOneShotAcquisition("os-1")).resolves.toBeNull();
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/one-shot-acquisitions/os-1");
    expect(init.method).toBe("GET");

    fetchMock.mockReset();
    fetchMock.mockResolvedValue(jsonResponse(500, { code: "INTERNAL", message: "boom" }));
    const cause = await getOneShotAcquisition("os-1").catch((e) => e);
    expect(cause).toBeInstanceOf(ApiError);
    expect((cause as ApiError).status).toBe(500);
  });

  it("lists one-shot execution attempts with limit and opaque cursor, and reads one attempt", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { items: [], next_cursor: "cursor-2" }));
    await listOneShotExecutionAttempts("os-1", { limit: 20 });
    let [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/one-shot-acquisitions/os-1/attempts?limit=20");
    expect(init.method).toBe("GET");

    await listOneShotExecutionAttempts("os-1", { cursor: "cursor-2", limit: 20 });
    [url] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe("/api/v1/one-shot-acquisitions/os-1/attempts?cursor=cursor-2&limit=20");

    fetchMock.mockResolvedValue(
      jsonResponse(200, {
        id: "attempt-1",
        acquisition_id: "os-1",
        sequence: 1,
        mode: "initial",
        state: "failed",
        input_digest: "sha256:" + "f".repeat(64),
        created_at: "2026-08-25T00:00:00Z",
        updated_at: "2026-08-25T00:01:00Z",
      }),
    );
    await getOneShotExecutionAttempt("os-1", "attempt-1");
    [url] = fetchMock.mock.calls[2] as [string, RequestInit];
    expect(url).toBe("/api/v1/one-shot-acquisitions/os-1/attempts/attempt-1");
  });
  it("reads the agent guide as text, optionally wrapped as a skill, and reports API errors", async () => {
    fetchMock.mockResolvedValue(new Response("# Bansoko agent guide\n", { status: 200, headers: { "Content-Type": "text/markdown" } }));
    await expect(readAgentGuide()).resolves.toBe("# Bansoko agent guide\n");
    let [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/agent-guide");
    expect(init.method).toBe("GET");

    fetchMock.mockResolvedValue(new Response("---\nname: bansoko-agent\n---\n", { status: 200 }));
    await expect(readAgentGuide("skill")).resolves.toBe("---\nname: bansoko-agent\n---\n");
    [url] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe("/api/v1/agent-guide?format=skill");

    fetchMock.mockResolvedValue(jsonResponse(401, { code: "SESSION_INVALID", message: "expired" }));
    await expect(readAgentGuide()).rejects.toMatchObject({ status: 401, code: "SESSION_INVALID" });
  });
});
