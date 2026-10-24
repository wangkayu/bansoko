import { useCallback, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  listCatalogSuppressions,
  suppressCatalogEntry,
  unsuppressCatalogEntry,
  type CatalogSuppression,
} from "./api/client";
import { catalogKeys } from "./queryKeys";
import { t } from "./i18n";

// Hidden anime (called "hidden" in the UI; the API and code keep the suppression/blacklist
// naming): the server's `catalog_suppressions` is the single source of truth, so the list no
// longer disappears when the browser changes. The old localStorage (key v2.blacklist) is no
// longer read or written and is not migrated — the hidden list is low-value rebuildable
// state and migration code would not be worth it.
//
// An entry key must carry the provider: an entry's providerEntryRef is a bare ref unique
// only within its provider — Mikan's 12345 and AniBT's 12345 are two different entries. The
// server guarantees this with UNIQUE (provider, provider_entry_ref); the frontend key must
// align with it.

/** Providers the server supports hiding. Custom presets are not among them — they have their own delete action. */
export type SuppressibleProvider = "mikan" | "anibt";

export interface BlacklistEntry {
  /** The globally unique key, shaped like `mikan:12345`. */
  key: string;
  source: SuppressibleProvider;
  /** The bare entry ref within the provider. */
  providerEntryRef: string;
  title: string;
  addedAt: string;
}

export interface BlacklistInput {
  source: string;
  providerEntryRef: string;
  title: string;
}

export const blacklistQueryKey = catalogKeys.suppressions;

export function blacklistKey(source: string, providerEntryRef: string): string {
  return `${source}:${providerEntryRef}`;
}

export function isSuppressibleProvider(source: string): source is SuppressibleProvider {
  return source === "mikan" || source === "anibt";
}

function toEntry(suppression: CatalogSuppression): BlacklistEntry {
  return {
    key: blacklistKey(suppression.provider, suppression.provider_entry_ref),
    source: suppression.provider as SuppressibleProvider,
    providerEntryRef: suppression.provider_entry_ref,
    title: suppression.title_snapshot,
    addedAt: suppression.suppressed_at,
  };
}

/**
 * 订阅隐藏列表。读取失败时返回空列表——由 {@link useBlacklistState} 的调用方
 * 呈现错误，浏览网格不因隐藏列表不可用而整体失效。
 */
export function useBlacklist(): BlacklistEntry[] {
  return useBlacklistState().entries;
}

/** A hidden-list read with load/error facts, for management UIs that need to show failure explicitly. */
export function useBlacklistState(): {
  entries: BlacklistEntry[];
  isLoading: boolean;
  error: unknown;
} {
  const query = useQuery({ queryKey: blacklistQueryKey, queryFn: listCatalogSuppressions });
  const entries = useMemo(() => (query.data ?? []).map(toEntry), [query.data]);
  return { entries, isLoading: query.isLoading, error: query.error };
}

/**
 * 隐藏列表写操作。两个动作都返回 Promise 并在失败时 reject，调用方负责提示；
 * 成功后使列表与目录读模型（其 preference 由服务端按 suppression 填充）失效。
 */
export function useBlacklistActions(): {
  /** Hide: always suppress (an idempotent PUT). */
  add: (input: BlacklistInput) => Promise<void>;
  remove: (entry: Pick<BlacklistEntry, "source" | "providerEntryRef">) => Promise<void>;
  pending: boolean;
} {
  const queryClient = useQueryClient();
  const invalidate = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: blacklistQueryKey });
    // The server fills a catalog entry's preference from suppressions, so a re-read is required after hiding.
    await queryClient.invalidateQueries({ queryKey: catalogKeys.all });
  }, [queryClient]);

  const suppress = useMutation({
    mutationFn: (input: { source: SuppressibleProvider; providerEntryRef: string; title: string }) =>
      suppressCatalogEntry(input.source, input.providerEntryRef, input.title),
    onSuccess: invalidate,
  });
  const restore = useMutation({
    mutationFn: (target: { source: SuppressibleProvider; providerEntryRef: string }) =>
      unsuppressCatalogEntry(target.source, target.providerEntryRef),
    onSuccess: invalidate,
  });

  const remove = useCallback(
    async (entry: Pick<BlacklistEntry, "source" | "providerEntryRef">) => {
      await restore.mutateAsync({ source: entry.source, providerEntryRef: entry.providerEntryRef });
    },
    [restore],
  );

  const add = useCallback(
    async (input: BlacklistInput): Promise<void> => {
      if (!isSuppressibleProvider(input.source)) {
        throw new Error(t("blacklist.customUnsupported"));
      }
      await suppress.mutateAsync({
        source: input.source,
        providerEntryRef: input.providerEntryRef,
        title: input.title,
      });
    },
    [suppress],
  );

  return { add, remove, pending: suppress.isPending || restore.isPending };
}
