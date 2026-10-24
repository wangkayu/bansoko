import {
  ApiError,
  deleteAnimeConfigCandidate,
  getAnimeConfigCandidate,
  listAnimeConfigCandidates,
  patchAnimeConfigCandidate,
  saveAnimeConfigCandidate,
  subscribeAnimeConfigCandidate,
  type AnimeDraft,
  type CandidateSubscribeInput,
  type SubscribeResult,
} from "../api/client";
import {
  CandidateAlreadyActivated,
  CandidateNotReady,
  CandidateVersionConflict,
  CandidateWorkingDuplicate,
  type AnimeConfigCandidate,
  type CandidateQuery,
  type CandidateStore,
} from "./port";

// The server candidate store: candidates persist in anime_config_candidates, activation goes
// through /anime-config-candidates/{id}/subscribe, and the owner set plus
// activated_managed_anime_id commit in the same transaction. This layer does only the HTTP
// calls and error translation — no caching, no local recomputation of candidate state; the
// candidate source of truth is on the server.
//
// Hardening: on 409 there is zero automatic GET; only
// CandidateVersionConflict(currentVersion) is thrown, and the UI decides whether to reload.
// subscribe sends If-Match expectedVersion.

/**
 * 把服务端的 409 错误翻译成 port.ts 的领域错误。
 * 不自动 GET；只使用错误体中携带的 currentVersion / code。
 */
function translate409(cause: unknown): never {
  if (cause instanceof ApiError && cause.status === 409) {
    if (cause.code === "CANDIDATE_ALREADY_ACTIVATED") {
      throw new CandidateAlreadyActivated({} as AnimeConfigCandidate);
    }
    if (cause.code === "CANDIDATE_WORKING_DUPLICATE") {
      throw new CandidateWorkingDuplicate();
    }
    if (cause.currentVersion !== undefined) {
      throw new CandidateVersionConflict(cause.currentVersion);
    }
  }
  throw cause;
}

export class ServerCandidateStore implements CandidateStore {
  async list(query?: CandidateQuery): Promise<AnimeConfigCandidate[]> {
    return listAnimeConfigCandidates(query);
  }

  async get(id: string): Promise<AnimeConfigCandidate | null> {
    try {
      return await getAnimeConfigCandidate(id);
    } catch (cause) {
      // A missing candidate is a normal read outcome for this port, not a
      // failure; every other error still propagates.
      if (cause instanceof ApiError && cause.status === 404) return null;
      throw cause;
    }
  }

  async save(draft: AnimeDraft): Promise<AnimeConfigCandidate> {
    try {
      return await saveAnimeConfigCandidate(draft);
    } catch (cause) {
      translate409(cause);
    }
  }

  async patch(id: string, draft: AnimeDraft, expectedVersion: number): Promise<AnimeConfigCandidate> {
    try {
      return await patchAnimeConfigCandidate(id, draft, expectedVersion);
    } catch (cause) {
      // Zero automatic GET: only the conflict error carrying the reported currentVersion is thrown.
      translate409(cause);
    }
  }

  async subscribe(
    id: string,
    expectedVersion: number,
    idempotencyKey: string,
    input: CandidateSubscribeInput,
  ): Promise<SubscribeResult> {
    try {
      const result = await subscribeAnimeConfigCandidate(id, expectedVersion, idempotencyKey, input);
      return result;
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404) {
        throw new CandidateNotReady(id);
      }
      translate409(cause);
    }
  }

  async delete(id: string): Promise<void> {
    try {
      await deleteAnimeConfigCandidate(id);
    } catch (cause) {
      translate409(cause);
    }
  }
}

let singleton: ServerCandidateStore | undefined;

export function getCandidateStore(): CandidateStore {
  if (!singleton) {
    singleton = new ServerCandidateStore();
  }
  return singleton;
}
