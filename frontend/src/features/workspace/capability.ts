// The workspace's UI capability authority.
//
// The backend builds ManagedAnime's lifecycle as a standalone irreversible boundary: after
//   retirement the full owner graph (Release/DownloadTask/Artifact/FileOperation/logs) is kept,
//   only no longer accepting any mutation (the HTTP layer still answers 409 to breached writes).
//
// The frontend therefore decides exactly once, here: lifecycle_state maps to an explicit mutation
//   allowlist. Every workspace surface (tabs, RSS actions, card entries) only asks
//   `capability.can(...)`; scattered `lifecycleState === "retired"` checks are not permitted — a
//   missed one would render a guaranteed-409 button onto a retired instance.

/** ManagedAnime's irreversible lifecycle boundary (same origin as the backend's lifecycle_state). */
export type ManagedAnimeLifecycle = "active" | "retired";

/** Every mutation a workspace surface could attempt (read actions are not among them). */
export type WorkspaceMutation =
  | "metadata.edit"
  | "subgroups.edit"
  | "rules.edit"
  | "rules.preview"
  | "parser_policy.save"
  | "file_effect.save"
  | "rss.refresh"
  | "targets.scan"
  | "owner.retry"
  | "release.retry"
  | "rss.offset";

/**
 * read-only 生命周期下被禁止的 mutation 全集。写成显式集合而不是一个布尔短路，
 * 是为了让「哪些动作受生命周期约束」本身可读、可审：新增 mutation 必须在这里
 * 表态，而不是默认继承一个 `!readOnly`。
 */
const READ_ONLY_BLOCKED: ReadonlySet<WorkspaceMutation> = new Set<WorkspaceMutation>([
  "metadata.edit",
  "subgroups.edit",
  "rules.edit",
  "rules.preview",
  "parser_policy.save",
  "file_effect.save",
  "rss.refresh",
  "targets.scan",
  "owner.retry",
  "release.retry",
  "rss.offset",
]);

export interface WorkspaceCapability {
  /** The effective lifecycle state; absent/unknown is always treated as active (fail-closed applies to writes, not reads). */
  lifecycleState: ManagedAnimeLifecycle;
  /** Whether this is a read-only workspace (= retired). */
  readOnly: boolean;
  /** The sole mutation gate. */
  can: (mutation: WorkspaceMutation) => boolean;
}

/** Capabilities derived from the workspace read model's subscription.lifecycle_state. */
export function workspaceCapability(lifecycleState: ManagedAnimeLifecycle | undefined): WorkspaceCapability {
  const state: ManagedAnimeLifecycle = lifecycleState === "retired" ? "retired" : "active";
  const readOnly = state === "retired";
  return {
    lifecycleState: state,
    readOnly,
    can: (mutation) => !(readOnly && READ_ONLY_BLOCKED.has(mutation)),
  };
}

/** The fallback: every mutation allowed (draft/pre-subscription in-memory draft surfaces have no lifecycle owner). */
export function editableCapability(): WorkspaceCapability {
  return workspaceCapability("active");
}

/** The minimal structure of a listAnime entry: an owner identity plus lifecycle. */
export interface ManagedAnimeLifecycleRef {
  managed_anime: { id: string };
  subscription: { lifecycle_state?: ManagedAnimeLifecycle };
}

/**
 * 没有工作区读模型的面（全局日志）用 listAnime 反查某个 owner 的能力。
 *
 * 只有明确查到 lifecycle_state=active 的 owner 才返回可写 capability；id 缺失、
 * 列表未加载/查不到该 owner、或生命周期不是明确的 active 时返回 undefined。
 * 调用方必须把它当成 fail closed：拿不到证明就不放行 mutation 入口。
 */
export function animeOwnerCapability(
  anime: ReadonlyArray<ManagedAnimeLifecycleRef> | undefined,
  managedAnimeID: string | null | undefined,
): WorkspaceCapability | undefined {
  if (!anime || !managedAnimeID) return undefined;
  const item = anime.find((entry) => entry.managed_anime.id === managedAnimeID);
  if (!item) return undefined;
  // Missing/unknown fields are always treated as retired: only an explicit active proves writability; it is never "active by default".
  return workspaceCapability(item.subscription.lifecycle_state === "active" ? "active" : "retired");
}
