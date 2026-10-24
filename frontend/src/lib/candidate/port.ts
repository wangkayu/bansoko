import type { AnimeConfigCandidate, AnimeDraft, CandidateSubscribeInput, SubscribeResult } from "../api/client";
import { t } from "../i18n";

// AnimeConfigCandidate is a persisted planning configuration, not a runtime owner like
// ManagedAnime / Subscription / RSSSource. Saving a candidate produces no scheduler or
// runtime writes; only an explicit activation (subscribing) atomically creates the formal
// owner. The candidate source of truth is the server's `/api/v1/anime-config-candidates`,
// and types come straight from the generated contract rather than a copy here.

export type { AnimeConfigCandidate };

export type CandidateOrigin = AnimeConfigCandidate["origin"];

/** Query parameters of the candidate list. */
export interface CandidateQuery {
  origin?: CandidateOrigin;
  origin_entry_ref?: string;
}

/** A candidate CAS version conflict. Carries only the currentVersion the server reports; no automatic GET. */
export class CandidateVersionConflict extends Error {
  readonly currentVersion: number;
  constructor(currentVersion: number) {
    super(t("config.validation.versionConflict"));
    this.name = "CandidateVersionConflict";
    this.currentVersion = currentVersion;
  }
}

/** provider identity already has an unlinked working draft. */
export class CandidateWorkingDuplicate extends Error {
  constructor() {
    super(t("config.validation.draftExists"));
    this.name = "CandidateWorkingDuplicate";
  }
}

/** The candidate is not saved yet (cannot be activated). */
export class CandidateNotReady extends Error {
  constructor(id: string) {
    super(t("config.validation.notSaved"));
    this.name = "CandidateNotReady";
  }
}

/** The candidate is already activated (cannot be activated again under a new key). */
export class CandidateAlreadyActivated extends Error {
  readonly candidate: AnimeConfigCandidate;
  constructor(candidate: AnimeConfigCandidate) {
    super(t("config.validation.alreadySubscribed"));
    this.name = "CandidateAlreadyActivated";
    this.candidate = candidate;
  }
}

// CandidateStore is the single boundary through which the catalog / anime-config UI reads
// and writes candidates. Pages depend only on this interface; see serverCandidateStore.ts
// for the implementation (the candidate source of truth is on the server).

export interface CandidateStore {
  /** Reads candidates; no side effects, no provider / qB / scheduler calls. */
  list(query?: CandidateQuery): Promise<AnimeConfigCandidate[]>;

  /** Reads one candidate by id. */
  get(id: string): Promise<AnimeConfigCandidate | null>;

  /** Saves a full draft and returns the candidate at a new version; creates no runtime owner. */
  save(draft: AnimeDraft): Promise<AnimeConfigCandidate>;

  /**
   * 以 CAS 替换候选完整 draft。expectedVersion 过旧时抛
   * CandidateVersionConflict，不覆盖较新的 draft。
   */
  patch(id: string, draft: AnimeDraft, expectedVersion: number): Promise<AnimeConfigCandidate>;

  /**
   * 激活最近保存的候选：原子创建正式 owner 集合并链接
   * activated_managed_anime_id。dirty / 未保存版本不能激活。
   * 发送 If-Match: expectedVersion 与 Idempotency-Key。
   * `input.initial_state: "stopped"` 用于一次性下载：实例建为已停止，
   * 不进入自动调度，后续手动刷新 + 逐条重试走完整流水线。
   */
  subscribe(id: string, expectedVersion: number, idempotencyKey: string, input: CandidateSubscribeInput): Promise<SubscribeResult>;

  /** Deletes an unactivated candidate preset; an activated candidate is a runtime record and does not go through here. */
  delete(id: string): Promise<void>;
}
