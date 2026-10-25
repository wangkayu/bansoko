import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import {
  ApiError,
  fetchSeedSearchSnapshot,
  readSeedSearchSnapshot,
  searchProviderCatalog,
  toUserMessage,
  type CatalogItem,
  type SeedSearchSnapshotSeed,
  type SeedSearchSnapshotView,
} from "../../lib/api/client";
import { blacklistKey, useBlacklist } from "../../lib/blacklist";
import { getCatalogProvider, providerLabel } from "../../lib/providers/registry";
import { BrowseCard } from "./card";
import { mapCatalogItem } from "../../lib/providers/catalogProvider";
import type { CatalogProvider } from "../../lib/providers/port";
import type { CatalogEntryView, ProviderKey } from "../../lib/providers/viewmodels";
import { catalogKeys } from "./queries";
import { StateBlock } from "../../ui/StateBlock";
import { Dialog, Input, Segmented } from "../../ui/base";
import { Button } from "../../ui/base/controls";
import { loadAnimeConfigDialog } from "../anime-config/loadAnimeConfigDialog";
import { loadOneShotDialog } from "../oneshot/loadOneShotDialog";
import { SearchSeedOneShotDialog } from "../oneshot/SearchSeedOneShotDialog";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { t } from "../../lib/i18n";

const AnimeConfigDialogEntry = lazy(loadAnimeConfigDialog);
const OneShotDialogEntry = lazy(loadOneShotDialog);

// GlobalSearchDialog — the app's single search entry (the top-bar review: the catalog toolbar
//   no longer has search). One dialog, two modes, both sites (AniBT / Mikan):
//   - 番剧 mode reuses the provider keyword-search contract (searchProviderCatalog, a pure read).
//     Results are catalog entry candidates; a card click walks AnimeGrid's result->config path
//     (mapCatalogItem + the same AnimeConfigDialogEntry, where subscribing completes), and the
//     strip's browse icon opens the one-shot download entry. Hidden anime never appear in results.
//   - 种子 mode runs the provider torrent search: one POST persists the keyword result as a
//     short-lived search snapshot, and windows are read back by snapshot identity + cursor (at
//     most 100 rows; the note under the list says so instead of faking full pagination). A row's
//     only action is the one-shot download confirmation — torrents found by search are never a
//     subscription source. A stale identity (409) collapses the list into "search again".
//   Closing the dialog discards the transient search context; after a successful subscription the
//   catalog query invalidates so the catalog page reflects the latest state.

type SearchMode = "program" | "seed";
type SearchSite = "anibt" | "mikan";

const SEARCH_PROVIDERS: readonly SearchSite[] = ["anibt", "mikan"];
// One keyword window; both sites cap a keyword result at 100 rows.
const SEED_PAGE_SIZE = 20;

type CandidateTarget = {
  providerKey: ProviderKey;
  provider: CatalogProvider;
  entry: CatalogEntryView;
};

/** A search snapshot identity is only refreshable by searching again. */
function isStaleSearchFailure(cause: unknown): boolean {
  return (
    cause instanceof ApiError &&
    (cause.status === 409 ||
      cause.code === "SEARCH_SNAPSHOT_SCOPE_INVALID" ||
      cause.code === "SEARCH_SNAPSHOT_CURSOR_INVALID")
  );
}

