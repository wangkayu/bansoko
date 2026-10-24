import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setSessionTokenProvider, setUnauthorizedHandler, type AnimeDraft } from "../api/client";
import { ServerCandidateStore } from "./serverCandidateStore";
import {
  CandidateAlreadyActivated,
  CandidateNotReady,
  CandidateVersionConflict,
  CandidateWorkingDuplicate,
} from "./port";

// The candidate source of truth is on the server, so what is tested here is the HTTP shape
// and error translation: requests hit the agreed path/method/body, the server's 409/404 are
// translated into port.ts's domain errors, and other errors are not swallowed.

const draft: AnimeDraft = {
  origin: "mikan",
  origin_entry_ref: "mikan-entry-1",
  title: "测试番剧",
  title_override: null,
  instance_label: null,
  metadata: { bgm: null, tmdb: null },
  subgroups: { max_rss: 3, entries: [] },
  rules: {
    sections: {},
  },
  rss_sources: [],
};

function candidateBody(overrides: Record<string, unknown> = {}) {
  return {
    id: "cand-1",
    origin: "mikan",
    origin_entry_ref: "mikan-entry-1",
    title: "测试番剧",
    title_override: null,
    instance_label: null,
    draft,
    version: 1,
    activated_managed_anime_id: null,
    created_at: "2026-07-26T00:00:00Z",
    updated_at: "2026-07-26T00:00:00Z",
    status: "working",
    draft_digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
    last_activity: "2026-07-26T00:00:00Z",
    retention_until: "2026-08-25T00:00:00Z",
    current_revision: 1,
    current_revision_digest: "sha256:" + "1".repeat(64),
    cleanup_eligible_at: null,
    cleanup_reason: null,
    ...overrides,
  };
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("ServerCandidateStore", () => {
  const fetchMock = vi.fn();
  let store: ServerCandidateStore;

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockReset();
    setSessionTokenProvider(() => "");
    setUnauthorizedHandler(() => {});
    store = new ServerCandidateStore();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    setSessionTokenProvider(() => "");
  });

  it("列表按 origin 与 origin_entry_ref 过滤", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { candidates: [candidateBody()] }));
    const result = await store.list({ origin: "mikan", origin_entry_ref: "mikan-entry-1" });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime-config-candidates?origin=mikan&origin_entry_ref=mikan-entry-1");
    expect(init.method).toBe("GET");
    expect(result).toEqual([candidateBody()]);
  });

  it("保存把整份 draft 放进请求体", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, candidateBody()));
    await store.save(draft);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime-config-candidates");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ draft });
  });

  it("CAS 更新用 PATCH 与 If-Match 头携带期望版本", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, candidateBody({ version: 3 })));
    await store.patch("cand-1", draft, 2);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime-config-candidates/cand-1");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(String(init.body))).toEqual({ draft });
    expect(new Headers(init.headers).get("If-Match")).toBe("2");
  });

  it("版本冲突翻译成 CandidateVersionConflict 并带上当前版本（零自动 GET）", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(409, { code: "VERSION_CONFLICT", message: "冲突", current_version: 5 }),
    );
    const failure = await store.patch("cand-1", draft, 2).catch((cause: unknown) => cause);
    expect(failure).toBeInstanceOf(CandidateVersionConflict);
    expect((failure as CandidateVersionConflict).currentVersion).toBe(5);
    // No automatic GET anymore; fetch is called exactly once
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("保存 provider 重复翻译成 CandidateWorkingDuplicate", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(409, { code: "CANDIDATE_WORKING_DUPLICATE", message: "重复" }),
    );
    const failure = await store.save(draft).catch((cause: unknown) => cause);
    expect(failure).toBeInstanceOf(CandidateWorkingDuplicate);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("激活带 If-Match + Idempotency-Key 并打到 subscribe 子路径", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { managed_anime: { id: "anime-1", title: "t", version: 1 } }));
    await store.subscribe("cand-1", 1, "idem-key-1", { revision: 1 });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/anime-config-candidates/cand-1/subscribe");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("If-Match")).toBe("1");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("idem-key-1");
  });

  it("重复激活翻译成 CandidateAlreadyActivated（零自动 GET）", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(409, { code: "CANDIDATE_ALREADY_ACTIVATED", message: "候选已激活" }),
    );
    const failure = await store.subscribe("cand-1", 1, "idem-key-2", { revision: 1 }).catch((cause: unknown) => cause);
    expect(failure).toBeInstanceOf(CandidateAlreadyActivated);
    // No automatic GET anymore
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("激活不存在的候选翻译成 CandidateNotReady", async () => {
    fetchMock.mockResolvedValue(jsonResponse(404, { code: "NOT_FOUND", message: "候选不存在" }));
    const failure = await store.subscribe("missing", 1, "idem-key-3", { revision: 1 }).catch((cause: unknown) => cause);
    expect(failure).toBeInstanceOf(CandidateNotReady);
  });

  it("删除已激活候选翻译成 CandidateAlreadyActivated（零自动 GET）", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(409, { code: "CANDIDATE_ALREADY_ACTIVATED", message: "候选已激活" }),
    );
    const failure = await store.delete("cand-1").catch((cause: unknown) => cause);
    expect(failure).toBeInstanceOf(CandidateAlreadyActivated);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("删除候选透传服务端幂等 204", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await expect(store.delete("missing")).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("读取不存在的候选返回 null，其余错误不吞掉", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(404, { code: "NOT_FOUND", message: "候选不存在" }));
    await expect(store.get("missing")).resolves.toBeNull();

    fetchMock.mockResolvedValueOnce(jsonResponse(500, { code: "INTERNAL", message: "服务端错误" }));
    await expect(store.get("cand-1")).rejects.toThrow();
  });
});