export function GlobalSearchDialog({
  open,
  onOpenChange,
  onOpenRecords,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Jumps to the execution record from the one-shot download entry; the dialog renders no jump when the host has not wired it. */
  onOpenRecords?: () => void;
}) {
  useEffect(() => {
    void loadAnimeConfigDialog();
    void loadOneShotDialog();
  }, []);

  const queryClient = useQueryClient();
  const [keyword, setKeyword] = useState("");
  const [mode, setMode] = useState<SearchMode>("program");
  const [site, setSite] = useState<SearchSite>("anibt");

  // 番剧 mode: idle (never searched) and empty (searched, no results) are two distinct empty states.
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState<string>();
  const [items, setItems] = useState<CatalogItem[]>([]);
  const [searched, setSearched] = useState(false);

  // 种子 mode: one persisted search snapshot per query; rows accumulate window by window.
  const [seedSnapshot, setSeedSnapshot] = useState<SeedSearchSnapshotView | null>(null);
  const [seedRows, setSeedRows] = useState<SeedSearchSnapshotSeed[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [seedSearching, setSeedSearching] = useState(false);
  const [seedLoadingMore, setSeedLoadingMore] = useState(false);
  const [seedError, setSeedError] = useState<string>();
  const [seedPageError, setSeedPageError] = useState<string>();
  const [seedStale, setSeedStale] = useState(false);
  const [seedSearched, setSeedSearched] = useState(false);

  // The request sequence: a slow response must never overwrite a newer search's results.
  const requestSeq = useRef(0);
  const [configTarget, setConfigTarget] = useState<CandidateTarget | null>(null);
  const [seedTarget, setSeedTarget] = useState<CandidateTarget | null>(null);
  const [downloadTarget, setDownloadTarget] = useState<{ snapshot: SeedSearchSnapshotView; seed: SeedSearchSnapshotSeed } | null>(null);
  const hidden = useBlacklist();
  const shownItems = useMemo(() => {
    const hiddenKeys = new Set(hidden.map((entry) => entry.key));
    return items.filter((item) => !hiddenKeys.has(blacklistKey(item.provider, item.provider_entry_ref)));
  }, [hidden, items]);

  // Closing discards the transient context (query/results/error); in-flight requests are dropped with it.
  useEffect(() => {
    if (open) return;
    requestSeq.current += 1;
    setKeyword("");
    setItems([]);
    setError(undefined);
    setSearching(false);
    setSearched(false);
    setSeedSnapshot(null);
    setSeedRows([]);
    setNextCursor(null);
    setSeedSearching(false);
    setSeedLoadingMore(false);
    setSeedError(undefined);
    setSeedPageError(undefined);
    setSeedStale(false);
    setSeedSearched(false);
    setDownloadTarget(null);
  }, [open]);

  function resetResults() {
    requestSeq.current += 1;
    setItems([]);
    setError(undefined);
    setSearching(false);
    setSearched(false);
    setSeedSnapshot(null);
    setSeedRows([]);
    setNextCursor(null);
    setSeedSearching(false);
    setSeedLoadingMore(false);
    setSeedError(undefined);
    setSeedPageError(undefined);
    setSeedStale(false);
    setSeedSearched(false);
    setDownloadTarget(null);
  }

  async function runSearch() {
    const q = keyword.trim();
    if (!q) return;
    // The busy guard runs before a sequence number is taken: a second submit while a request
    // is in flight is a no-op, never an invalidation that strands the in-flight response
    // (its seq would stop matching and `searching` would never clear).
    if (mode === "program" ? searching : seedSearching) return;
    const seq = ++requestSeq.current;
    setDownloadTarget(null);
    if (mode === "program") {
      setSearching(true);
      setError(undefined);
      try {
        const result = await searchProviderCatalog(site, q);
        if (seq !== requestSeq.current) return;
        setItems(result);
        setSearched(true);
      } catch (cause) {
        if (seq !== requestSeq.current) return;
        setItems([]);
        setSearched(true);
        setError(toUserMessage(cause, t("grid.searchFailed")));
      } finally {
        if (seq === requestSeq.current) setSearching(false);
      }
      return;
    }
    setSeedSearching(true);
    setSeedError(undefined);
    setSeedPageError(undefined);
    setSeedStale(false);
    try {
      const view = await fetchSeedSearchSnapshot(site, { query: q, limit: SEED_PAGE_SIZE });
      if (seq !== requestSeq.current) return;
      setSeedSnapshot(view);
      setSeedRows(view.seeds);
      setNextCursor(view.next_cursor ?? null);
      setSeedSearched(true);
    } catch (cause) {
      if (seq !== requestSeq.current) return;
      setSeedSnapshot(null);
      setSeedRows([]);
      setNextCursor(null);
      setSeedSearched(true);
      setSeedError(toUserMessage(cause, t("grid.searchFailed")));
    } finally {
      if (seq === requestSeq.current) setSeedSearching(false);
    }
  }

  async function loadMoreSeeds() {
    const snapshot = seedSnapshot;
    const cursor = nextCursor;
    if (!snapshot || !cursor || seedLoadingMore) return;
    const seq = requestSeq.current;
    setSeedLoadingMore(true);
    setSeedPageError(undefined);
    try {
      const view = await readSeedSearchSnapshot(site, snapshot.snapshot_id, {
        scope_ref: snapshot.scope_ref,
        query_ref: snapshot.query_ref,
        snapshot_version: snapshot.snapshot_version,
        content_hash: snapshot.content_hash,
        cursor,
        limit: SEED_PAGE_SIZE,
      });
      if (seq !== requestSeq.current) return;
      setSeedRows((current) => [...current, ...view.seeds]);
      setNextCursor(view.next_cursor ?? null);
    } catch (cause) {
      if (seq !== requestSeq.current) return;
      if (isStaleSearchFailure(cause)) {
        // The snapshot identity is no longer provable: only a new search helps.
        setSeedStale(true);
        setNextCursor(null);
      } else {
        setSeedPageError(toUserMessage(cause, t("grid.searchFailed")));
      }
    } finally {
      if (seq === requestSeq.current) setSeedLoadingMore(false);
    }
  }

  function openCandidate(item: CatalogItem) {
    const providerKey = item.provider;
    setConfigTarget({
      providerKey,
      provider: getCatalogProvider(providerKey),
      entry: mapCatalogItem(item, providerKey),
    });
    onOpenChange(false);
  }

  // The same mapping path as a candidate click, but aimed at browse seeds; the search dialog closes
  //   too, so dialogs never stack.
  function openSeeds(item: CatalogItem) {
    const providerKey = item.provider;
    setSeedTarget({
      providerKey,
      provider: getCatalogProvider(providerKey),
      entry: mapCatalogItem(item, providerKey),
    });
    onOpenChange(false);
  }

  function handleSubscribed() {
    void queryClient.invalidateQueries({ queryKey: catalogKeys.all });
  }

  const busy = mode === "program" ? searching : seedSearching;

  return (
    <>
      <Dialog open={open} onRequestClose={() => onOpenChange(false)} tier="M" fixedHeight title={t("search.title")}>
        <div className="tw:mb-2 tw:flex tw:flex-wrap tw:items-center tw:gap-2">
          <Segmented<SearchMode>
            size="compact"
            aria-label={t("globalSearch.modeAria")}
            value={mode}
            onValueChange={(next) => {
              if (next === mode) return;
              setMode(next);
              resetResults();
            }}
            options={[
              { value: "program", label: t("globalSearch.modeProgram") },
              { value: "seed", label: t("globalSearch.modeSeed") },
            ]}
          />
          <Segmented<SearchSite>
            size="compact"
            aria-label={t("globalSearch.providerAria")}
            value={site}
            onValueChange={(next) => {
              if (next === site) return;
              setSite(next);
              resetResults();
            }}
            options={SEARCH_PROVIDERS.map((key) => ({ value: key, label: providerLabel(key) }))}
          />
        </div>
        <form
          className="global-search-form"
          onSubmit={(event) => {
            event.preventDefault();
            void runSearch();
          }}
        >
          <div className="global-search-keyword">
            <div className="tw:relative">
              <Input
                aria-label={t("globalSearch.keywordAria")}
                placeholder={mode === "program" ? t("globalSearch.placeholder") : t("globalSearch.seedPlaceholder")}
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                className="tw:pr-8"
              />
              {keyword ? (
                <button
                  type="button"
                  aria-label={t("globalSearch.clearAria")}
                  onClick={() => setKeyword("")}
                  className="tw:absolute tw:top-1/2 tw:right-2 tw:grid tw:size-5 tw:-translate-y-1/2 tw:place-items-center tw:rounded-full tw:text-muted-foreground tw:hover:text-foreground"
                >
                  <X className="tw:size-3.5" />
                </button>
              ) : null}
            </div>
          </div>
          <Button variant="primary" type="submit" disabled={busy || !keyword.trim()}>
            {busy ? t("grid.searching") : t("grid.search")}
          </Button>
        </form>

        {mode === "program" ? (
          <div className="global-search-results" aria-live="polite">
            {error ? (
              <StateBlock
                kind="error"
                title={error}
                action={
                  <Button size="sm" disabled={searching} onClick={() => void runSearch()}>
                    {t("grid.retry")}
                  </Button>
                }
              />
            ) : searching ? (
              <StateBlock kind="busy" title={t("grid.searchingNow")} />
            ) : !searched ? (
              <StateBlock kind="empty" title={t("globalSearch.start")} />
            ) : shownItems.length === 0 ? (
              <StateBlock kind="empty" title={t("search.noMatch")} description={t("grid.searchRetryHint")} />
            ) : (
              <ul className="global-search-list">
                {shownItems.map((item) => {
                  const entry = mapCatalogItem(item, item.provider);
                  return (
                    <li key={`${item.provider}:${item.provider_entry_ref}`}>
                      <BrowseCard
                        entry={entry}
                        onOpenConfig={() => openCandidate(item)}
                        onBrowseSeeds={() => openSeeds(item)}
                      />
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        ) : (
          <div className="global-search-results" aria-live="polite">
            {seedStale ? (
              <StateBlock
                kind="error"
                title={t("globalSearch.stale")}
                action={
                  <Button size="sm" disabled={seedSearching} onClick={() => void runSearch()}>
                    {t("globalSearch.searchAgain")}
                  </Button>
                }
              />
            ) : seedError ? (
              <StateBlock
                kind="error"
                title={seedError}
                action={
                  <Button size="sm" disabled={seedSearching} onClick={() => void runSearch()}>
                    {t("grid.retry")}
                  </Button>
                }
              />
            ) : seedSearching ? (
              <StateBlock kind="busy" title={t("grid.searchingNow")} />
            ) : !seedSearched ? (
              <StateBlock kind="empty" title={t("globalSearch.start")} description={t("globalSearch.seedHint")} />
            ) : seedRows.length === 0 ? (
              <StateBlock kind="empty" title={t("globalSearch.noMatchSeed")} description={t("grid.searchRetryHint")} />
            ) : (
              <div className="tw:grid tw:gap-2">
                <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">
                  {t("globalSearch.resultSummary", { n: seedSnapshot?.total_seeds ?? seedRows.length })}
                </p>
                <ul className="tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0">
                  {seedRows.map((seed) => (
                    <li
                      key={seed.stable_seed_key}
                      className="tw:flex tw:items-center tw:gap-3 tw:rounded-control tw:border tw:border-border tw:px-3 tw:py-2"
                    >
                      <span className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
                        <span className="break-anywhere tw:text-sm">{seed.title}</span>
                        <SeedMeta seed={seed} />
                      </span>
                      <Button
                        size="sm"
                        onClick={() => {
                          if (seedSnapshot) setDownloadTarget({ snapshot: seedSnapshot, seed });
                        }}
                      >
                        {t("globalSearch.downloadSeed")}
                      </Button>
                    </li>
                  ))}
                </ul>
                {seedPageError ? <p className="tw:m-0 tw:text-sm tw:text-destructive">{seedPageError}</p> : null}
                {nextCursor ? (
                  <div className="tw:flex tw:justify-center">
                    <Button size="sm" disabled={seedLoadingMore} onClick={() => void loadMoreSeeds()}>
                      {seedLoadingMore ? t("grid.searching") : t("globalSearch.loadMore")}
                    </Button>
                  </div>
                ) : null}
                {(seedSnapshot?.total_seeds ?? 0) >= 100 ? (
                  <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("globalSearch.seedWindowNote")}</p>
                ) : null}
              </div>
            )}
          </div>
        )}
      </Dialog>

      {/* The config dialog after a candidate click: the same AnimeConfigDialogEntry AnimeGrid uses
       *  (the search dialog is already closed by the time the second level opens, so no z-order clash). */}
      {configTarget && (
        <Suspense fallback={null}>
          <AnimeConfigDialogEntry
            open
            onOpenChange={(next) => {
              if (!next) setConfigTarget(null);
            }}
            provider={configTarget.provider}
            entry={configTarget.entry}
            sourceLabel={providerLabel(configTarget.providerKey)}
            onSubscribed={handleSubscribed}
          />
        </Suspense>
      )}
      {/* The candidate's browse seeds: another second-level dialog sitting alongside the config one. */}
      {seedTarget && (
        <Suspense fallback={null}>
          <OneShotDialogEntry
            key={`${seedTarget.providerKey}:${seedTarget.entry.providerEntryRef}`}
            open
            entry={seedTarget.entry}
            onClose={() => setSeedTarget(null)}
            onOpenRecords={onOpenRecords}
          />
        </Suspense>
      )}
      {/* A search row's one-shot download: the only action a found torrent has. Unlike the two
       *  program paths above the search dialog stays open underneath — closing it would discard
       *  the result list, and this confirmation is small enough to stack. */}
      {downloadTarget && (
        <SearchSeedOneShotDialog
          provider={site}
          snapshot={downloadTarget.snapshot}
          seed={downloadTarget.seed}
          onClose={() => setDownloadTarget(null)}
          onSuccess={() => setDownloadTarget(null)}
          onStale={() => {
            setDownloadTarget(null);
            setSeedStale(true);
            setNextCursor(null);
          }}
        />
      )}
    </>
  );
}

function SeedMeta({ seed }: { seed: SeedSearchSnapshotSeed }) {
  // Missing facts are omitted (no "unknown time" placeholders).
  const parts = [
    seed.published_at ? formatDateTime(seed.published_at) : undefined,
    seed.size_bytes !== null && seed.size_bytes !== undefined ? formatBytes(seed.size_bytes) : undefined,
    seed.group_name ?? undefined,
    seed.resolution ?? undefined,
  ].filter((part): part is string => Boolean(part));
  if (parts.length === 0) return null;
  return <span className="tw:text-xs tw:text-muted-foreground">{parts.join(" · ")}</span>;
}
